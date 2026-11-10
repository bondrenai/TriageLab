---
title: Browser workspace
description: Run the loopback-only workspace UI, forward it over SSH, and iterate on it.
---

The browser workspace is the same investigation workflow as the CLI behind a
small Bondren-styled UI: case inputs and recorded experiments, not dashboard
metrics. It serves history, live runs, evidence inspection, and report
downloads from the same `reports/` directory the CLI writes.

## Run it

Use Node.js 24 and Go 1.27.3 on the development VM:

```sh
make install-ui
make build-ui
make build
bin/triagelab serve --config triagelab.example.json --output reports
```

The default listener is `127.0.0.1:8080` and `--assets` defaults to `web/dist`.
The server binds only to loopback; reach it through an SSH local forward:

```sh
ssh -N -L 8080:127.0.0.1:8080 YOUR_DEV_VM
```

Open `http://127.0.0.1:8080`. Sign-in is required — see
[sign-in and roles](/TriageLab/guides/sign-in/). The UI lists existing CLI reports in the
selected output directory, including a separately labelled example bundled
from the repository's recorded verification report. Viewing history or the
example does not require a running model provider.

To investigate, configure kagent and its model credential as in the
[quickstart](/TriageLab/start/quickstart/), forward the controller port on the VM, and
set `TRIAGELAB_TOKEN` in the server's environment before starting it. Neither
that token nor the model key belongs in browser configuration. Model and cloud
usage can incur charges even though TriageLab is free.

## Operate it

- The server accepts **one investigation at a time**. Closing a tab leaves a
  run going; use **Cancel** to stop it.
- Server shutdown cancels active work and allows bounded cleanup. After an
  abrupt crash, inspect the report and use **Retry cleanup** on its page (user
  role or higher) or `triagelab cleanup` if resources remain.
- The workspace can also show live kagent deployment status and save kagent
  resources — see [defining agents](/TriageLab/guides/agents/).

## Iterate on the UI

Keep the Go server running and start `make dev-ui` on the VM. Vite listens on
loopback port 5173 and proxies `/api` to the Go server; forward that port too
and open `http://127.0.0.1:5173` for hot reload. Before landing a change, run
`make check-ui`, `make build-ui`, and `make check` on the VM.

The API is a local interface for a small team, not a hosted service. Report
text renders as text, never executable HTML; the Go service normalizes uploads
with the same ingestion package as the CLI and invokes the same investigator.
No database, queue service, browser model SDK, or alternate execution backend
is introduced.
