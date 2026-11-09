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
