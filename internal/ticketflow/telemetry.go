package ticketflow

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Ticket telemetry is opt-in like all instrumentation: without
// telemetry.Setup these are no-ops. Spans and counters cover the
// network-touching actions with a fixed verb and outcome; ticket ids,
// subjects, requester identities, and comment bodies are never recorded.
const instrumentation = "github.com/bondrenai/TriageLab/internal/ticketflow"

func tracer() trace.Tracer { return otel.Tracer(instrumentation) }

var loadInstruments = sync.OnceValue(func() metric.Int64Counter {
	c, _ := otel.Meter(instrumentation).Int64Counter("triagelab.ticketflow.actions",
		metric.WithDescription("Ticket actions by action and outcome"))
	return c
})

// actionResult classifies a ticket action outcome into a fixed vocabulary.
func actionResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrBlocked):
		return "blocked"
	case errors.Is(err, ErrChanged):
		return "changed"
	case errors.Is(err, ErrStorage):
		return "storage"
	case errors.Is(err, ErrOAuthRequired), errors.Is(err, ErrOAuthHost),
		errors.Is(err, ErrOAuthCallback), errors.Is(err, ErrOAuthDenied),
		errors.Is(err, ErrOAuthExchange):
		return "oauth"
	default:
		return "error"
	}
}

// traced wraps one ticket action with a span and an outcome counter. Call the
// returned finish function exactly once with the action's final error.
func traced(ctx context.Context, action string) (context.Context, func(error)) {
	ctx, span := tracer().Start(ctx, "ticketflow."+action)
	return ctx, func(err error) {
		result := actionResult(err)
		span.SetAttributes(attribute.String("result", result))
		if err != nil {
			span.SetStatus(codes.Error, "ticket action ended with "+result)
		}
		span.End()
		if counter := loadInstruments(); counter != nil {
			counter.Add(context.Background(), 1,
				metric.WithAttributes(attribute.String("action", action), attribute.String("result", result)))
		}
	}
}
