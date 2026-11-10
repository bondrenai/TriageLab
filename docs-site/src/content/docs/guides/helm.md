---
title: Helm deployment
description: Install the single-pod workspace beside an existing KAgent/Substrate installation from the public chart.
---

The public Helm chart installs the TriageLab workspace — the `serve` process
with the built web UI — as a single-pod Deployment beside an existing
KAgent/Substrate installation. It does **not** install or replace KAgent;
start from a working dedicated installation first.

This is the same private workspace as the local CLI, now with required
sign-in, but not a multi-tenant service. The pod listens only on
`127.0.0.1:8080` inside its network namespace; the chart creates no Service
or Ingress because the server refuses non-loopback listeners by design. Reach
the UI with `kubectl port-forward` — the Kubernetes equivalent of the SSH
tunnel rule. Do not deploy into a shared or production cluster.

## Install

```sh
helm install triagelab oci://ghcr.io/bondrenai/charts/triagelab \
  --version <chart version> --namespace triagelab --create-namespace
```

Prerequisites:

1. **KAgent workspace resources.** The agent, sandbox template, and internal
   `triagelab-tools` Service must exist in the configured KAgent namespace.
   Render them from the deployed image if they are not installed:

   ```sh
   kubectl -n triagelab exec deploy/triagelab -- triagelab manifest \
     | kubectl apply -f -
   ```

2. **Invocation credential.** Mint a short-lived token in the cluster that
   runs KAgent and store it as a Secret (preferred over
   `--set token.value=`, which is retained in Helm release metadata):

   ```sh
   kubectl -n kagent create token triagelab \
     --audience=kagent-invocation --duration=1h
   kubectl -n triagelab create secret generic triagelab-token \
     --from-literal=TRIAGELAB_TOKEN=<minted token>
   ```

   Then install with `--set token.create=false --set token.secretName=triagelab-token`.
   The token is read once at container start; rotating the Secret does not
   refresh the running process. Re-mint, update the Secret, and
   `kubectl -n triagelab rollout restart deploy/triagelab`. Automating
   rotation is deliberately out of scope.

3. **Access.** The install notes print the port-forward command:

   ```sh
   kubectl -n triagelab port-forward deploy/triagelab 8080:8080
   ```

   Anyone with this port has full workspace access; do not share or proxy it.

Reports, private ticket state, and sign-in accounts persist in a
PersistentVolumeClaim mounted at `/data/reports` (`persistence.*`; disable for
an ephemeral emptyDir). The optional ticket procedures
([Zendesk](/TriageLab/guides/tickets-zendesk/), [GitHub](/TriageLab/guides/tickets-github/),
[Linear](/TriageLab/guides/tickets-linear/)) are enabled with `tickets.enabled` and
`tickets.config`; their JSON is stored in a Secret, and the credential named
by `token_env` comes from `tickets.token` or your own Secret.

## Sign-in

Bootstrap the first admin inside the pod — local accounts live on the reports
volume:

```sh
kubectl -n triagelab exec -it deploy/triagelab -- \
  triagelab user add --username admin --role admin --password-stdin --output /data/reports
```

Optional single sign-on uses one OpenID Connect provider via `auth.enabled`
with `auth.config` (rendered verbatim to a Secret as `auth.json`). See
[sign-in and roles](/TriageLab/guides/sign-in/) for the configuration shape and the
provider roles mapping; register the exact redirect URI with the provider.

## Cluster HTTP endpoints

The workspace reaches KAgent through the internal ClusterIP Service over
plain HTTP, `http://triagelab-tools.kagent.svc.cluster.local:8083/mcp` by
default. The CLI otherwise requires HTTPS off-loopback, because a laptop's
network cannot be trusted with a plaintext bearer token.

In-cluster plain HTTP is an explicit, narrow opt-in: `config.allow_cluster_http:
true` in the chart's investigation configuration. It accepts only `*.svc` and
`*.svc.cluster.local` hostnames — IP literals, public names, and lookalikes
stay rejected in both the config validator and the client constructor. The
pod network of a dedicated cluster is the trust boundary. Keep NetworkPolicy
or an equivalent upstream mechanism limiting which pods can reach the
controller; if your policy requires HTTPS everywhere, front the controller
with TLS and set an `https://` endpoint instead.

## Limitations

- Single replica only: the workspace keeps active-run and ticket state in
  memory; the chart refuses `replicaCount > 1`.
- No Service or Ingress; the workspace is reached exclusively through
  `kubectl port-forward`. Roles do not isolate accounts from each other's
  reports on the shared volume.
- Deployment status and resource writes (`--status-kubeconfig`,
  `--allow-resource-writes`) are not wired into the chart; extend the pod
  arguments explicitly if you accept that scope.
- Token rotation is manual.
- The published image is linux/amd64.

Chart versions follow the application tag (`v0.1.0` → chart `0.1.0`) so one
release updates the image and chart together; pin `image.digest` to a reviewed
digest for controlled deployments.
