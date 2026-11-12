---
title: Upstream compatibility
description: Source-reviewed pins for kagent, Agent Substrate, images, and the OpenTelemetry SDK.
---

Research date: 2026-11-10. These are **source-reviewed integration pins**, not
a claim that every backend was exercised. The dedicated Linux development VM
completed the real gVisor sandbox checks — nested upload, fixture failure,
corrected run, DNS/HTTP egress denial, process termination, and deletion —
and a separate live investigator demonstration is recorded in the
[verification record](https://github.com/bondrenai/TriageLab/blob/main/docs/verification.md).
The sandbox checks ran on rc3; the live investigator demonstration and the
retention and pause/resume checks were repeated on the v1.0.0 pins.
Do not use a live `main` branch as a dependency.

| Component | Pin | Identity |
| --- | --- | --- |
| kagent CLI/charts/runtime | `v1.0.0` (chart `1.0.0`) | `a08cde76ad6482dd758a4220b14213d79185ac30` |
| kagent controller | bondrenai retention patch on v1.0.0 | `048e1016d50b209dba01065d0d10dfd80b78fb41` |
| kagent's Substrate Helm distribution | `v1.0.0` (chart `1.0.0`) | `kagent-dev/substrate`, `563df06f6be751c101539986896c17c55aa22a9d` |
| Agent Substrate core reviewed | `v1.0.0` | `agent-substrate/substrate`, `b34fbee742906494b5eb4f1f36987176bf8770a4` |
| Sandbox guest image | `sandbox-guest:1.0.0` | `sha256:354825706868fc4d39300fe7795f4fa04d1eb53c3c93fb5b65b5da76f8222db7` |
| Fixture tools image | `python:3.12-slim-bookworm` | `sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df` |
| Development Kubernetes | `kindest/node:v1.35.0` | kagent 1.0.0's development configuration |
| TriageLab Go toolchain | `go1.27.4` | latest security release; `net/http` GO-2026-6859 fixed |
| OpenTelemetry Go SDK | `v1.47.0` line | `sdk`, `sdk/metric`, `sdk/log`, OTLP/HTTP exporters, `otelslog` bridge, vendored |

The two Substrate repositories are intentionally distinguished. kagent
requires `go.ate.dev/substrate v1.0.0` and replaces it with
`github.com/kagent-dev/substrate v1.0.0`, whose installer consumes that
project's Helm charts and worker images. Installing the core project's source
installer alongside kagent's Helm installer would duplicate infrastructure;
TriageLab uses only the supported kagent installation path.

## Interfaces used

1. Kubernetes configuration uses **`api.kagent.dev/v1alpha3`**: `Harness`,
   `AgentTemplate`, `Agent`, `RemoteMCPServer`, `SandboxTemplate`, and
   TriageLab's `ModelConfig`. `Agent.spec.template` selects behavior and
   `harnessRef` selects its Go ADK runtime. This differs from kagent 0.x
   examples.
2. A standalone **Sandbox is an owner-scoped service object**, not a
   Kubernetes Sandbox CRD. kagent persists its lifecycle and maps it to a
   Substrate Actor. TriageLab calls the controller's existing `/mcp` tools
   and implements no sandbox operator or process execution service.
3. kagent's core MCP session/invocation tools start the real investigator.
   The investigator's remote binding selects six existing process/file tools;
   it cannot create other sandboxes or invoke infrastructure administration
   tools through that binding. Built-in runtime bash and file search are
   disabled.
4. The investigator returns kagent structured output (`outputSchema`) backed
   by its Go ADK harness. kagent validates the terminal JSON; TriageLab
   additionally reconciles process references with observed upstream
   execution records.
5. Agent Substrate owns worker placement, gVisor/microVM isolation, network
   mediation, identity, snapshots, and actor teardown. `ate.dev/v1`
   `WorkerPool` is a Kubernetes resource; TriageLab does not submit native
   Actors or ActorTemplates directly.

## Constraints that affect TriageLab

- kagent requires digest-pinned workload and guest images. Ordinary sandbox
  lifetime is fixed at creation. TriageLab opts into the fork's suspended
  retention: `SUSPENDED/NONE` has no expiry, and the first resume admission
  starts one fresh bounded active TTL. Activity and duplicate resumes do not
  renew it. The dedicated deployment sets `config.sessions.idleTtl: 0s` so a
  blocked investigation's conversation survives too. Creation has a stable
  request ID, but process start has **no idempotency receipt** — never
  blindly replay a process start after an uncertain response.
- Process state and handles are guest-local. Collect evidence before deleting
  or suspending. MCP output pages and writes are capped at 1 MiB; `exit_code`
  is meaningful only for `PROCESS_STATE_EXITED`.
- Substrate denies sandbox egress by default, including ungranted DNS
  queries, and supports policy-controlled HTTP(S). Policy grants are operator
  input, never taken from issue prose.
- gVisor enforces actor-wide CPU/memory; per-container limits are ignored in
  this backend. microVM uses Cloud Hypervisor/Kata with KVM.
- Global runtime token propagation would forward the caller credential to
  every MCP server; keep `config.runtimeDefaults.tokenPropagation.enabled`
  disabled. The trusted sandbox server uses its own `tokenPassthrough: true`
  binding with kagent re-verifying the service-account token.
- kagent's shipped authorizer is not a production tenant policy. TriageLab's
  private development setup does not claim a multi-tenant perimeter.
  Prompt-injection instructions reduce risk but are not a security boundary.

## Upgrade procedure

Review the upstream CRD schemas, protobuf configuration, MCP input/output
schemas, guest dependency, lifecycle retry rules, and credential propagation
before changing pins. Update CLI, chart, controller, guest, and runtime
images together; resolve and record new image digests. Render and validate
manifests, run unit and error-path tests, then run the real demo on a fresh
disposable cluster. Record precisely which versions and checks ran. Do not
infer compatibility from unchanged tool names.
