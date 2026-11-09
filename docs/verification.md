# Executed verification

Verified on November 8, 2026 (America/New_York) on a dedicated Linux development
VM: 4 vCPU, 16 GiB RAM, Ubuntu 24.04, kind Kubernetes 1.35.0, Go 1.25.12,
KAgent v1.0.0-rc3, its matching Substrate v1.0.0 distribution, and gVisor.
The investigator used OpenAI `gpt-4.1`. No agent output or sandbox was mocked in
these live checks.

## Passed

- `make check`: formatting validation, `go vet ./...`, and all unit/HTTP protocol
  tests. The live test is intentionally skipped without its opt-in variable.
- `make build`: the Go CLI compiled on the VM.
- Generated resources passed Kubernetes server-side dry-run and applied
  successfully. Agent and SandboxTemplate reached Ready.
- `make test-integration` with the real deployment, caller token and model:
  **PASS**, 22.6 seconds. It verified ingestion, actual KAgent invocation,
  independently collected sandbox evidence, supported diagnosis, both failure
  and correction, report generation, and deletion of both runtimes.
- Separate real sandbox checks verified nested uploads, DNS denial, direct-IP
  HTTP denial (403), process termination (exit 137), and sandbox deletion.
- The CLI itself completed a real investigation and returned exit 0 with a
  reproduction report. Earlier failed runs retained reports and confirmed
  cleanup rather than reporting invented success.

The full live integration produced these actual process results:

| Command | Exit | Observation |
| --- | --- | --- |
| `python3 app.py` | 1 | 5 ms deadline expires before the simulated 120 ms response |
| `python3 app.py --timeout-ms 5000` | 0 | Corrected timeout succeeds |
| `python3 app.py --config config.json` | 1 | Original configuration still reproduces the failure |
| `python3 app.py --timeout-ms 5000 --config config.json` | 0 | Explicit override verifies the correction again |

The fixture simulates downstream latency without making an HTTP request or
sleeping. It is a real Python configuration failure executed in a real isolated
sandbox; its printed HTTP status is application output, not a claim of external
network success.

Evidence:

- [Human-readable investigation](verification/agent-demo.md)
- [Machine-readable investigation](verification/agent-demo.json)
- [Independent sandbox lifecycle/security check](verification/sandbox-smoke.json)

The agent report contains actual process IDs, commands, decoded stdout/stderr,
input checksums, hypotheses, tool-call records, and successful cleanup. The
sandbox smoke record correctly states `agent_invoked: false`; it is not used as
proof of agent execution.

## Limits of this verification

Model behavior is not deterministic. During integration, the smaller
`gpt-4.1-mini` sometimes selected a prohibited investigator-local file tool or
stopped before verification. Such runs were stopped or reported with their
available evidence and cleaned up. A model output that invented a process ID
was downgraded to inconclusive by the evidence validator. The final tested
configuration uses `gpt-4.1` and an explicit investigation request; this is not
a guarantee for arbitrary future cases.

Unit tests exercise transient/uncertain API outcomes, idempotent recovery,
parked/active task cancellation, timeouts, missing output, truncation, private
report writes, redaction, input rejection, and protocol errors using test
fixtures. These are not live controller outage or hostile escape tests.

Not executed: microVM backend, multi-user deployment, production authorization,
controller/VM crash during a run, or broad hostile
workload security testing. Exact binary artifact export and external issue
connectors remain roadmap items. The development VM is ephemeral; saved evidence
is retained in the repository, not dependent on its continued availability.

The microVM configuration and opt-in CI job added on 2026-11-09 were checked
only statically: shell and workflow linting, YAML parsing, and a `helm template`
of the pinned KAgent chart showing the `microvm` class, SandboxConfig reference,
and worker image on `kagent-default`. No KVM host ran them, so no microVM
sandbox, asset staging, or investigation has been executed.

The default investigator model changed on the same date from `gpt-4.1` to
`gpt-6.1-sol` over OpenAI's Responses API. Every live run recorded above used
`gpt-4.1` over Chat Completions, so the new default has no live verification
yet.

After verification, the dedicated VM, its boot disk, and its IAP SSH firewall
rule were deleted. Cloud inventory checks found none of those resources remaining.
Shared infrastructure used by other work was retained.

## Browser workspace verification

The initial UI was checked separately on a smaller 2 vCPU / 8 GiB Ubuntu VM,
using Node 24.18.1 and Go 1.25.12. Builds, browser execution, and tests ran on
that VM, not on the resource-constrained development laptop.

- TypeScript checking and the Vite production build passed.
- `go test -race ./internal/webserver ./cmd/triagelab`, focused `go vet`, and
  `make build` passed. The HTTP tests cover origin/host checks, upload limits,
  unsafe paths, single-run admission, cancellation, shutdown, persisted history,
  download redaction, and safe report reads.
- Chromium exercised empty history, the saved real example, experiment
  disclosures, required description validation, file add/remove, report
  downloads, and navigation to a newly submitted run.
- With KAgent absent, an actual UI submission saved an infrastructure-error
  report and appeared in history. This checks error handling, not model access.
- A browser response fixture verified that a partial report with missing process
  state/output remains readable. No agent execution is claimed for this fixture.
- Desktop (1440px) and mobile (390px) report/intake views had no detected
  WCAG A/AA violations in the focused axe checks, no horizontal page overflow,
  and no browser runtime errors. Automated checks are not a complete accessibility
  audit.
- The Vite development proxy served settings, accepted same-origin intake
  validation, and rejected a foreign origin.

No new live model investigation was run for the UI work. The bundled example
is the preserved live run documented above; current new runs still require a
configured private KAgent/Substrate deployment and model credential.

## Zendesk ticket procedure verification

On the remote development VM, `make check`, `make build`, and `make build-ui`
passed. The ticket tests use a local TLS HTTP fixture with Zendesk response
shapes. They cover read-only preview, policy-authorized clarification,
recovery across a restart, unconfirmed writes, requester replies, changed
attachments, stale approval, and a busy run slot. A Chromium walkthrough
using fixture API responses checked the queue, approval, run link, and
desktop/mobile accessibility. These checks did not call a Zendesk account,
send customer messages, or start a live KAgent investigation.

## Zendesk OAuth verification

The remote VM passed `make check`, `make build`, and `make build-ui`.
The OAuth tests use a TLS fixture to verify PKCE, one-time callback state,
browser binding, exact scopes, private persistence, refresh rotation, and
recovery after a lost refresh response. Chromium verified the scoped
authorization redirect and a simulated cross-site denial callback, including
desktop and mobile accessibility checks. No live Zendesk grant was issued.

## GitHub issue connector verification

On 2026-11-09, the existing remote build VM passed `make check`, `make build`,
`make check-ui`, and `make build-ui` using Go 1.25.12, `GOFLAGS=-trimpath`, and
Node 24.18.1. Formatting also ran remotely with `make fmt` and was copied back
to the source workspace. The isolated build copy included the saved demo JSON
required by the UI. No build or test ran on the development laptop.

GitHub HTTP fixtures cover the pinned API headers, issue versus pull request
parsing, author identity, source links, comment limits and freshness, bounded
responses, anonymous reads, credential-free attachments, restricted redirects,
and errors that omit provider bodies. Workflow tests cover author-only evidence,
filename normalization, file hashes and source URLs, edited/deleted comments,
changed attachment bytes or locations, configured credentials in evidence and
references, approval revalidation, one-run persistence, and repository-scoped
state. Configuration and HTTP tests reject unconfigured GitHub writes and Zendesk OAuth
actions in a GitHub workspace. Existing Zendesk tests also pass.

Playwright 1.65.0 with Chromium 1254 exercised the built Go server and browser
assets on the VM. The real workspace API identified the configured GitHub
repository without exposing OAuth or credentials. Fixture preview and track
responses exercised issue, author, comment and attachment links, the approval
control, and manual clarification guidance. GitHub views exposed no Zendesk
connection or send controls. At 1440px and 390px widths there was no horizontal
overflow or browser runtime error. The test made no external request and did
not click run approval. Its server and temporary fixture state were removed.

These are fixture and build checks. No private GitHub account integration,
GitHub-to-KAgent investigation, model call, or sandbox run was performed for
this change. The public attachment transport observation is recorded separately
in [upstream compatibility](upstream.md#github-issue-integration).

## GitHub comment workflow verification

The writable GitHub workflow subsequently passed `make check`, `make build`,
`make check-ui`, and `make build-ui` on the same remote build VM with Go 1.25.12,
`GOFLAGS=-trimpath`, and Node 24.18.1. `make check` ran again after adding the
literal Markdown summary regression test. Formatting ran remotely and was
copied back to the local source workspace.

HTTP fixtures cover a single authenticated comment POST, final freshness
confirmation, matching `201` receipts, bounded requests and responses, disabled
private comments, missing write capability, refused redirects, uncertain
responses, and no transport replay. Workflow tests cover exact clarification
approval, authorized narrower follow-ups, no-progress and attempt limits,
changed policy or requester, own-account author comments, receipt persistence,
uncertain delivery across restarts, and completed-report summary approval.
Summary tests bind the destination, body, source, and repository audience;
reject mismatched report provenance; and verify imported Markdown remains
literal and configured credentials are redacted. Both GitHub comments and
Zendesk internal notes use the same `ticket_note` workspace response and
`ticket-note` action. Existing Zendesk tests pass.

Remote Playwright 1.65.0/Chromium 1254 exercised the built server and assets with
fixture write responses. It checked initial clarification review and the exact
revision submitted, waiting state, investigation comment review with repository
audience, the exact summary plan submitted, and a confirmed receipt. An
uncertain delivery removed posting controls and remained held after reload.
A Zendesk report fixture retained internal-note wording. At 1440px and 390px
there was no horizontal overflow or browser runtime error. All mutation
requests were intercepted; the browser made no external request or run
approval. Its temporary server and fixture state were removed.

No live GitHub write or GitHub-to-KAgent investigation was performed. HTTP and
workflow fixtures demonstrate the tested request and persistence behavior, not
provider permissions, real notification delivery, or an atomic GitHub
compare-and-send guarantee.


## Custom agent definitions

The remote development VM passed `make check`, `make build`, `make check-ui`,
and `make build-ui` with Go 1.25.12 and Node 24.18.1. Unit and HTTP fixtures
covered native manifest generation, input restrictions, manual agent routing,
ticket-default separation, and the watchdog's treatment of added MCP calls.
Both agent types passed offline structural validation against the pinned
KAgent rc3 CRD schemas. Kubernetes CEL rules and controller reconciliation
require a deployment.

Remote Chromium exercised the built workspace and real manifest preview API
at desktop and mobile widths. It rendered declarative and BYO definitions,
downloaded the generated JSON, rejected a reserved tool name, and submitted the
selected agent in an intercepted manual-run request. Live custom-agent startup,
model and MCP connectivity, and sandbox ownership with scoped token
passthrough remain unverified.

The separate resource editors passed the same Go and UI checks. Chromium
verified that each editor retains its draft when switching views, models and
servers export standalone resources, agents reference them by name, and
changing to a custom image adds a Harness. The checks used the temporary
workspace's manifest API and covered desktop and mobile widths.

## Input and output sanitizers

Verified on November 9, 2026 on the shared Linux build VM (Go 1.26.10, Node
toolchain from `make install-ui`). The ticket sanitizer changes passed
`make check` (gofmt, `go vet ./...`, and all unit and HTTP protocol tests) and
`make check-ui`. New fixture tests cover redaction provenance in
`report.Redacted`, outbound deny-list terms (other tracked requesters and
internal note lines), held clarification drafts, blocked note previews that
refuse to post, and sanitized fact text reaching the case description, stored
facts, and sandbox environment with `input_redactions` provenance. Refusal of
configured credentials in facts, files, filenames, and attachment URLs remains
covered by the existing evidence tests. The full ticketflow suite also fails
on this macOS laptop at HEAD regardless of these changes, because fixture
evidence directories sit under the symlinked `/var` temp prefix that
`casefile.Ingest` refuses; the VM remains the check environment. Live KAgent
integration, a real Zendesk or GitHub send, and the blocked-note browser UI
were not exercised end to end and stay open with the roadmap item.

## Linear issue connector verification

On 2026-11-09, the shared bondren-build VM passed `make check` (gofmt, `go
vet`, `go test` with `GOFLAGS=-trimpath`) and `make check-ui` (`tsc -b`) over
the combined workspace snapshot, which also carried in-flight sign-in and
telemetry work from a concurrent session. No build or test ran on the
development laptop except the symlink-free `TMPDIR` ticketflow run used to
confirm the local `/var` artifact.

Linear GraphQL fixtures cover the pinned endpoint and raw Authorization
header, the signed-URL request header, issue lookup scoped to the configured
team key and per-team number, identifier/UUID/state/timestamp validation,
label and creator identity, description-as-first-comment provenance, comment
anchors, the 100-comment bound, and freshness receipts that detect edited or
deleted comments. Attachment parsing accepts only Linear upload hosts with
Markdown link names; downloads reject other hosts and fragments, never send
the API key to the storage host, and enforce text and size bounds. Workflow
tests cover author-only evidence, workspace investigation comments with
durable receipts and restart persistence, the token account's own outgoing
comments never counting as creator replies, team-scoped state, and refusal of
configured credentials in facts, files, filenames, and attachment URLs.
Configuration and HTTP tests reject unconfigured Linear writes, Zendesk-only
settings, malformed keys, and Zendesk OAuth actions in a Linear workspace.
Existing Zendesk and GitHub tests also pass.

These are fixture and build checks. No Linear account was connected, and no
live Linear clarification, summary send, or Linear-to-KAgent investigation
was performed. The GraphQL document shapes were pinned against the published
Linear API documentation; a controlled live pilot remains to be run.
