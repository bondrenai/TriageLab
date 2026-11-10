---
title: "Configuration"
description: "The investigation configuration file: every field, default, and bound, generated from the Go configuration struct."
---

This page is generated from the CLI source by `triagelab docs`; run `make docs-generate` after changing commands, flags, or configuration. Do not edit it by hand.

One JSON object, decoded with unknown fields rejected. Omitted fields take the defaults below; every field is optional in the file but validated after loading.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `endpoint` | string | `http://127.0.0.1:8083/mcp` | KAgent MCP API endpoint. HTTPS off-loopback; plain HTTP only on loopback or, with allow_cluster_http, on cluster-internal service names. |
| `namespace` | string | kagent | Kubernetes namespace of the KAgent installation. |
| `agent` | string | triagelab-investigator | KAgent agent invoked as the investigator. |
| `template` | string | triagelab-python | Standalone sandbox template used for the run's sandbox. |
| `user_id` | string | triagelab@localhost | KAgent user identity that owns sessions and sandboxes. |
| `token_env` | string | TRIAGELAB_TOKEN | Environment variable holding the KAgent invocation token; the token itself never goes in the file. |
| `investigation_timeout` | integer | 600 | Overall investigation budget in seconds (at most 3000). |
| `process_timeout` | integer | 60 | Per-process watchdog in seconds; cannot exceed investigation_timeout. |
| `sandbox_ttl` | integer | 900 | Sandbox TTL in seconds, fixed at creation; must cover the investigation plus request and cleanup budgets (at most 3600). |
| `max_experiments` | integer | 8 | Client-side cap on recorded experiments (at most 32). A watchdog observed between polls, not admission control. |
| `poll_interval` | integer | 2 | Seconds between task-history polls (at most 10). |
| `request_timeout` | integer | 20 | Per HTTP request timeout in seconds (at most 60). |
| `max_output_bytes` | integer | 1048576 | Cap on captured process output per read (at most 4 MiB). |
| `manual_agents` | object | (none) | Optional map of agent name to extra MCP tool names for --agent runs; an empty tool list means the six investigation tools only. |
| `allow_cluster_http` | boolean | false | Permit plain-HTTP endpoints on *.svc or *.svc.cluster.local service names, for a dedicated in-cluster deployment whose pod network is the trust boundary. |

Save it as `triagelab.json` and pass `--config triagelab.json`; the CLI and the workspace accept the same file. The committed [configuration examples](/TriageLab/reference/configuration-examples/) include this file plus the optional sign-in, ticket source, and telemetry configurations.
