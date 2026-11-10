// Package telemetry configures optional OpenTelemetry export over OTLP/HTTP
// for the CLI and the private UI server. Telemetry is opt-in: without a
// configuration file the providers stay no-ops and nothing leaves the
// process. Exported data is limited to TriageLab-authored identifiers,
// states, and durations; case content, credentials, and model prompts are
// never instrumented.
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Version is the workspace version reported as service.version. Keep it in
// sync with the version line printed by the CLI.
const Version = "0.1.0"

// Config mirrors triagelab.telemetry.json (see triagelab.telemetry.example.json
// and chart values under telemetry.config). The endpoint is the OTLP/HTTP
// collector base URL; /v1/{traces,metrics,logs} is appended by the exporters.
type Config struct {
	Endpoint              string  `json:"endpoint"`
	Insecure              bool    `json:"insecure,omitempty"`
	HeadersEnv            string  `json:"headers_env,omitempty"`
	SampleRatio           float64 `json:"sample_ratio,omitempty"`
	ExportIntervalSeconds int     `json:"export_interval_seconds,omitempty"`
}

func DefaultConfig() Config {
	return Config{SampleRatio: 1.0, ExportIntervalSeconds: 30}
}

func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return c, err
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 65537))
		d.DisallowUnknownFields()
		if err = d.Decode(&c); err != nil {
			return c, fmt.Errorf("telemetry configuration: %w", err)
		}
		var extra any
		if err = d.Decode(&extra); err != io.EOF {
			return c, fmt.Errorf("telemetry configuration must contain one JSON object")
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("telemetry endpoint must be nonempty")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("telemetry endpoint must be HTTP(S) without credentials, query or fragment")
	}
	if u.Scheme == "http" && !isLoopbackHostname(u.Hostname()) && !c.Insecure {
		return fmt.Errorf("plain-HTTP telemetry is allowed only on loopback or with insecure")
	}
	if u.Scheme == "https" && c.Insecure {
		return fmt.Errorf("insecure does not apply to HTTPS telemetry endpoints")
	}
	if c.HeadersEnv != "" && !envName.MatchString(c.HeadersEnv) {
		return fmt.Errorf("headers_env must name an environment variable")
	}
	if c.SampleRatio < 0 || c.SampleRatio > 1 {
		return fmt.Errorf("sample_ratio must be between 0 and 1")
	}
	if c.ExportIntervalSeconds < 5 || c.ExportIntervalSeconds > 300 {
		return fmt.Errorf("export_interval_seconds must be between 5 and 300")
	}
	return nil
}

func isLoopbackHostname(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

var (
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerName = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

// Providers owns the OTel providers registered by Setup. Shutdown flushes
// buffered telemetry and must complete within the caller's bounded deadline;
// after Shutdown the global providers are no-ops again.
type Providers struct {
	traces  *sdktrace.TracerProvider
	metrics *sdkmetric.MeterProvider
	logs    *sdklog.LoggerProvider
}

// Setup registers global trace, metric, and log providers exporting OTLP/HTTP
// to the configured collector. Export is best effort: collector failures are
// reported on stderr and never interrupt an investigation. It returns an
// error for configuration mistakes (including a missing headers environment
// variable) so the CLI can fail fast at startup.
func Setup(ctx context.Context, c Config) (*Providers, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	headers, err := headersFromEnv(c)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName("triagelab"),
		semconv.ServiceVersion(Version),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}
	base := strings.TrimRight(c.Endpoint, "/")
	traceOptions := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(base + "/v1/traces"),
		otlptracehttp.WithHeaders(headers),
	}
	metricOptions := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpointURL(base + "/v1/metrics"),
		otlpmetrichttp.WithHeaders(headers),
	}
	logOptions := []otlploghttp.Option{
		otlploghttp.WithEndpointURL(base + "/v1/logs"),
		otlploghttp.WithHeaders(headers),
	}
	// WithEndpointURL maps the URL path verbatim, so the signal paths above are
	// always explicit; insecure follows the scheme the same way.
	if u, _ := url.Parse(c.Endpoint); u.Scheme == "http" {
		traceOptions = append(traceOptions, otlptracehttp.WithInsecure())
		metricOptions = append(metricOptions, otlpmetrichttp.WithInsecure())
		logOptions = append(logOptions, otlploghttp.WithInsecure())
	}
	traceExporter, err := otlptracehttp.New(ctx, traceOptions...)
	if err != nil {
		return nil, fmt.Errorf("telemetry trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(ctx, metricOptions...)
	if err != nil {
		return nil, fmt.Errorf("telemetry metric exporter: %w", err)
	}
	logExporter, err := otlploghttp.New(ctx, logOptions...)
	if err != nil {
		return nil, fmt.Errorf("telemetry log exporter: %w", err)
	}
	p := &Providers{
		traces: sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.SampleRatio))),
		),
		metrics: sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
				sdkmetric.WithInterval(time.Duration(c.ExportIntervalSeconds)*time.Second))),
		),
		logs: sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		),
	}
	otel.SetTracerProvider(p.traces)
	otel.SetMeterProvider(p.metrics)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	logger.Store(otelslog.NewLogger("github.com/bondrenai/TriageLab", otelslog.WithLoggerProvider(p.logs)))
	return p, nil
}

// Shutdown flushes and releases every provider. Call it once with a bounded
// context before the process exits; export failures are joined, not fatal.
func (p *Providers) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var errs []error
	for _, shutdown := range []func(context.Context) error{p.traces.Shutdown, p.metrics.Shutdown, p.logs.Shutdown} {
		if err := shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var logger atomic.Pointer[slog.Logger]

func init() {
	logger.Store(slog.New(slog.DiscardHandler))
}

// Logger returns the process logger. Records are discarded until Setup
// registers the OTLP bridge, so call sites never need to check for telemetry.
func Logger() *slog.Logger { return logger.Load() }

// headersFromEnv reads newline-separated "Name: value" header lines from the
// environment variable named by headers_env, mirroring the token_env
// indirection used for credentials. Header values never enter configuration
// files or Helm values.
func headersFromEnv(c Config) (map[string]string, error) {
	if c.HeadersEnv == "" {
		return nil, nil
	}
	value := os.Getenv(c.HeadersEnv)
	headers := map[string]string{}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		if !ok || !headerName.MatchString(name) || val == "" || strings.ContainsAny(val, "\x00\r\n\t") {
			return nil, fmt.Errorf("telemetry headers must be one 'Name: value' pair per line in %s", c.HeadersEnv)
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("telemetry headers environment variable %s is empty or unset", c.HeadersEnv)
	}
	return headers, nil
}
