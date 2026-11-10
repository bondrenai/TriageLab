---
title: "CLI"
description: "Every triagelab command, flag, and exit code, generated from the CLI source."
---

This page is generated from the CLI source by `triagelab docs`; run `make docs-generate` after changing commands, flags, or configuration. Do not edit it by hand.

## investigate

Run one investigation: collect the bounded case, create a standalone sandbox, run the kagent investigator, and write JSON and Markdown reports.

```text
triagelab investigate [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --agent | (none) | agent name configured under manual_agents |
| --config | (none) | JSON configuration (default: built-in localhost settings) |
| --description | (none) | local issue description |
| --description-file | (none) | UTF-8 issue description file |
| --file | (repeatable) | attachment (repeatable) |
| --log | (repeatable) | log file (repeatable) |
| --output | reports | parent directory for private reports |
| --repo | (none) | small local repository or fixture directory |
| --telemetry | (none) | optional OTLP telemetry configuration exporting logs, metrics, and traces |

## serve

Serve the authenticated browser workspace on loopback, sharing the CLI's investigation workflow and report directory.

```text
triagelab serve [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --allow-resource-writes | false | allow the workspace to save kagent definitions in the configured namespace |
| --assets | web/dist | built frontend directory |
| --auth | (none) | optional sign-in configuration JSON with an oidc section |
| --config | (none) | JSON configuration (default: built-in localhost settings) |
| --listen | 127.0.0.1:8080 | loopback address for the private web UI |
| --output | reports | parent directory for private reports |
| --status-context | (none) | Kubernetes context for live deployment status |
| --status-kubeconfig | (none) | explicit Kubernetes config for live deployment status and optional saves |
| --telemetry | (none) | optional OTLP telemetry configuration exporting logs, metrics, and traces |
| --tickets | (none) | optional Zendesk, GitHub, or Linear case configuration |
| --users | (none) | account file for web sign-in (default <output>/.auth/users.json) |

## doctor

Check that the kagent sandbox API is reachable with the configured credentials. A live investigation is still needed to verify runtime and model access.

```text
triagelab doctor [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --config | (none) | JSON configuration (default: built-in localhost settings) |
| --telemetry | (none) | optional OTLP telemetry configuration exporting logs, metrics, and traces |

## cleanup

Finish sandbox and session cleanup from a saved report; the configuration must match the report's endpoint, user, and namespace.

```text
triagelab cleanup [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --config | (none) | JSON configuration (default: built-in localhost settings) |
| --report | (none) | saved report.json for cleanup |
| --telemetry | (none) | optional OTLP telemetry configuration exporting logs, metrics, and traces |

## manifest

Render the kagent workspace resources (investigator agent, sandbox template, tools service) as Kubernetes JSON.

```text
triagelab manifest [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --workload-image | (none) | digest-pinned replacement tools image |

## agent-manifest

Render a bundled agent definition — agent plus optional model and MCP server resources — as Kubernetes JSON for review and deployment.

```text
triagelab agent-manifest [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --definition | (none) | local agent definition JSON file |
| --namespace | kagent | kagent namespace |

## user

Manage local sign-in accounts without contacting kagent. The first account defaults to admin; later accounts default to viewer.

```text
triagelab user <add|list|remove|set-role|set-password> [options]
```

| Flag | Default | Description |
| --- | --- | --- |
| --output | reports | parent directory for the default account file |
| --password | (none) | initial or replacement password (prefer --password-stdin) |
| --password-stdin | false | read the password from standard input |
| --role | (none) | viewer, user, or admin |
| --username | (none) | account name |
| --users | (none) | account file (default <output>/.auth/users.json) |

## version

Print the TriageLab and pinned upstream versions.

```text
triagelab version
```

## Exit codes

A nonzero exit never means the report should be discarded.

| Code | Meaning |
| --- | --- |
| 0 | Command succeeded; an investigation reproduced the reported symptom |
| 1 | Investigation completed; the symptom was not reproduced |
| 2 | Invalid input, configuration, or usage |
| 3 | Investigation finished inconclusive |
| 4 | Infrastructure, agent, or protocol failure |
| 5 | Cleanup incomplete or sandbox creation uncertain; see the report |
| 124 | Investigation exceeded its time limit |
| 130 | Interrupted (Ctrl-C or SIGTERM) |
