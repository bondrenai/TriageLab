# Upstream compatibility record

Research date: 2026-11-08. These are **source-reviewed integration pins**, not a
claim that every backend was exercised. The dedicated Linux development VM
completed the real gVisor sandbox checks recorded in
[sandbox-smoke.json](verification/sandbox-smoke.json): nested upload, fixture
failure, corrected run, DNS/HTTP egress denial, process termination, and deletion.
That record explicitly marks `agent_invoked: false`; it is a sandbox integration
check, not the agent demonstration. The separate [live investigator report](verification/agent-demo.md)
and [verification record](verification.md) document the completed real agent demonstration. Do not use a live `main` branch as a dependency.

| Component | Pin | Identity |
| --- | --- | --- |
| KAgent CLI/charts/controller/runtime | `v1.0.0-rc3` (chart `1.0.0-rc3`) | `3548b7aaf4d91ec86b0565df2bd8a9950e9a276d` |
| KAgent's Substrate Helm distribution | `v1.0.0` (chart `1.0.0`) | `kagent-dev/substrate`, `563df06f6be751c101539986896c17c55aa22a9d` |
| Agent Substrate core reviewed | `v1.0.0` | `agent-substrate/substrate`, `b34fbee742906494b5eb4f1f36987176bf8770a4` |
| Sandbox guest image | `sandbox-guest:1.0.0-rc3` | `sha256:8521d7e41097fb2964570ce1e65115056098d1c0c17c5dab1c2a4f30a7e06939` |
| Fixture tools image | `python:3.12-slim-bookworm` | `sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df` |
| Development Kubernetes | `kindest/node:v1.35.0` | KAgent rc3's development configuration |
| TriageLab Go toolchain | `go1.27.3` | Development VM installation verified against the official release SHA-256 |
| OpenTelemetry Go SDK | `v1.47.0` line | `otel`/`sdk`/`sdk/metric` `v1.47.0`, `sdk/log` `v1.47.0`, OTLP/HTTP exporters `v1.47.0`/`v0.23.0`, `contrib/bridges/otelslog` `v0.21.0`, vendored in `vendor/` |

The two Substrate repositories are intentionally distinguished. KAgent's
[go.mod](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/go/go.mod)
requires `go.ate.dev/substrate v1.0.0` and replaces it with
`github.com/kagent-dev/substrate v1.0.0`. Its installer consumes that project's
Helm charts and worker images. The
[fork's release notes](https://github.com/kagent-dev/substrate/releases/tag/v1.0.0)
describe synchronization with Agent Substrate. Installing the core project's
source installer alongside KAgent's Helm installer would duplicate infrastructure;
TriageLab uses only the supported KAgent installation path.

## Interfaces used

1. Kubernetes configuration uses **`api.kagent.dev/v1alpha3`**: `Harness`,
   `AgentTemplate`, `Agent`, `RemoteMCPServer`, `SandboxTemplate`, and the
   TriageLab's `ModelConfig`. `Agent.spec.template` selects behavior and
   `harnessRef` selects its Go ADK runtime. This differs from KAgent 0.x examples.
2. A standalone **Sandbox is an owner-scoped service object**, not a Kubernetes
   Sandbox CRD. KAgent persists its lifecycle and maps it to a Substrate Actor.
   TriageLab calls the controller's existing `/mcp` tools, including sandbox
   creation, file writes, process operations, output reads, and deletion. It
   implements no sandbox operator or process execution service.
3. KAgent's core MCP session/invocation tools start the real investigator.
   The investigator's remote binding selects six existing process/file tools.
   It cannot create other sandboxes or invoke infrastructure administration tools
   through that binding. Built-in runtime bash and file search are disabled.
   Upstream still exposes basic read/write/edit tools for the investigator's own
   separate runtime filesystem; this release has no switch to remove them.
4. The investigator returns KAgent structured output (`outputSchema`) backed by
   its Go ADK harness. KAgent validates the terminal JSON; TriageLab additionally
   reconciles process references with observed upstream execution records.
5. Agent Substrate owns worker placement, gVisor/microVM isolation, network
   mediation, identity, snapshots, and actor teardown. `ate.dev/v1` `WorkerPool`
   is a Kubernetes resource. Native Actors and ActorTemplates live in the ate
   API; TriageLab does not submit either directly.

Authoritative references:

- [Standalone sandbox contract and MCP tools](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/docs/architecture/sandboxes.md)
- [Exact MCP sandbox implementations](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/go/core/internal/mcp/sandboxes.go)
- [KAgent structured output](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/docs/architecture/structured-output.md)
- [KAgent installation and version constraints](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/helm/README.md)
- [Substrate runtime classes](https://github.com/agent-substrate/substrate/blob/b34fbee742906494b5eb4f1f36987176bf8770a4/docs/reference/sandbox-classes.md)
- [Substrate security scope](https://github.com/agent-substrate/substrate/blob/b34fbee742906494b5eb4f1f36987176bf8770a4/docs/security/security.md)
- [Substrate egress behavior](https://github.com/agent-substrate/substrate/blob/b34fbee742906494b5eb4f1f36987176bf8770a4/docs/guides/egress-traffic.md)

## Constraints that affect TriageLab

- KAgent requires digest-pinned workload and guest images. The listed image
  digests were resolved from their registries, not invented from examples.
  Runtime resource limits are operator configuration, not agent-selectable
  template fields.
- Sandbox lifetime is fixed at creation; activity does not renew it. KAgent's
  expiration workers delete expired sandboxes. Ordinary pending lifecycle
  operations need retries; reads alone do not advance them. Creation has a
  stable request ID, but process start has **no idempotency receipt**. Never
  blindly replay a process start after an uncertain response.
- Process state and handles are guest-local. Suspension ends processes and
  restoring a volume snapshot does not preserve their handles. Collect evidence
  before deleting or suspending. MCP output pages and writes are capped at 1 MiB;
  output is base64 with continuation offsets. `exit_code` is meaningful only
  for `PROCESS_STATE_EXITED`.
- Substrate denies sandbox egress by default, including ungranted DNS queries.
  It supports policy-controlled HTTP(S); arbitrary UDP and plain TCP protocols
  are restricted. A generic network failure may require a purpose-built template
  or future upstream support. Policy grants are operator input, never taken from
  issue prose.
- gVisor enforces actor-wide CPU/memory; per-container limits are ignored in
  this backend. microVM uses Cloud Hypervisor/Kata with KVM and at least 128 MiB
  guest RAM. Substrate's trusted infrastructure needs node-level permissions;
  these are not permissions granted to the untrusted fixture.
- Substrate injects provider credentials outside the guest. Global runtime token
  propagation forwards the caller credential and identity to every MCP server
  in Go ADK rc3. Keep it disabled. The trusted sandbox server uses its own
  `tokenPassthrough: true` binding and KAgent re-verifies the Kubernetes
  service-account token to recover the caller identity.
- The installed `triagelab-model` uses OpenAI's `gpt-6.1-sol` with
  `spec.openAI.apiFormat: responses`. Reviewed 2026-11-09: OpenAI's GPT-6
  models require the [Responses API for tool calling](https://developers.openai.com/api/docs/models/gpt-6.1-sol),
  KAgent's default is `chatCompletions`, and `gpt-4.1` is
  [scheduled for shutdown](https://developers.openai.com/api/docs/deprecations)
  on 2027-01-14. Models defined in the workspace editor or a CLI bundle render
  no `apiFormat`, so they still use Chat Completions. KAgent rc3 and rc4 replay
  tool calls without the model's reasoning items, which OpenAI's
  [reasoning guidance](https://developers.openai.com/api/docs/guides/reasoning)
  asks clients to pass back; four live runs completed that way and one failed
  for an unrecorded reason.
- The `triagelab-tools` ClusterIP Service is an alias for the controller with a
  distinct HTTP origin. rc3's [actor egress policy](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/go/core/internal/substrate/policy.go#L47)
  always replaces `Authorization` at the control-plane origin with an actor JWT.
  Actor credentials cannot access owner-scoped sandboxes. The separate tools
  origin and `tokenPassthrough: true` preserve the verified invocation
  service-account credential. No authorizer or ownership
  checks are weakened. This alias is not an externally exposed service.
- KAgent's shipped authorizer is not a production tenant policy. See its
  [authentication documentation](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/docs/architecture/oidc-proxy-authentication.md).
  TriageLab's private development setup does not claim a multi-tenant perimeter.
  Prompt-injection instructions reduce risk but are not a security boundary.

## microVM backend

Source-reviewed 2026-11-09 at the pins above and exercised by the CI runs in
[verification.md](verification.md#microvm-backend-verification). The sandbox
class belongs to the WorkerPool. KAgent compiles a SandboxTemplate into an
ActorTemplate using the class of the pool it references, so TriageLab's
templates and tool calls do not change.

- KAgent's chart renders `substrateWorkerPool.sandboxClass: microvm` with the
  `ateworker-microvm` image and defaults the SandboxConfig reference to
  `microvm`. Its Helm README states that KAgent does not install the `microvm`
  SandboxConfig, the runtime assets, or KVM-capable workers. The Substrate
  chart ships only the gVisor SandboxConfig.
- Substrate's `ate-setup deploy sandboxconfig --sandbox-classes=microvm`
  supplies them. At `v1.0.0` it downloads Kata Containers 4.3.1 and Cloud
  Hypervisor v53.0, checks the per-architecture SHA-256 digests pinned in its
  SandboxConfig template, stages the four files under
  `microvm-assets/<arch>/<sha256>` in the kind RustFS bucket `ate-snapshots`
  using the chart's default RustFS credentials, and applies the SandboxConfig.
  It also reapplies the SandboxConfig admission policy from the same release.
  KAgent rc3's CI runs this step after `kagent install`;
  `scripts/install-microvm-assets.sh` does the same and refuses a tag that no
  longer resolves to the reviewed commit.
- Workers reach KVM through the `ate.dev/kvm` extended resource, which atelet's
  device plugin advertises only on nodes where `/dev/kvm` exists. A kind node
  needs the device bind-mounted (`deploy/kind-microvm.yaml`); otherwise the
  workers stay Pending.
- Upstream requires assets to stay outside any snapshot location's
  `/atespaces/` prefix. The `microvm-assets/` prefix satisfies that.

References:

- [KAgent microVM values and prerequisites](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/helm/README.md)
- [KAgent's kind microVM setup](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/scripts/kind/setup-microvm.sh)
- [Substrate local microVM guide](https://github.com/kagent-dev/substrate/blob/563df06f6be751c101539986896c17c55aa22a9d/docs/dev/microvm-local.md)
- [Pinned microVM SandboxConfig and asset digests](https://github.com/kagent-dev/substrate/blob/563df06f6be751c101539986896c17c55aa22a9d/manifests/ate-install/sandboxconfig-microvm.yaml.tmpl)

## Custom investigators

TriageLab renders native `Agent.spec.template.inline` and
`Agent.spec.harnessRef`. Declarative agents reuse the installed Go ADK Harness.
Custom images use `Harness.spec.byo` with a SHA-256-pinned image and explicit
command. The BYO compiler passes the prompt, optional model, and MCP settings
as ADK-shaped configuration. rc3 rejects native `outputSchema` on BYO Harnesses,
so the image must implement the report contract itself.

`ModelConfig` sets the invocation provider, model ID, optional endpoint, and
Secret references. `ModelProviderConfig` is a separate OpenAI-compatible model
discovery controller. Tool allowlists are native
`AgentTemplateSpec.tools[].mcp.tools` entries against `RemoteMCPServer` references.
An empty upstream list exposes the whole server, so TriageLab requires explicit
selections. KAgent compiles and deploys the configuration after the resource
is saved to Kubernetes. The workspace can write these resources when started with
an explicit kubeconfig, context, and write access. For BYO images it stages a new
Harness before changing the Agent's `harnessRef`.
The optional workspace status reader uses namespace-scoped Kubernetes list
permissions. It projects current-generation conditions and the controller's
`RemoteMCPServer.status.discoveredTools` for the editor.

- [Agent and Harness configuration](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/docs/architecture/configuration-and-compilation.md)
- [ModelConfig provider schema](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/go/api/v1alpha3/modelconfig_types.go)
- [MCP binding schema](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/go/api/v1alpha3/agenttemplate_types.go)
- [Credential injection and caller-token propagation](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/docs/architecture/credential-injection.md)

## Upgrade procedure

Review the upstream CRD schemas, protobuf configuration, MCP input/output
schemas, guest dependency, lifecycle retry rules, and credential propagation
before changing pins. Update CLI, chart, controller, guest and runtime images
together. Resolve and record new image digests. Render and validate manifests,
run unit/error-path tests, then run the real demo on a fresh disposable cluster.
Inspect failure and corrective outputs, network denial, timeouts and deletion;
record precisely which versions and checks ran. Preserve reports before deleting
the cluster. Do not infer compatibility from unchanged tool names.

## OpenTelemetry SDK integration

Added 2026-11-09. TriageLab uses the official OpenTelemetry Go SDK (the
module's first third-party dependency, vendored so builds stay offline and
reproducible) to export **traces, metrics, and logs over OTLP/HTTP** when a
telemetry configuration file is supplied (`--telemetry`, chart values under
`telemetry:`). Without configuration every provider stays a no-op and nothing
leaves the process.

Interfaces used:

- `sdk/trace` with `ParentBased(TraceIDRatioBased)` sampling, batch span
  processor, and `otlptracehttp`; `sdk/metric` with a periodic reader and
  `otlpmetrichttp`; `sdk/log` with a batch processor and `otlploghttp`; the
  `otelslog` bridge for structured logs; W3C `traceparent` injection into
  KAgent MCP requests.
- `otelhttp` transports are **not** used; outbound propagation is a direct
  propagator call and server spans are hand-wrapped so the loopback and
  origin checks stay ahead of instrumentation.
- `WithEndpointURL` maps the configured URL path verbatim, so TriageLab
  appends `/v1/{traces,metrics,logs}` itself; a base path prefix is honored.

Constraints that affect TriageLab:

- Export is best effort. Collector failures surface on stderr and never
  change investigation outcomes; the shutdown flush is bounded to five
  seconds.
- Attributes are limited to fixed vocabularies (statuses, outcomes, trigger,
  MCP method/tool names, error kinds, collapsed route patterns) and
  identifiers (run IDs). Case descriptions, attachments, model prompts, tool
  outputs, credentials, and account names are never instrumented; phase log
  messages pass through the report redactor first.
- The pinned line requires Go >= 1.26.0; the toolchain pin is `go1.27.3`.
  The logs SDK stable (`sdk/log v1.47.0`) line exists only from v1.47 onward.
- Verified by unit tests against an in-process OTLP/HTTP collector decoding
  the exported protobuf for all three signals (see
  `internal/telemetry/telemetry_test.go`). Live export to a real collector is
  exercised only to the extent recorded in `docs/verification.md`.

## GitHub issue integration

Reviewed 2026-11-09 against GitHub's REST API version **2026-03-10**, sent in
`X-GitHub-Api-Version`. The connector reads one issue and at most 100 comments
from a configured GitHub.com repository, rejects pull requests, and rechecks
issue and comment contents before admitting an approved run. Public issue text
is available anonymously; private issue text needs **Issues: read** for the
selected repository. Reviewed clarifications, authorized follow-ups, and
reviewed investigation summaries use `POST /repos/{owner}/{repo}/issues/{issue_number}/comments`
with **Issues: write** permission. Comment creation can notify repository
participants; GitHub provides no private issue-comment visibility option.

- [API version contract](https://docs.github.com/en/rest/about-the-rest-api/api-versions)
- [Get an issue](https://docs.github.com/en/rest/issues/issues?apiVersion=2026-03-10#get-an-issue)
- [List issue comments](https://docs.github.com/en/rest/issues/comments?apiVersion=2026-03-10#list-issue-comments)
- [Create an issue comment](https://docs.github.com/en/rest/issues/comments?apiVersion=2026-03-10#create-an-issue-comment)
- [Conditional request limitations](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests)
- [Attachment access rules](https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/attaching-files)

GitHub does not document conditional writes for comment creation. A final
freshness read checks the reviewed snapshot immediately before posting, but
concurrent changes can still occur before GitHub accepts the comment. The flow
persists a delivery reservation first and accepts only a `201` receipt with a
positive exact comment ID, the submitted body, and matching issue/comment URLs.
API redirects and automatic write retries are disabled. An uncertain response
remains held across restarts for manual inspection. The POST body is capped
at 16 KiB; provider responses are bounded. Investigation summaries use literal
Markdown code blocks so imported content cannot create mentions or links.

Only public text uploads linked with standard Markdown at
`https://github.com/user-attachments/files/<id>/<filename>` are supported.
A public attachment transport check observed a single HTTPS redirect to
`objects.githubusercontent.com/github-production-repository-file-5c1aeb/<repository-id>/<file-id>`.
The connector accepts that exact host and path shape with the same file ID and
no further redirects. Neither attachment request receives the API token.
This observed download shape is not a versioned REST contract; a changed host
or path fails closed and requires review. Private attachment downloads,
Enterprise hosts, and repository cloning are unsupported. HTTP fixtures and
the transport check do not prove a live GitHub-to-KAgent run. No live GitHub
clarification or investigation-summary sends were performed during implementation.
