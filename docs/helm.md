# Helm deployment

The public Helm chart installs the TriageLab workspace — the `serve` process
with the built web UI — as a single-pod Deployment beside an existing
KAgent/Substrate installation. It does **not** install or replace KAgent;
follow [development](development.md) first for the pinned upstream stack, or
point the chart at an equivalent dedicated installation you already run.

This is the same private, single-user workspace as the local CLI, not a
multi-tenant service. The pod listens only on `127.0.0.1:8080` inside its
network namespace; the chart creates no Service or Ingress because the server
refuses non-loopback listeners by design. Reach the UI with
`kubectl port-forward`, the Kubernetes equivalent of the documented SSH
tunnel rule. Do not deploy into a shared or production cluster.

## Install

The chart is published as an OCI artifact from the public registry:

```sh
helm install triagelab oci://ghcr.io/bondrenai/charts/triagelab \
  --version <chart version> --namespace triagelab --create-namespace
```

Prerequisites:

1. **KAgent workspace resources.** The agent, sandbox template, and the
   internal `triagelab-tools` Service must exist in the configured KAgent
   namespace. Render them from the deployed image if they are not installed:

   ```sh
   kubectl -n triagelab exec deploy/triagelab -- triagelab manifest \
     | kubectl apply -f -
   ```

2. **Invocation credential.** Mint a short-lived token in the cluster that
   runs KAgent and store it as a Secret (preferred over `--set token.value=`,
   which is retained in Helm release metadata):

   ```sh
   kubectl -n kagent create token triagelab \
     --audience=kagent-invocation --duration=1h
   kubectl -n triagelab create secret generic triagelab-token \
     --from-literal=TRIAGELAB_TOKEN=<minted token>
   ```

   Then install with `--set token.create=false --set token.secretName=triagelab-token`.

   The token is read once at container start and KAgent tokens are
   short-lived. Rotating the Secret does not refresh the running process:
   re-mint, update the Secret, and `kubectl -n triagelab rollout restart
   deploy/triagelab`. Automating rotation is deliberately out of scope.

3. **Access.** The install notes print the port-forward command:

   ```sh
   kubectl -n triagelab port-forward deploy/triagelab 8080:8080
   ```

   Open `http://127.0.0.1:8080` on the same machine. Anyone with this port has
   full workspace access; do not share or proxy it.

Reports and private ticket state persist in a PersistentVolumeClaim mounted at
`/data/reports` (`persistence.*`; disable for an ephemeral emptyDir). The
optional ticket procedures ([Zendesk](tickets.md), [GitHub
issues](github-issues.md)) are enabled with `tickets.enabled` and
`tickets.config`; their JSON is stored in a Secret, and the credential named
by `token_env` comes from `tickets.token` or your own Secret.

## Cluster HTTP endpoints

The workspace reaches KAgent through the internal ClusterIP Service over
plain HTTP, `http://triagelab-tools.kagent.svc.cluster.local:8083/mcp` by
default. The CLI otherwise requires HTTPS off-loopback, because a laptop's
network cannot be trusted with a plaintext bearer token.

In-cluster plain HTTP is an explicit, narrow opt-in:

- `config.allow_cluster_http: true` in the chart's investigation
  configuration, and the matching `kagent.AllowClusterHTTP()` client option
  wired from it.
- It accepts only `*.svc` and `*.svc.cluster.local` hostnames. IP literals,
  public names, and anything similar to a cluster domain stay rejected in both
  the config validator and the client constructor.
- The pod network of a dedicated cluster is the trust boundary. That is the
  same boundary the upstream resources already use: the rendered agent
  definition's remote MCP server points at the same Service over plain HTTP.

Keep NetworkPolicy or an equivalent upstream mechanism limiting which pods can
reach the controller. If your policy requires HTTPS everywhere, front the
controller with TLS and set an `https://` endpoint; `allow_cluster_http` then
stays unnecessary and can be disabled.

## Publishing (maintainers)

The [release workflow](../.github/workflows/release.yml) publishes both
artifacts on a pushed `v*` tag (or a manual dispatch with a version input):

- the linux/amd64 container image built from the root `Dockerfile`, as
  `ghcr.io/bondrenai/triagelab:<version>`;
- the chart packaged with that version and appVersion, as
  `oci://ghcr.io/bondrenai/charts/triagelab`.

One-time setup after the first release: change both ghcr.io packages
(`triagelab` and `charts/triagelab`) from private to public in the
repository's Packages settings; newly created container packages default to
private even in public repositories.

CI lints and renders the chart on every change (`make chart-lint`) and builds
the image without publishing. The chart's default image tag tracks the chart
appVersion; pin `image.digest` to a reviewed digest for controlled
deployments, in line with the project's digest-pinning rules.

Chart versions follow the application tag (`v0.1.0` → chart `0.1.0`/`v0.1.0`)
so one release updates the image and chart together.

## Limitations

- Single replica only: the workspace keeps its active-run and ticket state in
  memory; the chart refuses `replicaCount > 1`.
- No Service, Ingress, authentication, or multi-user access; the workspace is
  reached exclusively through `kubectl port-forward`.
- Deployment status and resource writes (`--status-kubeconfig`,
  `--allow-resource-writes`) are not wired into the chart; extend the pod
  arguments explicitly if you accept that scope.
- Token rotation is manual (see above).
- The published image is linux/amd64.
