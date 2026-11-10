package kagent

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Client telemetry is opt-in like all instrumentation: without
// telemetry.Setup these are no-ops. Attributes are limited to the fixed MCP
// method and tool vocabulary and upstream error kinds; request payloads,
// which carry case content and credentials, are never instrumented.
const instrumentation = "github.com/bondrenai/TriageLab/internal/kagent"

func tracer() trace.Tracer { return otel.Tracer(instrumentation) }

type instruments struct {
	rpc     metric.Float64Histogram
	errors  metric.Int64Counter
	kubectl metric.Float64Histogram
}

var loadInstruments = sync.OnceValue(func() *instruments {
	m := otel.Meter(instrumentation)
	i := &instruments{}
	i.rpc, _ = m.Float64Histogram("triagelab.kagent.rpc.duration", metric.WithDescription("kagent MCP call latency in seconds"), metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 20))
	i.errors, _ = m.Int64Counter("triagelab.kagent.rpc.errors", metric.WithDescription("kagent MCP call failures"))
	i.kubectl, _ = m.Float64Histogram("triagelab.kagent.kubectl.duration", metric.WithDescription("Deployment status kubectl calls in seconds"), metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 5))
	return i
})

func (i *instruments) rpcEnded(call string, seconds float64, failed bool) {
	if i == nil || i.rpc == nil {
		return
	}
	i.rpc.Record(context.Background(), seconds, metric.WithAttributes(attribute.String("call", call)))
	if failed && i.errors != nil {
		i.errors.Add(context.Background(), 1, metric.WithAttributes(attribute.String("call", call)))
	}
}

func (i *instruments) kubectlEnded(operation string, seconds float64) {
	if i == nil || i.kubectl == nil {
		return
	}
	i.kubectl.Record(context.Background(), seconds, metric.WithAttributes(attribute.String("operation", operation)))
}

// errorKind classifies an rpc failure for telemetry without echoing payload
// content; kagent error messages are TriageLab-authored by construction.
func errorKind(err error) string {
	var upstream *Error
	if errors.As(err, &upstream) {
		return upstream.Kind
	}
	return "client"
}
