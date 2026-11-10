package telemetry

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracespb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigValidates(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string
	}{
		{name: "minimal loopback http", json: `{"endpoint": "http://127.0.0.1:4318"}`},
		{name: "https endpoint", json: `{"endpoint": "https://collector.example.com"}`},
		{name: "insecure http off loopback", json: `{"endpoint": "http://collector.example.com", "insecure": true}`},
		{name: "explicit sampling", json: `{"endpoint": "http://localhost:4318", "sample_ratio": 0.25, "export_interval_seconds": 15}`},
		{name: "missing endpoint", json: `{}`, wantErr: "endpoint must be nonempty"},
		{name: "http off loopback", json: `{"endpoint": "http://collector.example.com"}`, wantErr: "plain-HTTP telemetry"},
		{name: "https with insecure", json: `{"endpoint": "https://collector.example.com", "insecure": true}`, wantErr: "insecure does not apply"},
		{name: "bad ratio", json: `{"endpoint": "http://127.0.0.1:4318", "sample_ratio": 1.5}`, wantErr: "sample_ratio"},
		{name: "zero ratio", json: `{"endpoint": "http://127.0.0.1:4318", "sample_ratio": 0}`},
		{name: "interval too small", json: `{"endpoint": "http://127.0.0.1:4318", "export_interval_seconds": 4}`, wantErr: "export_interval_seconds"},
		{name: "interval too large", json: `{"endpoint": "http://127.0.0.1:4318", "export_interval_seconds": 301}`, wantErr: "export_interval_seconds"},
		{name: "bad headers env name", json: `{"endpoint": "http://127.0.0.1:4318", "headers_env": "1BAD"}`, wantErr: "headers_env"},
		{name: "unknown field", json: `{"endpoint": "http://127.0.0.1:4318", "colors": true}`, wantErr: "unknown field"},
		{name: "query in endpoint", json: `{"endpoint": "http://127.0.0.1:4318/?x=1"}`, wantErr: "must be HTTP(S)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadConfig(writeConfig(t, tc.json))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v (config %+v)", tc.wantErr, err, c)
			}
		})
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, `{"endpoint": "http://[::1]:4318"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.SampleRatio != 1.0 || c.ExportIntervalSeconds != 30 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if _, err := LoadConfig(""); err == nil || !strings.Contains(err.Error(), "endpoint must be nonempty") {
		t.Fatalf("empty path has no default endpoint and should not validate: %v", err)
	}
}

// restoreGlobals resets the process-wide providers after a test registers
// real ones, keeping package tests order-independent.
func restoreGlobals(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithResource(sdkresource.Empty())))
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
		logger.Store(slog.New(slog.DiscardHandler))
	})
}

func TestSetupExportsAllSignals(t *testing.T) {
	restoreGlobals(t)
	var mu sync.Mutex
	var bodies map[string][]byte
	var headerSeen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if bodies == nil {
			bodies = map[string][]byte{}
		}
		bodies[r.URL.Path] = body
		headerSeen = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("TL_TEST_HEADERS", "Authorization: Basic test-headers\nX-Org: acme")
	cfg, err := LoadConfig(writeConfig(t, `{"endpoint": "`+srv.URL+`", "headers_env": "TL_TEST_HEADERS"}`))
	if err != nil {
		t.Fatal(err)
	}
	providers, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, span := otel.Tracer("test").Start(context.Background(), "test.span")
	span.SetAttributes(attribute.String("safe", "enum-value"))
	traceID := span.SpanContext().TraceID()
	counter, err := otel.Meter("test").Int64Counter("test.counter")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "unit")))
	Logger().InfoContext(ctx, "test log message", "attr", "value")
	span.End()

	flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := providers.Shutdown(flush); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if headerSeen.Get("Authorization") != "Basic test-headers" || headerSeen.Get("X-Org") != "acme" {
		t.Fatalf("collector did not receive configured headers: %v", headerSeen)
	}
	var traceReq tracespb.ExportTraceServiceRequest
	if err := proto.Unmarshal(bodies["/v1/traces"], &traceReq); err != nil {
		t.Fatalf("decode traces: %v", err)
	}
	if !hasResourceAttribute(traceReq.ResourceSpans, "service.name", "triagelab") {
		t.Fatal("spans are missing the triagelab service resource")
	}
	if findSpan(traceReq.ResourceSpans, "test.span") == nil {
		t.Fatal("test.span was not exported")
	}
	var metricReq metricspb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(bodies["/v1/metrics"], &metricReq); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if findMetric(metricReq.ResourceMetrics, "test.counter") == nil {
		t.Fatal("test.counter was not exported")
	}
	var logReq logspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(bodies["/v1/logs"], &logReq); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	record := findLog(logReq.ResourceLogs, "test log message")
	if record == nil {
		t.Fatal("test log message was not exported")
	}
	if !bytesEqual(record.TraceId, traceID[:]) {
		t.Fatalf("log record is not correlated with its span trace: %x vs %x", record.TraceId, traceID[:])
	}
}

func TestSetupHeadersErrors(t *testing.T) {
	restoreGlobals(t)
	t.Setenv("TL_MALFORMED", "no-colon-line")
	cases := []struct {
		name, env, value, wantErr string
	}{
		{name: "unset variable", env: "TL_UNSET", value: "", wantErr: "empty or unset"},
		{name: "malformed line", env: "TL_MALFORMED", value: "no-colon-line", wantErr: "one 'Name: value' pair"},
		{name: "header value with newline", env: "TL_CTRL", value: "X-A: ok\nX-B: bad\tvalue", wantErr: "one 'Name: value' pair"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			cfg, err := LoadConfig(writeConfig(t, `{"endpoint": "http://127.0.0.1:4318", "headers_env": "`+tc.env+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Setup(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoggerDiscardsBeforeSetup(t *testing.T) {
	restoreGlobals(t)
	if Logger() == nil {
		t.Fatal("Logger() must never be nil")
	}
	Logger().InfoContext(context.Background(), "discarded", "k", "v")
}

func hasResourceAttribute(spans []*tracev1.ResourceSpans, key, value string) bool {
	for _, rs := range spans {
		for _, attr := range rs.Resource.Attributes {
			if attr.Key == key && attr.Value.GetStringValue() == value {
				return true
			}
		}
	}
	return false
}

func findSpan(spans []*tracev1.ResourceSpans, name string) *tracev1.Span {
	for _, rs := range spans {
		for _, scope := range rs.ScopeSpans {
			for _, span := range scope.Spans {
				if span.Name == name {
					return span
				}
			}
		}
	}
	return nil
}

func findMetric(metrics []*metricsv1.ResourceMetrics, name string) *metricsv1.Metric {
	for _, rm := range metrics {
		for _, scope := range rm.ScopeMetrics {
			for _, m := range scope.Metrics {
				if m.Name == name {
					return m
				}
			}
		}
	}
	return nil
}

func findLog(logs []*logsv1.ResourceLogs, body string) *logsv1.LogRecord {
	for _, rl := range logs {
		for _, scope := range rl.ScopeLogs {
			for _, record := range scope.LogRecords {
				if record.Body != nil && record.Body.GetStringValue() == body {
					return record
				}
			}
		}
	}
	return nil
}

func bytesEqual(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
