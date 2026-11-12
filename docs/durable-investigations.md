# Investigation pause and resume

Status: upstream retention patch and TriageLab workflow implemented, 2026-11-10.
Since 2026-11-12 TriageLab treats retention as an optional controller
capability: it probes the controller at run start and runs the same workflow
without pause and resume when only a release controller is installed.
The controller pin is
[`bondrenai/kagent` commit `048e1016d50b209dba01065d0d10dfd80b78fb41`](https://github.com/bondrenai/kagent/commit/048e1016d50b209dba01065d0d10dfd80b78fb41),
based on kagent `v1.0.0`, commit
`a08cde76ad6482dd758a4220b14213d79185ac30`.
This replaces the earlier checkpoint-copy/fork proposal. Both the standalone
Sandbox retention test and the real-model pause/resume test passed; see
[verification.md](verification.md) for the evidence and limits.

## Design

Keep the same standalone Sandbox and the same investigator Session when work
pauses. Use kagent's existing `suspend_sandbox` and `resume_sandbox` operations.
Substrate already saves durable workspace files during suspension; kagent already
suspends idle investigator runtimes and resumes them on the next interaction.
Keeping those resources also keeps their runtime revisions pinned.

A paused investigation remains available for weeks, until explicitly resumed or
closed. Snapshot storage and control-plane records remain allocated while paused.
There is no automatic retention deadline in the first version. Completion or
explicit close deletes both execution resources through the existing cleanup path.

This needs no new checkpoint resource, independent snapshot copy, fork, retention
scheduler, or Substrate API. The necessary upstream change is limited to sandbox
expiration while paused. [Existing sandbox contract][sandboxes],
[Session lifecycle][sessions].

## Implemented upstream kagent change

The fork adds an optional `retain_while_suspended` creation flag, defaulting to
false, and persists it with the originally requested bounded active TTL. TriageLab opts in
for resumable investigations. Existing clients keep their fixed-lifetime behavior.

Use existing suspend/resume/delete RPCs and MCP tools with these semantics:

1. Only an opted-in sandbox at `SUSPENDED` with operation `NONE` is exempt from
   expiration. Suspension must complete and confirm its durable snapshot before
   that exemption applies. Pending operations remain bounded.
2. On the first resume admission from that retained state, atomically set
   `expires_at = database_now + active_ttl` and record the RESUME operation under
   the sandbox row lock. Permit this transition even after the former active
   deadline. Retry of the same operation, or resume of an already-ready sandbox,
   must not extend the deadline.
3. Pending RESUME remains eligible for expiration. Failed or abandoned restoration
   must not create indefinitely running compute. Once deletion is admitted,
   resume cannot revive the sandbox.
4. The expiration worker must recheck the deadline and retention exemption under
   the same row lock immediately before admitting deletion. Its current scan then
   ordinary-delete sequence could otherwise delete a sandbox selected before a
   concurrent resume refreshed its deadline.
5. Explicit deletion still works for retained sandboxes. Public Sandbox/MCP output
   exposes the retention flag and omits the active `expires_at` while stably paused;
   a resumed sandbox reports its new active deadline. No current active TTL limits
   are increased.

The implementation touches `proto/kagent/api/v1/sandboxes.proto`, sandbox store/service
and MCP adapters, generated bindings, and a new additive database migration.
Policy is persisted per Sandbox so restarts do not change its meaning. Existing
rows retain ordinary expiry; their unused active TTL defaults to zero. The
additive migration preserves the existing view projection for old-controller
reads. Lifecycle documentation and focused tests accompany the patch.
[Operation admission and expiry selection][sandbox-store],
[expiration worker][sandbox-workflow].

For investigator Sessions, use the existing `config.sessions.idleTtl: 0s` setting
in this dedicated deployment. It disables automatic idle deletion; automatic
quiescence remains separate. No new Session API or checkpoint API is required.
This setting applies deployment-wide, so TriageLab must explicitly clean up
finished/closed Sessions and retain diagnostics for unsuccessful cleanup.
[Existing Session expiration setting][session-expiration].

The focused `TestSandboxRetentionWhileSuspended` passed against real gVisor and
Substrate on the disposable Linux deployment. It waits for an ordinary suspended
Sandbox with a later deadline to expire, then restores the retained Sandbox and
checks its ID, file bytes, fresh active TTL, duplicate resume, and deletion.
This proves the elapsed-expiry path without claiming weeks of elapsed uptime.

`scripts/install-dev.sh` installs the released 1.0.0 stack, builds the pinned fork
controller with `scripts/install-fork-controller.sh`, loads it into Kind, and
waits for its rollout before installing TriageLab resources. No fork image has
been published. See [development.md](development.md) and [upstream.md](upstream.md)
for the source and image pins.

## TriageLab behavior

**Pause**

1. Finish or cancel the current investigator turn and observe it settling. When
   the investigator needs information, it can finish normally with a small
   existing `missing_information` array of blocking questions. An operator pause
   uses existing task cancellation and waits for cessation; it is not a memory
   freeze in the middle of a tool call.
2. Finish or stop outstanding experiments and collect bounded process evidence
   before guest handles disappear.
3. Persist pause intent, call `suspend_sandbox`, and confirm `SUSPENDED/NONE`.
4. Persist `paused` and release the single active-run slot. Keep both resource IDs.
   The existing deferred cleanup must not delete resources on this path.

**Resume**

1. Acquire the existing active-run slot and validate the saved upstream connection,
   identity, resources, new inputs, and fresh execution budget.
2. Persist resume intent, call `resume_sandbox`, and confirm `READY/NONE`.
3. Add new information/files through the existing bounded ingestion path, preserving
   prior inputs, then continue the same Session using `invoke_agent`.
4. Save the returned task identity and append this round's evidence and analysis.
   Do not blindly resend an uncertain model invocation.

The Session has no public suspend/resume operation. Its runtime suspends
automatically while the logical Session remains `READY`; the next interaction
restores it through Substrate ingress. TriageLab's MCP tasks capability makes
`invoke_agent` return an asynchronous task receipt. The adapter validates its
encoded Session and task IDs before the workflow records the new turn.

**Persistence and cleanup**

- Keep a small private JSON lifecycle record beside reports with resource IDs,
  connection/settings, pending action, and round/task IDs. Reuse private atomic
  write and locking patterns; store no credentials. Exported redacted reports
  are evidence views, not the sole operational resume record.
- Preserve previous rounds. Qualify process evidence by round because suspension
  resets the guest process registry; never re-fetch historical handles on resume.
- After application restart, show the saved lifecycle state. An explicit Resume
  reconciles a pending suspend/resume using existing lifecycle retry rules.
  Do not automatically restart paid work.
- Completion or explicit close runs existing task/session/sandbox cleanup and
  records failures. A failed pause must be visible, never described as safely
  paused. Ordinary interruption/timeout cleanup stays bounded.
- Keep current workspace role enforcement. For ticket investigations, reuse review
  and reservation patterns to approve added evidence and the saved limits before resume.
  Existing ticket tracking pause remains a separate operation.

## Implementation

1. **kagent expiry patch — complete:** the opt-in flag, persisted active TTL, atomic resume
   deadline, expiration recheck, adapters, migration, and focused tests.
2. **Shared investigation workflow — complete:** pause/resume paths, private lifecycle record,
   continuation in the same Session, and preservation of evidence across rounds.
3. **CLI/API/UI — complete:** Pause, Resume with new information, and Close. Show paused work
   after restart. Route all actions through the same workflow and active-run slot;
   retain existing ticket approval requirements.
4. **Pin update and live verification — complete:** after replacing the rejected
   OpenAI credential, the real-model test passed across separate run/resume/close
   processes. It verified two paused rounds, the same Sandbox and Session,
   restored workspace files and conversation runtime, immutable prior evidence,
   fresh caller credentials, and deletion. See [verification.md](verification.md).

## Acceptance checks

- A retained suspended sandbox remains resumable after its original active TTL;
  an ordinary sandbox still expires normally.
- Resume gets one fresh bounded deadline. Duplicate/retried resume does not renew
  it. Pending failed resume expires. A stale expiration scan cannot delete a
  successfully resumed sandbox, and admitted deletion cannot be undone.
- Pause collects evidence before suspension. Previous round outputs remain intact
  when new process handles are issued. Duplicate UI/CLI resume is rejected locally.
- Stop TriageLab while paused, restart it with refreshed credentials, resume the
  same Sandbox and Session, and verify workspace file digests and investigator
  continuity with a real model-backed turn. Repeat pause/resume once more.
- Close verifies deletion of both resources. Missing resources or uncertain
  lifecycle operations produce actionable errors rather than a fresh investigation.

Run `make check` with `GOFLAGS=-trimpath` and relevant UI checks on the dedicated
Linux environment. Record live checks separately from fixtures. Do not claim
multiweek resume from an ordinary short-lifetime suspend test alone; exercise
the elapsed-expiry path explicitly.

Durability depends on retaining the upstream databases and snapshot storage.
VOLUMES preserves durable directories such as `/data/workspace`, not arbitrary
root filesystem edits, process memory, or running commands. Paused resources are
retained until closed, and cleanup can fail during outages. These are the first
version's explicit limits; independent backups, checkpoint branching, retention
policies, and broader recovery automation are outside this implementation.

[sandboxes]: https://github.com/bondrenai/kagent/blob/048e1016d50b209dba01065d0d10dfd80b78fb41/docs/architecture/sandboxes.md
[sessions]: https://github.com/kagent-dev/kagent/blob/a08cde76ad6482dd758a4220b14213d79185ac30/docs/architecture/runtime-and-lifecycle.md
[sandbox-store]: https://github.com/bondrenai/kagent/blob/048e1016d50b209dba01065d0d10dfd80b78fb41/go/core/internal/database/sandboxes.go
[sandbox-workflow]: https://github.com/bondrenai/kagent/blob/048e1016d50b209dba01065d0d10dfd80b78fb41/go/core/internal/service/sandbox/workflow.go
[session-expiration]: https://github.com/kagent-dev/kagent/blob/a08cde76ad6482dd758a4220b14213d79185ac30/go/core/internal/service/session/expiration.go
