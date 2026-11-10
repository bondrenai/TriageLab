---
title: Telemetry
description: Optional OpenTelemetry export of traces, metrics, and logs over OTLP/HTTP.
---

TriageLab can export **traces, metrics, and logs** over OTLP/HTTP to your own
collector when a telemetry configuration file is supplied. Without one, every
provider stays a no-op and nothing leaves the process.

## Configuration

Pass `--telemetry` to `investigate`, `serve`, `doctor`, or `cleanup`, or set
`telemetry.enabled` with `telemetry.config` in the Helm chart (rendered
verbatim to `telemetry.json` in the ConfigMap):

```json
{
  "endpoint": "http://127.0.0.1:4318",
  "headers_env": "TRIAGELAB_OTLP_HEADERS",
  "sample_ratio": 1.0,
  "export_interval_seconds": 30
}
```

- `endpoint` is the OTLP/HTTP base URL. TriageLab appends
  `/v1/traces`, `/v1/metrics`, and `/v1/logs` itself; a base path prefix is
  honored. Plain HTTP is accepted on loopback or with `insecure: true` for an
  in-cluster collector Service; anything else must be HTTPS.
- `headers_env` names an environment variable holding newline-separated
  `Name: value` lines for collector authentication — headers never go through
  configuration files or Helm values. In the chart, supply them via
  `telemetry.headers.value` or `telemetry.headers.secretName`.
- `sample_ratio` controls trace sampling; `export_interval_seconds` controls
  the batch cadence.

## What is exported

The Go SDK's batch processors send spans, metrics, and structured logs, with
W3C `traceparent` propagation into kagent MCP requests so a run traces end to
end. Export is **best effort**: collector failures surface on stderr and never
change investigation outcomes, and the shutdown flush is bounded to five
seconds.

Attributes are limited to fixed vocabularies — statuses, outcomes, trigger,
MCP method and tool names, error kinds, collapsed route patterns — and run
identifiers. Case descriptions, attachments, model prompts, tool outputs,
credentials, and account names are never instrumented; phase log messages
pass through the report redactor first.

This is an **outbound** connection from the workspace process (or pod) to
your collector. Bring your own OTLP endpoint; no collector is shipped, and
the loopback-only UI model is unchanged.

The export path is verified by unit tests against an in-process OTLP/HTTP
collector decoding the exported protobuf for all three signals, and by a
recorded live run against a real collector — see the
[verification record](https://github.com/bondrenai/TriageLab/blob/main/docs/verification.md).
