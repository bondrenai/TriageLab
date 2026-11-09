# Architecture and decisions

## Boundaries

`cmd/triagelab` is a testable standard-library CLI. `internal/casefile` contains a
read-only `Connector` interface and local ingestion. It rejects dangerous file
inputs before transferring bytes. `internal/kagent` is the only version-dependent
transport layer: stateless MCP JSON-RPC, bounded HTTP, durable A2A event decoding,
and the upstream task-cancellation extension. It never executes host commands.
`internal/investigation` coordinates one sandbox and one investigator Session.
`internal/report` persists private reports atomically. `internal/agent` embeds the
trusted prompt, output schema, and declarative resources.

`internal/webserver` adds a loopback-only HTTP interface to that same workflow.
It admits one active run, ingests bounded multipart uploads, and reads persisted
reports for history and downloads. Closing a browser does not cancel a run;
the explicit cancel action or server shutdown cancels the investigation and
waits for its cleanup. `web/` is a React/TypeScript client with API transport,
domain types, query hooks, and presentation kept separate. There is no second
agent implementation or browser-held model credential.

The optional ticket procedure is implemented in `internal/ticketflow`. It
stores one private record per enrolled Zendesk ticket under the report
directory, with a process lock and atomic updates. It selects cases using
configured ticket properties and extracts readiness facts from public
requester comments. A durable clarification reservation precedes a policy
authorized Zendesk write. Waiting cases consume no sandbox. An operator
approves the plan's evidence and limits before the same workspace run slot
dispatches the existing investigation workflow. See [ticket setup](tickets.md).

KAgent owns its native Agent/AgentTemplate/Harness, SandboxTemplate, runtime
Session/Sandbox API, Go ADK tool loop, authorization integration, model gateway,
and expiry. Sandbox is a database-backed API resource, **not** a Kubernetes CRD.
Substrate owns WorkerPools, Actors, gVisor/optional microVM isolation, snapshots,
resource enforcement and the default-deny egress gateway.

## Why Go

Go provides a small deployable binary and fits the Kubernetes ecosystem. The
project uses `cmd/` + `internal/`, explicit startup configuration, context
cancellation and Make-based checks. TriageLab uses `net/http` and
`encoding/json` directly for the small pinned MCP surface; this avoids importing
the entire upstream control plane or an SDK version with a different MCP wire
contract. Future SDK adoption should preserve the tested upstream contract.

## Evidence and lifecycle

1. Allocate a case ID and write a private initial report.
2. Create a standalone sandbox using that stable idempotency key. Repeat only
   the same lifecycle operation when pending/uncertain; never retry process starts.
3. Upload bounded files through `write_sandbox_file` and verify byte counts.
4. Create an empty investigator Session and persist its ID before invoking it.
5. Give the investigator the sandbox ID, limits, and an explicitly untrusted case.
6. Poll durable task history. Accept canonical runtime tool parts, never ticket
   prose or user-role parts as execution evidence. Pair call/result IDs; directly
   fetch each process's state, exit code, and paginated output.
7. Validate the agent's structured answer and all evidence references. A failed
   infrastructure call is inconclusive. Missing/truncated evidence cannot support
   a reproduced claim. The relationship between an experiment and the reported
   symptom remains model-assessed and visible for human review.
8. Cancel active or parked tasks, delete Session and Sandbox, retry pending
   lifecycle deletion, and verify deleted state. Write final reports even on errors.

Process deadlines and experiment counts are client watchdog limits, observed
between API calls. An agent can exceed the count before detection; this is not
admission control. Per-request and overall deadlines, runtime resource limits,
and server TTLs complement those watchdogs. SIGKILL and network partitions may
leave a sandbox until its TTL. Session cleanup can require manual recovery.

## Extension points

A connector supplies normalized case data and source references. GitHub Issue
read-only ingestion is the first planned external adapter. Additional reproduction
images belong in upstream SandboxTemplates; use the manifest image override with
an OCI digest. Egress exceptions belong in reviewed upstream EgressPolicy
configuration, not model output. Specialized investigators can use additional
KAgent AgentTemplates when concrete cases justify them.

There is no generic plugin framework, multiagent topology, database, job queue,
custom Kubernetes operator, or production mutation tool in the initial CLI.
