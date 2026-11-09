# Roadmap

- [ ] Pilot the Zendesk ticket procedure against a disposable account, then add
      Bondren as a second source with shared ticket identity and one owner for
      automated replies. Use pilot evidence to guide procedure authoring and CEL.

- [ ] Harden capability boundaries: per-investigation sandbox-scoped credentials,
      enforce process-count/time admission server-side, and remove remaining
      investigator runtime file builtins when upstream supports it.
- [ ] Extend the opt-in live integration workflow with repeatable, cost-bounded
      disposable infrastructure across KAgent upgrades and
      failure scenarios (connection loss, model errors, TTL expiry, cancellation).
- [ ] Add read-only GitHub Issue ingestion with sanitized attachments and source
      references through the existing connector interface.
- [ ] Add exact binary artifact export through upstream gRPC file transfer, with
      size limits, checksums, and interruption recovery. MCP read-file content and
      command outputs are currently captured in the report.
- [ ] Validate additional templates for networking/Kubernetes reproductions and
      an optional KVM-backed microVM WorkerPool.
