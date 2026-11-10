package webserver

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// HTTP telemetry is opt-in like all instrumentation: without telemetry.Setup
// these are no-ops. Routes collapse to fixed patterns so metrics stay low
// cardinality; request bodies, query strings, and account names are never
// instrumented.
const instrumentation = "github.com/bondrenai/TriageLab/internal/webserver"

func tracer() trace.Tracer { return otel.Tracer(instrumentation) }

type httpInstruments struct {
	requests metric.Int64Counter
	duration metric.Float64Histogram
}

var loadHTTPInstruments = sync.OnceValue(func() *httpInstruments {
	m := otel.Meter(instrumentation)
	i := &httpInstruments{}
	i.requests, _ = m.Int64Counter("triagelab.http.requests", metric.WithDescription("HTTP requests handled by the private UI server"))
	i.duration, _ = m.Float64Histogram("triagelab.http.duration", metric.WithDescription("HTTP request handling time in seconds"), metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30))
	return i
})

func (i *httpInstruments) request(method, route string, status int, seconds float64) {
	if i == nil || i.requests == nil || i.duration == nil {
		return
	}
	attributes := []attribute.KeyValue{
		attribute.String("route", route),
		attribute.String("method", method),
		attribute.Int("status", status),
	}
	i.requests.Add(context.Background(), 1, metric.WithAttributes(attributes...))
	i.duration.Record(context.Background(), seconds, metric.WithAttributes(attributes[:2]...))
}

// statusRecorder captures the response status for telemetry without altering
// response behavior.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// routePattern collapses a request path to a fixed route label. Variable
// segments (run and ticket identifiers) become {id}; every other segment is
// part of a fixed route vocabulary.
func routePattern(path string) string {
	if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/oauth/") && path != "/healthz" {
		return "static"
	}
	if path == "/healthz" || path == ticketflow.OAuthCallbackPath {
		return path
	}
	segments := strings.Split(strings.Trim(strings.TrimSuffix(path, "/"), "/"), "/")
	if len(segments) < 2 || segments[0] != "api" {
		return "other"
	}
	switch {
	case len(segments) == 2:
		return "/" + strings.Join(segments, "/")
	case segments[1] == "auth" && len(segments) == 3:
		return "/" + strings.Join(segments, "/")
	case segments[1] == "auth" && len(segments) >= 4 && segments[2] == "admin" && segments[3] == "users":
		return "/api/auth/admin/users"
	case segments[1] == "auth" && len(segments) == 4 && segments[2] == "oidc":
		return "/api/auth/oidc/callback"
	case segments[1] == "auth":
		return "/api/auth/other"
	case segments[1] == "runs" && len(segments) == 3:
		return "/api/runs/{id}"
	case segments[1] == "runs":
		return "/api/runs/{id}/" + strings.Join(segments[3:], "/")
	case segments[1] == "tickets" && len(segments) == 3:
		return "/" + strings.Join(segments, "/")
	case segments[1] == "tickets" && len(segments) == 4 && segments[2] == "oauth":
		return "/" + strings.Join(segments, "/")
	case segments[1] == "tickets":
		return "/api/tickets/{id}/" + strings.Join(segments[3:], "/")
	default:
		if len(segments) == 3 {
			return "/" + strings.Join(segments, "/")
		}
		// Unknown deeper API paths collapse so labels stay bounded.
		return "/api/other"
	}
}
