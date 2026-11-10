# Roadmap

- [ ] Run the evaluation corpus in `examples/` live and calibrate
      `examples/corpus.json`. Five of its six cases, including the
      prompt-injection bait, have never met a model; their expectations are
      design intent until a scorecard exists.
- [ ] Clear the two CI jobs that fail on the current tree; the release
      workflow is gated on both. `make vulncheck` reports five HTTP/2
      advisories in the vendored `golang.org/x/net` v0.59.0, all fixed in
      v0.60.0. `make lint` reports seven findings: deprecated
      `ecdsa.PublicKey` coordinates in the OIDC key parsing and its test, a
      deprecated OpenTelemetry call in a test, and a self-comparison in a
      Linear test.
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
- [ ] Validate additional templates for networking/Kubernetes reproductions and
      an optional KVM-backed microVM WorkerPool. Cover templates authored through
      the Sandboxes and Egress editors, including one egress-granted reproduction
      against a disposable deployment.
