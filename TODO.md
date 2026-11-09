# Roadmap

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
- [ ] Exercise the new sign-in surface against a real identity provider (the
      OIDC client is tested against a local fake only) and pilot the account
      workflow: bootstrap, role changes, lockout recovery, and the shared
      users-file limitation between the CLI and a running server. Consider an
      append-only action audit log before widening who may launch runs.
- [ ] Repeat telemetry verification with a **successful** model-backed
      investigation and live ticket actions (the 2026-11-09 collector check in
      docs/verification.md covered the failure path, HTTP routes, RPC metrics,
      and log correlation end to end).
- [ ] Harden capability boundaries: per-investigation sandbox-scoped credentials,
      enforce process-count/time admission server-side, and remove remaining
      investigator runtime file builtins when upstream supports it.
- [ ] Extend the opt-in live integration workflow with repeatable, cost-bounded
      disposable infrastructure across KAgent upgrades and
      failure scenarios (connection loss, model errors, TTL expiry, cancellation).
      Exercise a declarative custom investigator and a BYO runtime with a
      reviewed external MCP tool on the pinned release.
- [ ] Add exact binary artifact export through upstream gRPC file transfer, with
      size limits, checksums, and interruption recovery. MCP read-file content and
      command outputs are currently captured in the report.
- [ ] Validate additional templates for networking/Kubernetes reproductions and
      an optional KVM-backed microVM WorkerPool. Cover templates authored through
      the Sandboxes and Egress editors, including one egress-granted reproduction
      against a disposable deployment.
