# Executed verification

## Model-backed GitOps investigation — November 12, 2026

Run `341019f5-3ace-498d-80a4-e022a71d9662` completed with outcome
`reproduced` on `bondren-build`, using `gpt-6.1-sol`, the pinned kagent
1.0.0 controller fork, and a real Substrate gVisor sandbox. All installation,
builds, checks, GitLab hosting, and investigation execution ran on the VM in
the isolated `/work/triagelab-gitops-model-20261112` snapshot.

A disposable self-managed GitLab CE 19.5.5 instance held a private synthetic
issue with three required facts and real `app.py` and `config.json` uploads.
The real workspace reconciled `TicketConnection` and `TicketProcedure` CRs
over the Kubernetes HTTPS API using the chart's namespace Role and an
allowlisted Secret. Agents and sandbox templates remained kagent resources.

- Generation 1 required approval and launched nothing. Updating both CRs to
  permit automatic runs reconciled generation 2 and changed the preview.
  Tracking the ready case launched exactly one investigation through the
  existing ticket policy path. The report records `started_by: ticket policy`
  and `unattended: true`.
- Attachment SHA-256 values matched the fixture bytes uploaded to GitLab.
  Independently collected sandbox evidence records the unchanged fixture
  exiting 1 with `TimeoutError: deadline 5ms elapsed before response at 120ms`,
  then `python3 app.py --timeout-ms 5000` exiting 0 with `HTTP 200: healthy`.
  The model cites both process IDs. This reproduces the deterministic
  simulator's diagnostic; it does not measure real network timeout behavior.
- The report contains no errors or warnings and records Sandbox and Session
  deletion. Separate authenticated controller reads confirmed the exact
  Sandbox in `RUNTIME_STATE_DELETED` and the exact Session absent.
- A full workspace process restart reconciled the same connection and
  retained the case's original run ID. Refresh created no duplicate run.
  Investigation comments were disabled; the issue retained only the test's
  initial evidence-upload note.

Saved evidence: [canonical report](verification/gitops-model-20261112/report.json),
[readable report](verification/gitops-model-20261112/report.md),
[verification metadata and hashes](verification/gitops-model-20261112/verification.json),
and [independent cleanup responses](verification/gitops-model-20261112/upstream-cleanup.json).
The same directory retains before/after CRs and case state. Temporary harness
assertions were corrected for the API's `approval` state and the pinned MCP
metadata/response-size requirements; the single completed model run was reused
for the independent checks. No product change was required by this live run.

`make build` passed. A follow-up in
`/work/triagelab-run-namespaces-docs-20261112` documented `run_namespaces`
and regenerated the reference pages, resolving the two generated-reference
test failures from the live-test snapshot. `make check` then passed in full
on the build VM with Go 1.27.3: formatting, vet, and all Go tests.
Argo CD/Flux synchronization and pod-projected API-token rotation were not
exercised: manifests were applied with kubectl and the workspace ran as a
loopback process on the VM. The disposable cluster and GitLab container were
removed after verification, including temporary credential copies; see the
[environment cleanup record](verification/gitops-model-20261112/environment-cleanup.json).

## Ticket GitOps CRDs — November 12, 2026

Implemented and checked in the isolated `/work/triagelab-gitops-20261112`
checkout on `bondren-build`, with Go 1.27.3 and Node 24.18.1. All builds and
tests ran remotely. `make check` passed (formatting, vet, and all Go tests),
as did generated-reference validation, targeted race tests for the GitOps
client/reconciler, `make chart-lint` with Helm 3.18.0, `make check-ui`,
`make build-ui`, and `make build-docs` including internal link validation.

A temporary kind 0.33.0 cluster using `kindest/node:v1.35.0` accepted both
structural CRDs through server-side apply. A separate loopback workspace
process used the chart's namespace Role and service account to reconcile
synthetic `TicketConnection`, `TicketProcedure`, and Secret resources over
the real, CA-verified Kubernetes HTTPS API. The smoke check verified:

- The Role allowed resource lists and an explicitly named Secret read, while
  denying Secret enumeration, other Secret reads, and connection-spec writes.
- Kubernetes rejected a change to the connection's immutable repository.
- The workspace applied the resources, wrote generation-aware status, exposed
  the GitOps ownership marker, and rejected authenticated UI configuration edits.
- A referenced procedure edit took effect; an invalid rotated Secret stopped
  the flow and replacing it recovered the connection. Deleting and restoring a
  referenced procedure also stopped and recovered it.
- Deleting the connection removed its live entry without deleting private
  state. Recreation and a full workspace process restart restored the same
  connection ID and preserved the state marker. Source credentials were absent
  from persisted workspace files and connection API responses.

These checks exercised real Kubernetes and the real workspace configuration
path. They did not contact a ticket provider, send a message, launch a sandbox,
or invoke a model. The cluster was dedicated to this check and removed afterward.
No existing workspace deployment was upgraded. The real-source/model follow-up
is recorded above; Argo CD/Flux rollout behavior and pod-projected token rotation
remain unverified. Unit tests cover API-token file rotation, failed or partial lists,
Secret allowlisting, reference ordering/scope, source identity conflicts,
retained state, and error recovery.

## Self-managed GitLab connector end to end — November 12, 2026

Ran the GitLab connector against a disposable self-hosted GitLab Community
Edition 19.5.5 container (Docker, loopback HTTP origin, root PAT with `api`
scope, scratch group deleted afterwards) using the opt-in live checks in
`internal/ticketflow/gitlab_live_test.go`
(`TRIAGELAB_GITLAB_LIVE_ORIGIN`, `_TOKEN`, `_GROUP`). No sandbox was
launched and no model was called.

`TestLiveGitLabConnectorAndFlow` passed: connection verification, issue-URL
reference resolution, a ready issue reaching approval with three facts and
the `app.py` Markdown upload (created through the live project uploads API)
downloaded and hashed through real evidence preparation, attachment
provenance in the approved input, the label-change system note excluded
from conversation, and a fenced investigation comment posted to the issue
with its receipt re-verified against a fresh read. The run found and fixed
a real defect: the connection probe bounded the project response at 4 KiB,
which a live project payload exceeds, so `VerifySource` failed against any
real instance (fixture responses are small and hid it); it now uses the
standard provider bound.

`TestLiveGitLabClarificationFollowup` passed: an evidence-less issue
drafted a clarification, **Approve and send clarification** delivered the
note, refresh waited, a reply posted directly in GitLab by the same
account (exercising the note-anchor rule that keeps our own receipts from
counting as requester evidence) permitted exactly one bounded follow-up.

Not exercised in these connector checks: GitLab.com (only the loopback
self-managed origin), model-backed investigation (subsequently verified through
GitOps above), internal notes (Premium; the connector never sends them),
confidential-issue requester visibility, and the workspace UI connection
flow for GitLab.

## UI-authored automatic Jira policy — November 11, 2026

Using the signed-in running workspace UI, configured the existing
`triagelab_python_timeout` procedure with match condition
`ticket.source == "jira" && "triagelab_python_timeout" in ticket.tags`, enabled
automatic starts, and used the condition builder to require the timeout tag for
launch. Existing Python-version, expected/actual behavior, and `.py` attachment
requirements remained intact. Both expressions passed the real server validator.

Draft and saved-policy previews of live Jira ticket `ASHE2E-4` showed evidence
ready. **Track and start when ready** launched run
`b6f84360-2e5a-456c-bdde-4e56011cbc29` without a separate run approval. The UI
showed **Started by policy**, and the canonical report records
`started_by: ticket policy`, `unattended: true`, and the exact launch condition.

Round one independently recorded `python3 app.py`, exit 0, stdout `timeout`.
The investigator paused because the 17-byte synthetic fixture has no timeout
logic. Supplied that limitation through the UI's resume form; round two completed
with an inconclusive outcome. It recorded no new process evidence, and unsupported
model citations/claims were downgraded by report validation. This verifies the
policy-to-investigation flow, not reproduction of the stated timing defect.

The report confirms Sandbox and Session deletion without cleanup errors.
Independent authenticated controller reads confirmed `RUNTIME_STATE_DELETED`
for the exact sandbox and `not_found` for the exact session. The policy remains
enabled as requested. Screenshots, saved policy, report hash, and controller
checks are under `triagelab-v1-live/chat-mcp/verification/cel-ui-live-20261111`
on `bondren-build`.


## Monaco workspace deployment — November 11, 2026

Updated the running `triagelab-workspace` service on `bondren-build` to the
previously tested `releases/cel-monaco-20261111` binary and UI. The gateway and
controller services remained on their existing configuration. All listeners
remain loopback-only. The prior release and launcher are retained for rollback.

The deployed workspace now uses the existing Jira test-site connection.
Automatic-start configuration is available to admins, while the saved default
procedure keeps automatic starts disabled. Clarification sends and internal
notes are disabled. A real browser signed into the deployed UI, exercised
Monaco completions and server errors in the three policy editors, and previewed
synthetic Jira ticket `ASHE2E-4`. It reached the approval-ready state without
creating a tracked case or investigation. Draft edits were not published.
Desktop/mobile checks passed with no runtime errors or external browser requests.

Before rollout and final cleanup, authenticated MCP reads confirmed no active
investigation or answer. After verification, the temporary account was removed and the
workspace restarted. The original account records are identical and all 60
preexisting report-directory files retain their hashes. The deployed service
returned HTTP 200 after the final restart. Evidence is under
`triagelab-v1-live/chat-mcp/verification/cel-monaco-deployment-20261111` on the VM.


## Monaco CEL editor — November 11, 2026

On `bondren-build`, built the local adaptation of Agentgateway v1.7.0's editor
with pinned Monaco 0.57.0 and React wrapper 4.7.0. Two isolated, authenticated
loopback workspaces used GitHub and Zendesk configurations without provider
credentials. Their real TriageLab server supplied validation and procedure
persistence; these checks did not start investigations or contact ticket providers.

- Playwright Chromium checked source-specific property suggestions, accepting a
  completion, builder/raw round trips, standard CEL function suggestions,
  inline server errors, and clearing stale markers. A diagnostic after two
  emoji landed on the correct identifier after Unicode-to-UTF-16 conversion.
- Three simultaneous policy editors retained independent values. Invalid CEL
  was rejected by the save API; valid match, evidence, and automatic-start
  expressions saved and survived reload. Each workspace made nine successful
  compiler-validation requests through the actual authenticated API.
- Tab exited the editor, desktop and 390-pixel mobile views were inspected,
  and there was no horizontal overflow. Monaco loaded only when raw CEL was
  opened. Browser requests stayed on the workspace origin, with no runtime or
  CSP errors. CSP permits Monaco's runtime styles, with scripts and workers
  restricted to the same origin.
- `make check build`, `make install-ui check-ui build-ui`, and
  `make install-docs build-docs` passed on the VM. The dependency audit was
  clean after overriding Monaco's DOMPurify pin to patched 3.4.17.
  Vite reports the expected large lazy editor chunk (about 0.9 MB gzipped).

Screenshots and the browser script are retained on the VM under
`triagelab-v1-live/chat-mcp/verification/cel-monaco-20261111`. Temporary
workspaces were stopped and their login credentials removed. The running
workspace deployment was not replaced.



## Live CEL automatic launch — November 11, 2026

On `bondren-build`, a separate loopback workspace read the existing synthetic
Jira issue `ASHE2E-4` using the real Jira connector. The workspace used the
disposable `kind-triagelab` kagent/Substrate deployment and its configured
`gpt-4.1` investigator. The test used private reports and accounts separate
from the running workspace.

- A condition requiring an absent label left the ticket ready for approval
  with no run ID and no new investigation report.
- After saving a condition matching the issue's existing label, refreshing
  started an unattended run. The report recorded the matching CEL expression
  and real sandbox process evidence.
- Run `161a8051-a818-49b2-bb43-d15da40eb70b` stopped after an out-of-scope
  sandbox access attempt and deleted its Sandbox and Session. A second
  unattended run, `0bdaabd7-1703-40b3-82ca-0685073bfd9f`, paused for missing
  information. Resuming with the fixture's limitations completed round two
  with an inconclusive outcome and confirmed Sandbox and Session cleanup.
- Playwright signed into the rebuilt workspace on the VM and exercised the
  builder and raw CEL modes, syntax highlighting, line and column diagnostics,
  preview against the live Jira issue, saving all three condition types, and
  the mobile layout. Preview and save created no tracked case or run.

## Live results chat and agentgateway deployment — November 11, 2026

Deployed on `bondren-build` using the existing disposable `kind-triagelab`
cluster, installed investigator, and `gpt-4.1` credential from
`kagent/kagent-openai`. This verification used real model calls and real
kagent/Substrate sandboxes. All builds, tests, and browser execution remained
on the VM. [Access and service operations](build-vm-chat-mcp.md) document the
installed release and private SSH-forwarded endpoints.

- Agentgateway v1.7.0 serves separate authenticated model and HTTP MCP routes.
  The workspace, gateway, and controller port-forward run as enabled user
  systemd services. All four service ports bind only to loopback. Model,
  frontend MCP, and backend MCP keys are distinct; the provider credential
  is loaded from the existing Kubernetes Secret into the gateway environment.
- Missing and incorrect MCP credentials returned 401. The frontend key was
  rejected by the backend, confirming credential substitution at the gateway.
  The backend viewer principal could read but could not start a run, even
  with an injected admin-role header. The model route also rejected an
  unauthenticated request.
- All **10 MCP tools** succeeded through the actual gateway. The primary
  investigation (`65bd6ccb-fe01-4e4d-84bf-cd5a6c3299f3`) reproduced the
  fixture failure and correction in three independently collected experiments.
  Report pagination used four pages and process evidence used six stream pages.
  A real chat answer included two validated evidence citations, and the
  canonical report's SHA-256 remained unchanged.
- The browser signed into the actual workspace and submitted exactly one
  question. The real answer cited a saved process; evidence review, keyboard
  dismissal, reload persistence, deep links, and mobile layout passed. JSON and
  Markdown reports remained byte-identical. A viewer could read the answer,
  had no composer, and received 403 for a direct submission. No browser
  runtime errors occurred. Desktop, mobile, and evidence screenshots were
  inspected.
- A second investigation (`f31b98c8-d23b-4f5c-a27c-735d0acfc88a`) paused and
  resumed into round two using the same Sandbox and Session. Its round-one
  archive remained byte-identical; it completed naturally with four processes.
  A stale-round polling race in the test harness missed a second pause window;
  the harness was corrected without replaying an accepted mutation.
- A third, narrowly scoped case (`3816b5d7-6af3-46cd-a140-5a6f57c38c95`)
  paused before the first process and successfully exercised explicit close
  using fresh round/settings bindings. Close preserved the case, analysis,
  evidence, and round archive. All three cases recorded successful Sandbox
  and Session deletion without cleanup errors.
- Independent controller reads confirmed all three exact Sandbox IDs in
  `RUNTIME_STATE_DELETED` and all three Session IDs with explicit `not_found`
  error codes. Authentication or transport failures were not counted as
  cleanup success.
- Removed both temporary browser accounts while the workspace was stopped,
  then restarted it. The original account records were unchanged, all 39
  saved report/chat files retained their hashes, and MCP read the browser's
  saved answer through the gateway after restart. All three services were
  active and enabled, with user lingering enabled. The temporary password
  file was removed. Checked workspace/gateway journal records contained
  neither configured credentials nor saved chat content; the provider key
  was absent from the workspace process environment.

The first live chat request exposed OpenAI rejecting `tool_choice` when no
`tools` are supplied. The request now omits both fields, with a regression test;
returned tool calls remain rejected. The failed question was preserved, and an
explicit new question succeeded after deployment of the fix. No uncertain or
accepted mutation was automatically retried.

Remote `make fmt build check-ui build-ui docs-check` passed, followed by a full
`make check`. After the provider compatibility fix, the result-chat package
tests, rebuild, and another full `make check` passed. The full checks include
gofmt, `go vet`, Go tests, and reference drift checks, using Go 1.27.3,
Node 24.18.1, and `GOFLAGS=-trimpath`.

The private runtime verification records are under
`/home/will_bondren_ai/triagelab-v1-live/chat-mcp/verification` on the VM.
The deployment uses machine credentials over SSH; a public HTTPS domain and
real OAuth provider were not configured or tested. MCP actions are attributed
to the shared gateway principal. The workspace's kagent invocation token lasts
24 hours and is refreshed by an idle workspace restart; chat's model credential
is independent.

## Results chat and remote HTTP MCP fixture checks — November 11, 2026

Implemented and cross-reviewed with three subagents. All builds, dependency
installation, tests, gateway execution, and browser execution ran on
`bondren-build`, using Go 1.27.3 and Node 24.18.1 in the isolated task checkout.

- `make fmt docs-generate check docs-check` passed, including all Go tests,
  `go vet`, and generated-reference drift checks. Result-chat tests cover
  bounded report/history projection, round-specific citation validation,
  no-tools requests, refusal/tool-call/partial/oversized response rejection,
  credential redaction, cancellation, and transport failures without retries.
- Webserver tests cover durable questions before model calls, unchanged
  canonical reports, private atomic chat storage, cross-process locks,
  restart/shutdown interruption, corrupt journals, symlink refusal, role and
  terminal-run admission, and redaction of old conversation content under
  newly configured credentials in both model input and HTTP responses.
- MCP tests use the real Go SDK transport and schemas, with fixture workflows:
  separate bearer authentication, per-call role isolation, rejected extra
  fields/unsafe uploads, asynchronous starts and busy admission, stale lifecycle
  bindings and ticket continuation policy, bounded chat receipts/pages, UTF-8
  and snapshot-bound report pages, and rejection of ambiguous round/process
  attribution. Focused race checks passed for result-chat and MCP/chat server
  paths; the final `go test -race ./internal/webserver -run 'Test(MCP|Chat)'
  -count=1` passed after the last hardening changes.
- `make check-ui build-ui` passed. Headless Chromium passed 13 distinct
  synthetic-API scenarios with no page errors: submission/polling, literal
  model text, historical citations and keyboard evidence review, role/lifecycle
  gating, failed/interrupted answers, uncertain submission reconciliation without
  replay, byte bounds, read errors, 401/403 cache clearing, mobile overflow,
  interrupted/timed-out runs, and resetting chat state between investigations.
  Desktop/mobile screenshots were inspected. The task preview was stopped.
- `make build-docs` passed with every internal link valid. Existing optional
  i18n and 404 content warnings remain.
- A real **agentgateway v1.7.0** binary, SHA-256
  `1b2fc01a2de29ca113bbb6258b5cae7b1eb3c724071c09d687a449132f2f42f8`,
  passed configuration validation and 20 end-to-end assertions against a
  freshly built TriageLab server plus disposable RSA JWT/JWKS/discovery and
  saved-report fixtures. Missing, expired, wrong-issuer, and wrong-audience
  tokens were rejected. An unapproved subject saw no tools and could not call
  them, including when reusing an approved subject's gateway session. Backend
  requests rejected both absent credentials and frontend JWTs; approved
  gateway requests succeeded using the separate backend principal. Listing,
  report pagination, historical evidence, and OAuth metadata all worked.
  The gateway negotiated `2025-11-25`, used SSE and a session ID externally,
  and connected to TriageLab's stateless JSON backend. Canonical report bytes
  were unchanged. All task listeners were loopback-only and stopped afterward.

The final full check also found a startup panic in concurrently added Jira
source: a regular-expression repetition exceeded Go's maximum. Splitting that
range into equivalent permitted repetitions preserved its accepted paths and
removed the panic.

These earlier checks used fixtures, not a live model or a real identity-provider
browser login. The live model and private deployment checks are recorded above;
public HTTPS and real OAuth verification remain open. No investigation sandbox,
cluster, production deployment, or public endpoint was changed during this
fixture phase. The gateway example's shared backend key records the gateway
principal, not individual OAuth subjects.

## Investigation workspace and evidence review — November 11, 2026

Verified in an isolated source checkout on `bondren-build` using Go 1.27.3,
Node 24.18.1, and `GOFLAGS=-trimpath`. No builds, tests, containers, or browser
execution ran on the local laptop.

- `make fmt check docs-check` passed: formatting, `go vet ./...`, all Go tests,
  and generated-reference drift checks. New behavior tests cover queue state
  projection and privacy, archived experiment counts, incomplete or invented
  evidence references, contradiction references, model assessment separation,
  redaction, and retained-session answers from the older output contract.
- `make check-ui build-ui` and `make build-docs` passed. All documentation
  internal links validated. The docs build still reports its existing missing
  optional i18n collection and 404 content warnings.
- The final output schema passed the unmodified `CheckStrict` function taken
  from the pinned kagent commit `048e1016d50b209dba01065d0d10dfd80b78fb41`.
  Rendered default and custom managed-agent schemas also have regression tests
  for required properties and closed objects. New native output fields remain
  required; local validation fills only absent explanatory fields in answers
  from older retained sessions.
- Headless Chromium exercised the Vite UI on loopback with synthetic intercepted
  API responses. Checks passed for the brief, evidence review, literal imported
  text, modal keyboard focus and Escape, incomplete-output warnings, historical
  round links after reload, an inspector open while polling advances the round,
  current-round resume review, ticket-note preview, 390px mobile layout, queue
  refresh failure preserving confirmed rows, denial clearing them, unconfirmed
  retention without a lifecycle record, and the historical saved example.
  No mutation requests were sent by this browser check. Desktop and mobile
  screenshots were visually inspected.

These checks do not prove a new live model or sandbox run. The installed
investigator and cluster were not changed. New experiment assessments require
an updated investigator definition; earlier retained sessions can omit them.

## kagent 1.0.0 upgrade — November 10, 2026

Executed remotely on `bondren-build` after kagent v1.0.0 was released. The fork
was built and tested with Go 1.27.3. No builds, tests, containers, or cluster
work ran on the local laptop.
The earlier `triagelab` cluster and its database were deleted rather than
upgraded. A new Kind cluster was installed with `scripts/install-dev.sh`: kagent
CLI and chart 1.0.0, Substrate 1.0.0 with gVisor, and controller `048e1016`
built from the rebased fork on the released `controller:1.0.0` image.

- Fork commit `048e1016` is v1.0.0 plus the retention patch. `go build ./...`,
  `go vet ./core/... ./api/...`, `go test -race` with envtest across 47 packages
  covering the set upstream used to validate its v1.0.0 backports, `buf` lint,
  breaking, and generation checks, and `make -C go lint` passed. See
  [bondrenai/kagent#1](https://github.com/bondrenai/kagent/pull/1).
- The installed database applied core migrations 1 through 4, including the
  release's `000003_session_revert_operation` and the fork's renumbered
  `000004_retain_suspended_sandboxes`. The SandboxTemplate and investigator
  Agent became Ready.
- The fork's `TestSandboxRetentionWhileSuspended` **passed in 96.23 seconds**:
  an ordinary suspended Sandbox expired, an opted-in one survived its original
  TTL, and restoration preserved its ID and file bytes with one fresh bounded
  TTL that a duplicate resume did not renew.
- `TestLiveInvestigation` **passed in 32.59 seconds** against the real `gpt-4.1`
  model with outcome `reproduced`: four experiments (exit 1, 0, 1, 0) including
  an observed timeout failure and a corrected run returning HTTP 200, complete
  output collection, four evidence references for the root cause, and deletion
  of the Sandbox and Session. The report carries one warning: a hypothesis
  lacking complete evidence was marked untested.
- `TestLiveInvestigationPauseResume` **passed in 23.31 seconds** against the same
  model. Run, resume, and close executed in separate OS processes; resume and
  close used a freshly issued caller token. Both rounds kept the same Sandbox
  and Session with a new task per round, and each pause confirmed
  `SUSPENDED/NONE` with the retention flag. Round 1 recorded the Python failure
  (exit 1); round 2 recorded the failure and a corrected run (exit 0) with fresh
  process IDs. Every experiment's output collection completed. Substrate logged
  the Session Actor checkpointing at 18:24:47.804 UTC, restoring at 18:24:50.508
  UTC, and checkpointing again at 18:24:56.980 UTC under one Actor UID. Close
  deleted both resources.
- `make check` and `make docs-check` passed with the updated pins under Go
  1.27.3 and under the VM's default go1.26.10, which built the two live
  TriageLab tests. `make build-docs` passed with all internal links valid.

Evidence: the live investigation [JSON](verification/agent-demo-v1.0.0.json) and
[Markdown](verification/agent-demo-v1.0.0.md) reports, and the pause/resume
[JSON](verification/pause-resume-v1.0.0.json) and
[Markdown](verification/pause-resume-v1.0.0.md) reports. All four were checked
for credential values before saving. The provider credential was carried from
the deleted cluster's Secret through a temporary private file on the VM,
deleted afterward, without logging its value.

Not re-run on v1.0.0: the sandbox smoke checks for DNS/HTTP egress denial and
process termination, the microVM backend, browser interaction, and live ticket
flows. The Substrate distribution and kagent's egress policy source are
unchanged from the rc3 check. These checks do not represent weeks of elapsed
uptime. The sections below describe runs on rc3.
## OAuth API clients — November 10, 2026

Executed remotely on `bondren-build` with Go 1.27.3; no builds, tests, or
containers ran on the local laptop.

- `make check` passed (gofmt, `go vet ./...`, all Go tests), including the new
  suites: `internal/auth` client store (persistence and failed-save rollback,
  damaged-file refusal, token lifecycle and per-client bound, revocation on
  every grant change, reserved-prefix rejection for single sign-on names),
  the OAuth protocol tests (Basic and body credentials, dual-method
  rejection, scope narrowing and `invalid_scope`, every non-
  client-credentials grant refused, URL-parameter refusal, oversized and JSON
  bodies, no CORS headers, revocation ownership, and interop with
  `golang.org/x/oauth2/clientcredentials` over real HTTP in all three auth
  styles), and `internal/webserver` (Bearer enforcement, scope and ownership
  boundaries, the refused surface, the cross-site marker rules, the admin
  client API, and route classification).
- `make check-ui build-ui`, `make build-docs` (25 pages, all internal links
  valid), `make chart-lint`, and `docker build` (the offline vendored image
  build) passed. The generated reference pages were regenerated for the new
  `serve --clients` flag.
- The live smoke (`scripts/oauth-smoke.sh`, served UI with an unreachable
  investigator endpoint) passed end to end: admin sign-in, client creation
  over the admin API, token issuance over Basic auth without the request
  marker, a Bearer launch answered 202, `started_by: client:<id>` recorded in
  the report, listing and report download through the API, a narrowed
  read-only token refused at launch (403), revocation (200) followed by 401
  on the next call, and disabling the client ending both its fresh token and
  further issuance (401). zitadel/oidc's route-registration lines appeared in
  the serve log.
- Not exercised live: the API clients page in a browser (typechecked and
  built, not clicked), a non-Go OAuth client library, and any flow against a
  real kagent deployment; the standard Go client is covered by the interop
  test against the same endpoint.

Dependency note: this adds the module's second direct third-party dependency,
`zitadel/oidc v3.52.0` (Apache-2.0, OpenID-certified), vendored; see the
[upstream record](upstream.md) for the review, including why fosite was
rejected and which library parts are deliberately unused.

## Pause/resume integration — November 10, 2026

Executed remotely on `bondren-build` with Go 1.27.3. No builds, tests, containers,
or cluster work ran on the local laptop.

- `make fmt docs-generate check` passed: generated reference pages, formatting,
  `go vet ./...`, and all Go tests. The lifecycle fixtures cover automatic and
  operator pause, same-session continuation, immutable rounds, restart recovery,
  stale approvals, concurrent admission, cancellation, uncertain invocation,
  evidence precision, and cleanup. These fixtures do not prove live restoration.
- `make check-ui build-ui` passed. `make build-docs` passed with all internal
  links valid. Installer scripts passed `bash -n`. Browser interaction was not
  exercised in this check.
- The isolated PR branch was rebased onto the current microVM/model changes and
  passed the full Go, UI, and documentation checks again. Remote installer
  fixtures verified that an existing controller or an inspection error stops
  installation before mutations, while a fresh cluster reaches the install step.
- Race checks passed for the investigation and ticket packages, and for all six
  new web lifecycle tests (restart/privacy, stale approval, pause, resume/active
  slot, close, and failure feedback). The broader web race run encountered
  `no space left on device` in temporary test files; the targeted retry passed
  with test fixtures under `/dev/shm`. The full web suite passed without race
  instrumentation under `make check`.
- `TestLiveInvestigationPauseResume` **passed in 22.35 seconds** against the real
  `gpt-4.1` model and disposable `triagelab` cluster, with controller `9bf1a20d`,
  Session idle expiration disabled, and the updated trusted prompt. The first
  attempt had failed with OpenAI HTTP 401; replacing the rejected credential
  resolved that failure. The key was validated with a bounded provider request,
  updated through Kubernetes Secret stdin, and verified without logging its value.
- Run, resume, and close executed in separate OS processes. Both rounds produced
  real experiments and honored an operator pause. Resume kept the same Sandbox
  and Session, created a new task, preserved the original file bytes, and uploaded
  new input files. The first round's report stayed byte-identical and its task
  history remained readable. Resume and close used a freshly issued caller token.
- The Session Actor checkpointed at 12:39:16.714 UTC, restored at 12:39:19.551 UTC,
  and checkpointed again at 12:39:25.977 UTC. Both tasks have native snapshot
  references. This verifies restoration of the conversation runtime as well as
  the standalone sandbox. Round 1 recorded the Python failure (exit 1) and a
  corrected override (exit 0); round 2 recorded a new failure experiment with a
  fresh process ID. Every experiment's output collection completed.
- Cleanup deleted the test Sandbox and Session. The original controller image,
  ConfigMap data, and investigator prompt were restored and compared with their
  saved originals; the Agent returned to Ready. The corrected provider credential
  remains installed. No credential values were recorded in this repository.

Evidence: [JSON report](verification/pause-resume.json) and
[Markdown report](verification/pause-resume.md), including both rounds and final
cleanup. Both artifacts were checked for credential values before saving.

The fork's separate real Sandbox retention test passed: an ordinary suspended
sandbox expired, an opted-in sandbox survived its original TTL, and restoration
preserved its ID and file bytes with one fresh bounded TTL. Duplicate resume did
not renew that deadline. Together these checks cover passing the original expiry
deadline and real model continuation; they do not represent weeks of elapsed
uptime. There is no new snapshot service or Substrate API in TriageLab.

Re-run `make test-pause-integration` with `TRIAGELAB_CONFIG`, a fresh invocation
token, and a real model credential on the disposable deployment. Private attempt
artifacts from the passing run remain on the VM under
`/home/will_bondren_ai/triagelab-pause-verification.FFPejT`.

## Original investigation integration — November 8, 2026

Verified on November 8, 2026 (America/New_York) on a dedicated Linux development
VM: 4 vCPU, 16 GiB RAM, Ubuntu 24.04, kind Kubernetes 1.35.0, Go 1.25.12,
kagent v1.0.0-rc3, its matching Substrate v1.0.0 distribution, and gVisor.
The investigator used OpenAI `gpt-4.1`. No agent output or sandbox was mocked in
these live checks.

## Passed

- `make check`: formatting validation, `go vet ./...`, and all unit/HTTP protocol
  tests. The live test is intentionally skipped without its opt-in variable.
- `make build`: the Go CLI compiled on the VM.
- Generated resources passed Kubernetes server-side dry-run and applied
  successfully. Agent and SandboxTemplate reached Ready.
- `make test-integration` with the real deployment, caller token and model:
  **PASS**, 22.6 seconds. It verified ingestion, actual kagent invocation,
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

Not executed in this verification: multi-user deployment, production
authorization, controller/VM crash during a run, or broad hostile
workload security testing. The microVM backend was exercised later; see
[MicroVM backend verification](#microvm-backend-verification). Exact binary
artifact export and external issue
connectors remain roadmap items. The development VM is ephemeral; saved evidence
is retained in the repository, not dependent on its continued availability.

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
- With kagent absent, an actual UI submission saved an infrastructure-error
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
configured private kagent/Substrate deployment and model credential.

## Zendesk ticket procedure verification

On the remote development VM, `make check`, `make build`, and `make build-ui`
passed. The ticket tests use a local TLS HTTP fixture with Zendesk response
shapes. They cover read-only preview, policy-authorized clarification,
recovery across a restart, unconfirmed writes, requester replies, changed
attachments, stale approval, and a busy run slot. A Chromium walkthrough
using fixture API responses checked the queue, approval, run link, and
desktop/mobile accessibility. These checks did not call a Zendesk account,
send customer messages, or start a live kagent investigation.

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
GitHub-to-kagent investigation, model call, or sandbox run was performed for
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

No live GitHub write or GitHub-to-kagent investigation was performed. HTTP and
workflow fixtures demonstrate the tested request and persistence behavior, not
provider permissions, real notification delivery, or an atomic GitHub
compare-and-send guarantee.


## Custom agent definitions

The remote development VM passed `make check`, `make build`, `make check-ui`,
and `make build-ui` with Go 1.25.12 and Node 24.18.1. Unit and HTTP fixtures
covered native manifest generation, input restrictions, manual agent routing,
ticket-default separation, and the watchdog's treatment of added MCP calls.
Both agent types passed offline structural validation against the pinned
kagent rc3 CRD schemas. Kubernetes CEL rules and controller reconciliation
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
`casefile.Ingest` refuses; the VM remains the check environment. Live kagent
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
live Linear clarification, summary send, or Linear-to-kagent investigation
was performed. The GraphQL document shapes were pinned against the published
Linear API documentation; a controlled live pilot remains to be run.

## OpenTelemetry export verification

On 2026-11-09, live OTLP/HTTP export from real TriageLab processes was
verified on the shared bondren-build VM against `otel/opentelemetry-collector:0.165.0`
(loopback 4318, debug exporter), using the binary built at commit `4543e4c`
(Go 1.27.3 toolchain) and `triagelab.telemetry.example.json` plus the
`headers_env` indirection.

- `triagelab doctor --telemetry` against the live kagent controller
  (kind cluster, `triagelab-tools` port-forward, short-lived
  `kagent-invocation` token): exported the `triagelab doctor` command span and
  a child `kagent.rpc` span (`mcp.call=list_sandbox_templates`) under one
  trace, plus the `triagelab.kagent.rpc.duration` data point.
- `triagelab investigate --telemetry` with the scripted `triagelab-demo-runtime`
  agent on the standard config-bug fixture: a real sandbox was created,
  populated, and deleted through kagent. The task ended `TASK_STATE_FAILED`
  (the scripted demo model does not drive this agent's investigation to a
  completed analysis), so the run completed on the **failure path** with
  `status=error`, `outcome=inconclusive`, exit code 4, and complete cleanup
  (`sandbox_deleted=true`, `session_deleted=true`, no cleanup errors). The
  collector received the full tree: `investigation.run` (attrs: run_id,
  trigger=cli, agent, sandbox_template, status, outcome) with child spans
  `investigation.sandbox.create`, `inputs.upload`, `session.create`,
  `session.invoke`, two `session.poll`, three `evidence.observe`, and
  `investigation.cleanup` (attrs: sandbox_deleted, session_deleted, errors=0),
  alongside 14 `kagent.rpc` spans. The `triagelab.investigation.runs`,
  `duration`, `experiments`, and `cleanup` metrics were exported.
- `triagelab serve --telemetry` (loopback 8099, local-account sign-in):
  exported `server started`/`server stopped` log records and `http` spans for
  `/api/settings` (both the 401 before login and the 200 after), the login
  POST, and an unknown deep API path collapsed to `http /api/other`;
  `triagelab.http.requests`/`http.duration` arrived with route/method/status
  attributes. `/healthz` correctly produced a metric without a span, and a
  serve start that failed on an occupied port exported its command span with
  the error status on shutdown flush.
- Log correlation: the phase log records ("Creating an isolated sandbox",
  "Uploading 3 files") carried the run span's exact trace and span IDs. The
  final-cleanup message is uncorrelated by design because cleanup runs on its
  independent bounded context.

Not exercised live: a **successful** model-backed investigation outcome
(the scripted demo model cannot produce one; no provider credential was used),
ticketflow action spans against a live helpdesk, and collector header
authentication (the wire behavior is covered by unit tests decoding real OTLP
protobuf in `internal/telemetry/telemetry_test.go`). Resource attributes were
`service.name=triagelab`, `service.version=0.1.0`; no case content, prompts,
credentials, or account names appeared in the exported telemetry.

## MicroVM backend verification

On 2026-11-09 the opt-in `microvm` CI job ran five times on GitHub-hosted
`ubuntu-latest` runners (image `ubuntu-26.04`). Each run built its own
disposable kind cluster: kind 0.33.0, Kubernetes 1.35.0, Helm 3.18.0, Go 1.27.3,
kagent v1.0.0-rc3, and its Substrate v1.0.0 distribution. The investigator used
OpenAI `gpt-6.1-sol` over the Responses API with the model's default reasoning
effort. No agent output or sandbox was mocked.

Passed in all five runs:

- The runner created a KVM guest (`KVM_CREATE_VM`), and the kind node received
  `/dev/kvm`.
- `TRIAGELAB_SANDBOX_CLASS=microvm scripts/install-dev.sh` completed in about
  four minutes, including Substrate's asset installer. `kagent-default` used
  the `microvm` class and SandboxConfig with the `ateworker-microvm:v1.0.0`
  image, and the SandboxTemplate and Agent reached Ready.
- The live test created a sandbox on that pool and uploaded three files, and
  the investigator read fixture files through `read_sandbox_file`.

Four runs at commit `02d9fa3` passed `make test-integration`, each in 34 to 35
seconds with the same nine tool calls: the failing experiment, the corrected
experiment, a supported `reproduced` outcome, and deletion of both runtimes.
They are runs [42718539343](https://github.com/bondrenai/TriageLab/actions/runs/42718539343),
[42724580664](https://github.com/bondrenai/TriageLab/actions/runs/42724580664),
[42724585200](https://github.com/bondrenai/TriageLab/actions/runs/42724585200), and
[42724589679](https://github.com/bondrenai/TriageLab/actions/runs/42724589679).

| Command | Exit | Observation |
| --- | --- | --- |
| `python3 app.py` | 1 | 5 ms deadline expires before the simulated 120 ms response |
| `python3 app.py --timeout-ms 5000` | 0 | Corrected timeout succeeds |

Evidence from run 42718539343: [human-readable report](verification/microvm-demo.md)
and [machine-readable report](verification/microvm-demo.json).

The first run ([42716580719](https://github.com/bondrenai/TriageLab/actions/runs/42716580719),
commit `5f59591`) failed. After two successful file reads the investigator's
task ended `TASK_STATE_FAILED` seven seconds after it started, with no
experiment. Cleanup succeeded. The cause is unknown: reports did not yet keep
the upstream failure text, which `task_failure` now records. Four passes in
five runs is a small sample, not a reliability measurement.

Limits:

- The isolation backend is inferred from the WorkerPool class, worker image,
  and SandboxConfig. Nothing observed it from inside the guest.
- The sandbox lifecycle and egress-denial checks in `sandbox-smoke.json` were
  not repeated on microVMs. Suspend/restore, resource limits, and hostile
  workloads were not tested.
- kagent rc3 replays tool calls without the model's reasoning items, which
  OpenAI's guidance asks clients to pass back. The passing runs show the API
  accepts that; the sample is too small to call it reliable.
- Shell and workflow linting, YAML parsing, and a `helm template` of the pinned
  kagent chart ran on the development laptop. `make check` ran on the shared
  build VM and in CI for both commits.

## Model API format option

On 2026-11-09 the shared build VM passed `make check`, `make check-ui`, and
`make build-ui` for the OpenAI API format option on model definitions. Unit
tests cover rendering `spec.openAI.apiFormat` beside a custom endpoint and
rejecting the option for other providers and unknown values.

Playwright 1.65.0 with Chromium 1254 exercised the built server and assets on
the VM at 1440px and 390px, signed in with a throwaway local admin account. The
Models editor showed the option only for the OpenAI provider, previewed
`apiFormat: responses`, kept the choice after returning to the editor, and
omitted the block for the default. There was no horizontal overflow or browser
error. The temporary server and account were removed.

No model call used a model defined this way. The live runs above exercise the
Responses API only through the installed `triagelab-model`.
