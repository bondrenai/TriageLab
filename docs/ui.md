# Browser workspace

TriageLab is an independent, free product with a small Bondren signature. Its UI
uses Bondren's ink-forward drafting system: cool paper, graphite ink, a midnight
navigation frame, Plex Sans and Plex Mono, and ultramarine interaction marks.
Marcellus is reserved for the BONDREN wordmark. The workspace centers on case
inputs and recorded experiments rather than aggregate dashboard metrics.

## Run on a development VM

Use Node.js 24 and Go 1.25.12. Install dependencies and build on the VM:

```sh
make install-ui
make build-ui
make build
bin/triagelab serve --config triagelab.example.json --output reports
```

The default listener is `127.0.0.1:8080`; `--assets` defaults to `web/dist`.
Run from the repository root or pass an absolute assets directory. Forward the
port with your existing SSH configuration, for example:

```sh
ssh -N -L 8080:127.0.0.1:8080 YOUR_DEV_VM
```

Open `http://127.0.0.1:8080`. The UI lists existing CLI reports in the selected
output directory. Its saved example is bundled from the repository's real
verification report and is explicitly separate from your investigation history.
Viewing history or the example does not require a running model provider.

To investigate, configure KAgent and its model credential as described in
[development setup](development.md), forward the controller on the VM, and set
`TRIAGELAB_TOKEN` in the server's environment before starting it. Neither that
token nor the model key belongs in browser configuration. Free source code does
not include provider or VM usage; those services may charge for a run.

## Iterate

Keep the Go server running, then run `make dev-ui` on the VM. Vite listens on
loopback port 5173 and proxies `/api` to the Go server. Forward that port too:

```sh
ssh -N -L 5173:127.0.0.1:5173 YOUR_DEV_VM
```

Open `http://127.0.0.1:5173` for hot reload. Ordinary UI iteration should stay
focused on the affected screen. Before landing a change, run the checks relevant
to its behavior on the VM:

```sh
make check-ui
make build-ui
make check
```

The API is a local single-user interface, not a hosted service. It accepts one
investigation at a time. Closing a tab leaves the investigation running; use
Cancel to stop it. Server shutdown also cancels active work and allows bounded
cleanup. After an abrupt crash, inspect the report and use the CLI cleanup
command if resources remain. A recorded outcome does not override cleanup errors.

## Data flow

The browser uses an API transport module, typed domain functions, and query hooks
before presentation components. Report text renders as text, including command
output; it never becomes executable HTML. The Go service normalizes uploads with
the existing ingestion package and invokes the existing investigator. JSON stays
the canonical report. No database, queue service, browser model SDK, or alternate
execution backend is introduced.
