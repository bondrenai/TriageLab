# Roadmap

- [x] Add opt-in GitOps reconciliation for namespaced ticket connections and
      procedures, with Secret references, UI ownership protection, and retained
      case state on deletion. See the [GitOps guide](docs-site/src/content/docs/guides/gitops.md).
- [x] Verify a model-backed investigation from GitOps-managed connections and
      procedures against a real self-managed GitLab instance. Approval gating,
      CR updates, input hashes, failed/corrected sandbox processes, restart
      persistence, and independent cleanup passed on the build VM.
- [ ] Pilot these alpha CRDs with Argo CD or Flux; verify synchronization and
      pod-projected API-token rotation. See [verification](docs/verification.md).

- [ ] Start `examples/missing-orders` from the New investigation form and add
      it to the scheduled live verification. A run started from a labelled
      GitHub issue has been verified, including the posted comment. Since
      2026-11-12 it is the main demonstration: the workspace's bundled
      example, `make demo`, and the quickstart example, after three live runs
      reproduced the loss, rejected the customer's theory, and verified a
      correction. See
      [verification](docs/verification.md) and the
      [demonstration notes](docs/demo.md). Those runs used a local arm64
      Docker Desktop cluster reached through a Linux container, a path that is
      not documented or supported yet.

- [x] Verify CEL automatic launch against live Jira and kagent on the build VM.
      The same synthetic issue exercised a blocking and an allowing rule.
- [ ] Pilot CEL procedure routing and conditional evidence with more ticket types.
      Evaluate CEL for clarification eligibility under the existing send controls.

- [x] Add saved, evidence-linked results chat and an authenticated HTTP MCP
      surface for reading, starting, and steering investigations. Agentgateway
      provides the optional remote OAuth edge; the workspace remains loopback.
      See [chat and MCP guide](docs-site/src/content/docs/guides/chat-mcp.md).
- [x] Verify results chat with the existing `gpt-4.1` credential and private
      HTTP MCP through agentgateway on the build VM. Real browser chat and all
      ten MCP tools passed, including start, pause, resume, close, saved evidence,
      and complete cleanup. See [build VM access](docs/build-vm-chat-mcp.md).
- [ ] Before publishing MCP, configure and verify the deployment's real HTTPS
      domain and OAuth provider. The private deployment uses API keys over SSH;
      its shared backend key does not establish per-user attribution.

- [x] Make investigation review and continuation easier: a compact brief,
      contextual evidence review, explicit execution/outcome/resource states,
      round history, and a queue showing the next useful action. New experiment
      assessments remain model analysis; older reports and retained sessions
      remain readable. See [workspace guide](docs/ui.md).
- [x] Add OAuth API clients: admins register clients with
      `investigations:run`/`investigations:read` scopes; applications use the
      RFC 6749 client credentials grant at `/oauth/token` and Bearer tokens on
      the run API, steering only the runs they started. Landed 2026-11-10 with
      the pinned zitadel/oidc v3.52.0 owning protocol handling (see
      [upstream record](docs/upstream.md) and the
      [API clients guide](https://bondrenai.github.io/TriageLab/guides/api-clients/)).
      Open: pilot one real integration end to end, and add RFC 8414 metadata
      or token introspection only when an integrator needs them.
- [x] Pause and resume investigations using the same sandbox and conversation.
      Verified 2026-11-10 with a real model: two rounds across process restart,
      sandbox and conversation restoration, preserved files and evidence, fresh
      caller credentials, and complete cleanup. The fork's separate retention
      test passed beyond the original expiry. Both passed again on kagent 1.0.0
      with the rebased fork. See [verification](docs/verification.md) and
      [lifecycle design](docs/durable-investigations.md).
- [ ] Run the evaluation corpus in `examples/` live and calibrate
      `examples/corpus.json`. Five of its six cases, including the
      prompt-injection bait, have never met a model; their expectations are
      design intent until a scorecard exists.
- [ ] Review held drafts and blocked notes from the first pilots and tune the
      input and output sanitizers: deny-list matching thresholds, per-source
      internal terms, and provenance of applied redactions, before widening
      automated replies.
- [ ] After the first tagged release, verify the *published* ghcr.io image and
      OCI chart end to end on a disposable deployment, including a real
      model-backed investigation and ticket-state persistence. (A live kind
      cluster already verified the local image and chart mechanics on
      2026-11-09: install, readiness, doctor, port-forward UI, loopback
      isolation, token rotation, failed-run report on the PVC, and full
      sandbox/session cleanup; the model call was the only unexercised step.)
      The reworked release workflow has itself never run: the CI gate, the
      linux/arm64 image, the provenance attestation, and the CLI binaries
      attached to the GitHub Release are all unexercised.
- [ ] Repeat the Zendesk pilot with a controlled requester inbox and verify
      internal note delivery. Run a controlled GitHub issue pilot covering
      clarification follow-ups, investigation comments, and uncertain delivery.
      Add a Linear pilot for one workspace and team covering the same sends
      (the connector is fixture-verified only). Then add Bondren with shared
      ticket identity and one owner for automated replies. Add a Jira pilot
      for one Cloud site covering the same sends (the connector is
      fixture-verified only, including the observed signed-attachment
      redirect shape). Add a GitLab pilot for one project on GitLab.com or a
      self-managed instance covering the same sends (the connector is
      fixture-verified only, including note-body normalization and the
      project uploads download). Use pilot evidence
      to guide procedure authoring and CEL. (The Jira pilot ran 2026-11-11
      against a real Cloud site: lookups, live signed attachment download,
      clarification with bounded follow-up, a model-backed two-round run
      approved from a Jira case, and internal-note delivery all passed; it
      found and fixed the issue-id/comment-id numeric collision, and a Jira
      Service Management project remains to be exercised for internal-note
      visibility. The GitLab connector passed live checks against a
      disposable self-managed CE 19.5.5 instance on 2026-11-12 — connection
      verification, upload download through real evidence preparation,
      clarification with a bounded follow-up, and investigation-comment
      delivery — which also found and fixed an undersized response bound in
      the connection probe. A GitOps-managed model-backed run from a GitLab
      case also passed; GitLab.com remains to be exercised. See
      [verification](verification.md).)
- [x] Exercise the new sign-in surface against real identity providers
      (done 2026-11-09 on the build VM: Keycloak 26 confidential client and
      Zitadel v4.20 public PKCE client, full authorization-code flows with
      admin/user/viewer role mapping and API enforcement; provider setup
      notes in docs/ui.md). The browser login-UI round trip was driven for
      Keycloak by form automation; Zitadel's Login v2 web-component UI was
      completed headlessly through the documented Session API, and its
      full-browser path remains manually unexercised. Still open: pilot the
      account workflow (bootstrap, role changes, lockout recovery, shared
      users-file limitation between CLI and a running server) and consider an
      append-only action audit log before widening who may launch runs.
- [ ] Repeat telemetry verification with a **successful** model-backed
      investigation and live ticket actions (the 2026-11-09 collector check in
      docs/verification.md covered the failure path, HTTP routes, RPC metrics,
      and log correlation end to end).
- [ ] Harden capability boundaries: per-investigation sandbox-scoped credentials,
      enforce process-count/time admission server-side, and remove remaining
      investigator runtime file builtins when upstream supports it.
- [ ] Run the scheduled `Live verification` workflow and record its first
      results in docs/verification.md. It builds the disposable gVisor
      deployment on a GitHub runner and runs the sandbox boundary check
      (nested upload, DNS/HTTP egress denial, termination, deletion; no model),
      the live investigation, and pause/resume. Drafted 2026-11-12 and not yet
      executed: the 1.0.0 upgrade re-ran the demonstration, retention, and
      pause/resume checks by hand, and DNS/HTTP egress denial and process
      termination were last exercised on rc3.
      Then extend it to failure scenarios (connection loss, model errors, TTL
      expiry, cancellation), and exercise a declarative custom investigator
      and a BYO runtime with a reviewed external MCP tool on the pinned release.
- [ ] Add exact binary artifact export through upstream gRPC file transfer, with
      size limits, checksums, and interruption recovery. MCP read-file content and
      command outputs are currently captured in the report.
- [ ] Repeat the `microvm` class of the `Live verification` workflow enough
      times to measure reliability: one of the first five runs of the earlier
      `microvm` CI job (on rc3) failed with an unrecorded cause, before reports
      kept `task_failure`. The workflow now also runs the sandbox lifecycle and
      egress-denial checks and pause/resume on that backend; none of it has run
      there on the 1.0.0 pins.
- [ ] Validate additional templates for networking/Kubernetes reproductions.
      Cover templates authored through the Sandboxes and Egress editors,
      including one egress-granted reproduction against a disposable deployment.
