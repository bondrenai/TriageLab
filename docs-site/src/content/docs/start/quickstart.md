---
title: Quickstart
description: Install the pinned upstream stack on a dedicated VM and run the included example investigation.
---

TriageLab needs a dedicated, disposable Linux development environment. All
cluster work happens there; your laptop only needs an SSH tunnel and a browser.

## Prerequisites

- Go **1.27.3** or later in the supported 1.27 line, and Node.js 24 for the UI.
- A **dedicated Linux development VM** with Docker, kind, kubectl, Helm, and the
  **kagent v1.0.0** CLI (4 vCPU / 16 GB RAM is the initial target).
- kagent **v1.0.0** plus its matching **Substrate v1.0.0** packaging.
- A model credential configured in kagent (the example uses `gpt-6.1-sol` through the Responses API).

The exact installation, port-forwarding, credential, and teardown steps are in
[development setup](https://github.com/bondrenai/TriageLab/blob/main/docs/development.md)
in the repository. The pinned Kubernetes interfaces are still alpha (`v1alpha3`); read the
[upstream compatibility record](/TriageLab/reference/upstream/) before upgrading anything.
Do not install the development configuration into a production cluster.

## Run the example

After the development cluster is up and the generated resources are applied:

```bash
make build
# In another terminal on the VM:
kubectl -n kagent port-forward svc/kagent-controller 8083:8083
# In your CLI shell (do not echo this token):
export TRIAGELAB_TOKEN="$(kubectl -n kagent create token triagelab \
  --audience=kagent-invocation --duration=1h)"
./bin/triagelab doctor --config triagelab.example.json
./bin/triagelab investigate --config triagelab.example.json \
  --description-file examples/config-bug/issue.md --repo examples/config-bug
```

`doctor` verifies the kagent sandbox API is reachable. `investigate` runs the
included Python timeout example end to end and prints the report location.

## Run your own case

```bash
./bin/triagelab investigate --description 'The service rejects valid requests' \
  --repo ./small-reproduction --log ./service.log --file ./config-example.json
```

Keep submitted repositories small and sanitized. Files are copied into
`/data/workspace` in the sandbox, never mounted from the host. Obvious-secret
detection is not a substitute for reviewing support data before sending it to
a model provider.

## If cleanup fails

Each run records everything needed to recover. If the CLI exits `5`, finish
cleanup from the saved report:

```bash
./bin/triagelab cleanup --config triagelab.example.json \
  --report reports/<case-id>/report.json
```

See the [CLI reference](/TriageLab/reference/cli/) for the full exit-code table, then
walk through [what the example investigation shows](/TriageLab/start/example/).
