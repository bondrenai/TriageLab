# Contributing

TriageLab welcomes focused changes that make technical investigations more reliable
and reproducible. Read [the architecture](docs/architecture.md),
[the upstream compatibility notes](docs/upstream.md), and
[the security model](SECURITY.md) before changing agent or sandbox behavior.

Use the pinned Go 1.25.12 toolchain. Python 3 is needed only to execute the included
fixture outside its sandbox in the fixture unit test. In a checkout:

```sh
make fmt
make check
```

Keep `go.mod` and `go.sum` updated when changing dependencies. `make check` mirrors
CI with formatting, tests, and vetting. Use the local-development guide
to configure a dedicated KAgent/Substrate test environment. It can run on a remote
Linux VM; no Docker socket is exposed to the investigator or reproduction sandbox.
Never use a production cluster for the example or integration tests.

A useful pull request explains the concrete problem, resulting behavior, relevant
tests, and any remaining uncertainty. Add tests for important behavior and failure
paths. Source-level protocol tests and unit tests are not evidence of a successful
live KAgent investigation; label those results accurately. Keep changes narrowly
scoped and avoid adding a custom controller, agent framework, or job system.

The repository follows conventional Go organization: `cmd/` entry
points, `internal/` packages, typed startup validation, context cancellation,
explicit connector registration, behavior-focused tests, and one Makefile entry
point for each common task. The TriageLab MVP has no web frontend or database.

The project is licensed under Apache 2.0. By submitting a contribution, you agree
to license it under the same terms. Report security issues privately as described
in [SECURITY.md](SECURITY.md); do not attach live credentials or private support data
to public issues.
