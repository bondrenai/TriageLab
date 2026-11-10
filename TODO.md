# Roadmap

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
      test passed beyond the original expiry. See [verification](docs/verification.md) and
      [lifecycle design](docs/durable-investigations.md).
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
- [ ] Repeat the Zendesk pilot with a controlled requester inbox and verify
      internal note delivery. Run a controlled GitHub issue pilot covering
      clarification follow-ups, investigation comments, and uncertain delivery.
      Add a Linear pilot for one workspace and team covering the same sends
      (the connector is fixture-verified only). Then add Bondren with shared
      ticket identity and one owner for automated replies. Use pilot evidence
      to guide procedure authoring and CEL.
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
- [ ] Extend the opt-in live integration workflow with repeatable, cost-bounded
      disposable infrastructure across kagent upgrades and
      failure scenarios (connection loss, model errors, TTL expiry, cancellation).
      Exercise a declarative custom investigator and a BYO runtime with a
      reviewed external MCP tool on the pinned release.
- [ ] Add exact binary artifact export through upstream gRPC file transfer, with
      size limits, checksums, and interruption recovery. MCP read-file content and
      command outputs are currently captured in the report.
- [ ] Repeat the opt-in `microvm` CI job enough times to measure reliability:
      one of the first five runs failed with an unrecorded cause, before reports
      kept `task_failure`. Repeat the sandbox lifecycle and egress-denial
      checks on that backend.
- [ ] Validate additional templates for networking/Kubernetes reproductions.
      Cover templates authored through the Sandboxes and Egress editors,
      including one egress-granted reproduction against a disposable deployment.
