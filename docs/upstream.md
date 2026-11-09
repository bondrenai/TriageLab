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
| TriageLab Go toolchain | `go1.25.12` | Development VM installation verified against the official release SHA-256 |

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
