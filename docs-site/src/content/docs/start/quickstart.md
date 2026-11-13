---
title: Quickstart
description: Install kagent and Agent Substrate on a dedicated VM and run the included example investigation.
---

This guide installs kagent and Agent Substrate in a kind cluster on a Linux
VM, then runs the included example investigation. Run every command on the
VM; your laptop only needs SSH.

There is no offline or simulated mode. The example calls a real model and runs
in a real sandbox, and your model provider bills for the calls.

## 1. Prepare a VM

Use a dedicated, disposable Linux VM. Start with 4 vCPUs, 16 GiB of RAM, and
about 30 GiB of free disk. Install:

- Docker, Git, kubectl, Helm 3, and kind 0.33.0
- Go 1.27.3, plus Node.js 24 if you want the browser workspace

You also need an OpenAI API key. The example investigator uses `gpt-6.1-sol`
through the Responses API.

Never install this setup into a production or shared cluster. Its kagent
authentication is for private development only.

## 2. Get the code and the kagent CLI

Clone the repository, then download and verify kagent CLI 1.0.0. This example
is for Linux amd64; for other architectures, see
[development setup](https://github.com/bondrenai/TriageLab/blob/main/docs/development.md).

```bash
git clone https://github.com/bondrenai/TriageLab.git
cd TriageLab
mkdir -p .tools
curl -fL https://github.com/kagent-dev/kagent/releases/download/v1.0.0/kagent-linux-amd64 \
  -o .tools/kagent
echo 'c1a0a37767c4c2526e6d42b7016cfc11e69e82e321f6b84c1499d539d0dd864d  .tools/kagent' \
  | sha256sum -c -
chmod +x .tools/kagent
export PATH="$PWD/.tools:$PATH"
```

## 3. Create the cluster and install the stack

Create the cluster:

```bash
kind create cluster --name triagelab --image kindest/node:v1.35.0 \
  --config deploy/kind.yaml --wait 120s
```

Enter your OpenAI API key at the hidden prompt, then run the installer and
build the CLI:

```bash
read -rs OPENAI_API_KEY && export OPENAI_API_KEY
bash scripts/install-dev.sh
unset OPENAI_API_KEY
make build
```

The installer changes only the `kind-triagelab` cluster. It:

- installs Agent Substrate and the released kagent 1.0.0 controller, and
  gives the API key to kagent;
- applies TriageLab's agent and sandbox template and waits until both are
  ready.

It stops without changing anything if the cluster already has a kagent
controller. Durable [pause and resume](/TriageLab/guides/pause-resume/) is an
opt-in extra: install with `TRIAGELAB_CONTROLLER=fork`, or run
`scripts/install-fork-controller.sh` afterwards, to swap in the controller
fork that keeps paused investigations from being deleted. Without it, pause is
refused and a run that needs more information completes its round. The pinned
Kubernetes interfaces are still alpha (`v1alpha3`);
read the [upstream compatibility record](/TriageLab/reference/upstream/)
before upgrading anything.

## 4. Connect to kagent

The CLI reaches the kagent controller through a port forward. Start it in its
own terminal and leave it running:

```bash
kubectl --context kind-triagelab -n kagent port-forward \
  --address 127.0.0.1 svc/kagent-controller 8083:8083
```

## 5. Run the example

In a second terminal, from the repository root, create a one-hour access token
and check the connection:

```bash
export TRIAGELAB_TOKEN="$(kubectl --context kind-triagelab -n kagent \
  create token triagelab --audience=kagent-invocation --duration=1h)"
./bin/triagelab doctor
```

`doctor` reports whether the kagent sandbox API is reachable. Then run the
example:

```bash
./bin/triagelab investigate \
  --description-file examples/missing-orders/ticket.md \
  --repo examples/missing-orders/repro
```

The example is a support ticket whose own diagnosis is wrong: a customer
blames lock retries for orders that a nightly sync silently drops. A run takes
about two minutes. The CLI prints where it saved the report. Keep the token out of logs and shell
output, and create a new one when it expires.

Without `--config`, the CLI uses the same settings as `triagelab.example.json`.
To change the endpoint, time limits, or experiment budget, copy that file and
pass `--config`; the [configuration reference](/TriageLab/reference/configuration/)
lists every field.

Next, read [what the example investigation shows](/TriageLab/start/example/)
and how its report is laid out.

## Run your own case

```bash
./bin/triagelab investigate --description 'The service rejects valid requests' \
  --repo ./small-reproduction --log ./service.log --file ./config-example.json
```

`--log` and `--file` can be repeated. TriageLab copies the inputs into
`/data/workspace` in the sandbox; nothing is mounted from the host. Everything
you submit goes to the model provider. Keep repositories small, and review
support data before sending it: secret detection catches only obvious cases.

## If cleanup fails

Each run records the IDs needed to recover. If the CLI exits with `5`, finish
cleanup from the saved report:

```bash
./bin/triagelab cleanup --report reports/<case-id>/report.json
```

The [CLI reference](/TriageLab/reference/cli/) lists every exit code.

## Tear down

Copy any reports you want to keep off the VM, then delete the cluster:

```bash
kind delete cluster --name triagelab
```

Delete the VM and its disk the same way you created them. A stopped VM still
incurs storage charges.

To use the browser instead of the CLI, continue with the
[browser workspace](/TriageLab/guides/workspace/).
