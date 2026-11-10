package investigation

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Instrumentation is opt-in: without telemetry.Setup these are no-ops, so
// workflow code never checks for a configured collector. Attributes are
// limited to fixed states and identifiers; case content and credentials are
// never instrumented.
const instrumentation = "github.com/bondrenai/TriageLab/internal/investigation"

func tracer() trace.Tracer { return otel.Tracer(instrumentation) }

type instruments struct {
	runs        metric.Int64Counter
	duration    metric.Float64Histogram
	experiments metric.Int64Histogram
	cleanup     metric.Int64Counter
	watchdog    metric.Int64Counter
}

var loadInstruments = sync.OnceValue(func() *instruments {
	m := otel.Meter(instrumentation)
	i := &instruments{}
	i.runs, _ = m.Int64Counter("triagelab.investigation.runs", metric.WithDescription("Completed investigations"))
	i.duration, _ = m.Float64Histogram("triagelab.investigation.duration", metric.WithDescription("Investigation wall time in seconds, including cleanup"), metric.WithExplicitBucketBoundaries(1, 10, 60, 300, 600, 1200, 3000))
	i.experiments, _ = m.Int64Histogram("triagelab.investigation.experiments", metric.WithDescription("Experiments observed per investigation"), metric.WithExplicitBucketBoundaries(1, 2, 4, 8, 16, 32))
	i.cleanup, _ = m.Int64Counter("triagelab.investigation.cleanup", metric.WithDescription("Cleanup outcomes by completeness"))
	i.watchdog, _ = m.Int64Counter("triagelab.watchdog.kills", metric.WithDescription("Sandbox processes killed by the client watchdog"))
	return i
})

func (i *instruments) runEnded(status, outcome, trigger string, seconds float64, experimentCount int) {
	if i == nil || i.runs == nil || i.duration == nil {
		return
	}
	attributes := []attribute.KeyValue{
		attribute.String("status", status),
		attribute.String("outcome", outcome),
		attribute.String("trigger", trigger),
	}
	i.runs.Add(context.Background(), 1, metric.WithAttributes(attributes...))
	i.duration.Record(context.Background(), seconds, metric.WithAttributes(attribute.String("status", status)))
	if i.experiments != nil {
		i.experiments.Record(context.Background(), int64(experimentCount))
	}
}

func (i *instruments) cleanupEnded(complete bool) {
	if i == nil || i.cleanup == nil {
		return
	}
	result := "incomplete"
	if complete {
		result = "complete"
	}
	i.cleanup.Add(context.Background(), 1, metric.WithAttributes(attribute.String("result", result)))
}

func (i *instruments) processKilled() {
	if i == nil || i.watchdog == nil {
		return
	}
	i.watchdog.Add(context.Background(), 1)
}
