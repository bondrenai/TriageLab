package investigation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestRunTelemetrySpansAndRedaction runs a fixture investigation under an
// in-memory tracer and asserts the span shape plus the security boundary:
// identifiers and fixed states only, never case content or credentials.
func TestRunTelemetrySpansAndRedaction(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	f := newFixture()
	f.summaryReply = true
	dir := filepath.Join(t.TempDir(), "report")
	secret := "top-secret-token-value"
	investigator := Investigator{Client: f, Config: fixtureConfig(), Trigger: "test", Secrets: []string{secret}}
	c := casefile.Case{ID: "telemetry-case", Description: "unique case description marker", Attachments: []casefile.Attachment{{Path: "app.py", Content: []byte("example"), SizeBytes: 7}}}
	result, err := investigator.Run(context.Background(), c, dir)
	if err != nil || result.Status != StatusCompleted {
		t.Fatalf("run failed: %v %v", result, err)
	}

	spans := exporter.GetSpans()
	names := map[string]bool{}
	for _, span := range spans {
		names[span.Name] = true
		if span.Name != "investigation.run" && span.Name != "investigation.cleanup" && strings.Contains(span.Name, "telemetry-case") {
			t.Fatalf("run identifier leaked into span name %q", span.Name)
		}
		for _, attr := range span.Attributes {
			text := attr.Value.String()
			if strings.Contains(text, secret) || strings.Contains(text, "unique case description marker") {
				t.Fatalf("span %q attribute %q=%q leaked sensitive content", span.Name, attr.Key, text)
			}
		}
		if span.Status.Description != "" && (strings.Contains(span.Status.Description, secret) || strings.Contains(span.Status.Description, "unique case")) {
			t.Fatalf("span %q status description leaked content: %q", span.Name, span.Status.Description)
		}
	}
	runSpan := findSpanByName(spans, "investigation.run")
	if runSpan == nil {
		t.Fatal("investigation.run span missing")
	}
	attrs := spanAttributes(runSpan)
	if attrs["run_id"] != "telemetry-case" || attrs["trigger"] != "test" || attrs["status"] != "completed" || attrs["outcome"] != "reproduced" {
		t.Fatalf("run span attributes incomplete: %v", attrs)
	}
	for _, phase := range []string{
		"investigation.sandbox.create", "investigation.inputs.upload",
		"investigation.session.create", "investigation.session.invoke",
		"investigation.session.poll", "investigation.evidence.observe",
		"investigation.cleanup",
	} {
		if !names[phase] {
			t.Fatalf("phase span missing: %s (have %v)", phase, names)
		}
	}
}

func findSpanByName(spans []tracetest.SpanStub, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

func spanAttributes(span *tracetest.SpanStub) map[string]string {
	attrs := map[string]string{}
	for _, attr := range span.Attributes {
		attrs[string(attr.Key)] = attr.Value.String()
	}
	return attrs
}
