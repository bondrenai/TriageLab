# Helm deployment

The public Helm chart installs the TriageLab workspace — the `serve` process
with the built web UI — as a single-pod Deployment beside an existing
kagent/Substrate installation. It does **not** install or replace kagent;
follow [development](development.md) first for the pinned upstream stack, or
point the chart at an equivalent dedicated installation you already run.

This is the same private workspace as the local CLI, now with required
sign-in, but not a multi-tenant service. The pod listens only on `127.0.0.1:8080` inside its
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

1. **kagent workspace resources.** The agent, sandbox template, and the
   internal `triagelab-tools` Service must exist in the configured kagent
   namespace. Render them from the deployed image if they are not installed:

   ```sh
   kubectl -n triagelab exec deploy/triagelab -- triagelab manifest \
     | kubectl apply -f -
   ```

2. **Invocation credential.** Mint a short-lived token in the cluster that
   runs kagent and store it as a Secret (preferred over `--set token.value=`,
   which is retained in Helm release metadata):

   ```sh
   kubectl -n kagent create token triagelab \
     --audience=kagent-invocation --duration=1h
   kubectl -n triagelab create secret generic triagelab-token \
     --from-literal=TRIAGELAB_TOKEN=<minted token>
   ```

   Then install with `--set token.create=false --set token.secretName=triagelab-token`.

   The token is read once at container start and kagent tokens are
   short-lived. Rotating the Secret does not refresh the running process:
   re-mint, update the Secret, and `kubectl -n triagelab rollout restart
   deploy/triagelab`. Automating rotation is deliberately out of scope.

3. **Access.** The install notes print the port-forward command:

   ```sh
   kubectl -n triagelab port-forward deploy/triagelab 8080:8080
   ```

   Open `http://127.0.0.1:8080` on the same machine. Anyone with this port has
   full workspace access; do not share or proxy it.

Reports, private ticket state, and sign-in accounts persist in a
PersistentVolumeClaim mounted at `/data/reports` (`persistence.*`; disable for
an ephemeral emptyDir). The optional ticket procedures ([Zendesk](tickets.md),
[GitHub issues](github-issues.md), [Linear issues](linear-issues.md)) are
enabled with `tickets.enabled` and `tickets.config`; their JSON is stored in a
Secret, and the credential named by `token_env` comes from `tickets.token`
or your own Secret.

## Sign-in

The UI requires sign-in ([details](ui.md#sign-in-and-roles)). Local accounts
live in `/data/reports/.auth/users.json` on the reports volume; bootstrap the
first admin inside the pod:

```sh
kubectl -n triagelab exec -it deploy/triagelab -- \
  triagelab user add --username admin --role admin --password-stdin --output /data/reports
```

Optional single sign-on uses one OpenID Connect provider. Set `auth.enabled`
with `auth.config` (rendered verbatim to a Secret as `auth.json`, mirroring
`auth.example.json`); when `auth.config.oidc.client_secret_env` names the
client-secret environment variable, provide it with `auth.secret.value` or
point `auth.secret.secretName` at your own Secret. The redirect URI defaults
to `http://127.0.0.1:8080/api/auth/oidc/callback`; register that exact value
with the provider.

## Telemetry export (OTLP)

Optional OpenTelemetry export sends traces, metrics, and logs to an OTLP/HTTP
collector when `telemetry.enabled` is set with `telemetry.config` (rendered
verbatim to `telemetry.json` in the ConfigMap; field names must match
`internal/telemetry.Config` — see `triagelab.telemetry.example.json` at the
repository root). This is an **outbound** connection from the workspace pod to
your collector; no Service or Ingress is created for it, and the loopback-only
UI model is unchanged.

Collector authentication headers never go through Helm values: set
`telemetry.config.headers_env` to an environment variable name and supply the
newline-separated `Name: value` lines via `telemetry.headers.value` (rendered
to a Secret, prefer `telemetry.headers.secretName` for pre-created Secrets).
Plain-HTTP endpoints are accepted on loopback or with `insecure: true`, for an
in-cluster collector Service; anything else must be HTTPS.

## Cluster HTTP endpoints

The workspace reaches kagent through the internal ClusterIP Service over
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

The [release workflow](../.github/workflows/release.yml) runs on a pushed `v*`
tag (or a manual dispatch with a version input, started from that same tag).
It first runs the CI workflow's jobs (Go checks on Linux and macOS,
`make race`, `make lint`, `make vulncheck`, frontend, docs site, chart lint, image build); nothing is published
unless all of them pass in that run. It then publishes:

- the multi-arch (linux/amd64 and linux/arm64) container image built from the
  root `Dockerfile`, as `ghcr.io/bondrenai/triagelab:<version>`, stamped with
  the version (`-X main.version`) and with a build provenance attestation
  (adds `id-token: write` and `attestations: write` to the publish job only);
- the chart packaged with that version and appVersion, as
  `oci://ghcr.io/bondrenai/charts/triagelab`;
- static CLI binaries (`triagelab_<os>_<arch>` for linux and darwin on amd64
  and arm64) and a `SHA256SUMS` file, attached to the GitHub Release for the
  tag with the `gh` CLI. This separate job alone holds `contents: write`.
  Verify with `shasum -a 256 -c SHA256SUMS`. The binaries do not contain the
  web UI: `triagelab serve` also needs the built assets (`make build-ui`, or
  the container image).

The Go build cross-compiles on the runner and the image's final stage runs no
commands, so the arm64 image is built without emulation. Base images in the
`Dockerfile` are pinned by digest; refresh each tag and digest together.

One-time setup after the first release: change both ghcr.io packages
(`triagelab` and `charts/triagelab`) from private to public in the
repository's Packages settings; newly created container packages default to
private even in public repositories.

CI lints and renders the chart on every change (`make chart-lint`) and builds
the image without publishing. The release's `GITHUB_TOKEN` is otherwise
read-only; no new third-party actions are used (`gh` is preinstalled, and the
attestation action is first-party and pinned by commit SHA). The chart's default image tag tracks the chart
appVersion; pin `image.digest` to a reviewed digest for controlled
deployments, in line with the project's digest-pinning rules.

Chart versions follow the application tag (`v0.1.0` → chart `0.1.0`/`v0.1.0`)
so one release updates the image and chart together.

## Limitations

- Single replica only: the workspace keeps its active-run and ticket state in
  memory; the chart refuses `replicaCount > 1`.
- No Service or Ingress; the workspace is reached exclusively through
  `kubectl port-forward`. Sign-in gates the UI (local accounts or one OIDC
  provider, [roles](ui.md#sign-in-and-roles)), but roles do not isolate
  accounts from each other's reports on the shared volume.
- Deployment status and resource writes (`--status-kubeconfig`,
  `--allow-resource-writes`) are not wired into the chart; extend the pod
  arguments explicitly if you accept that scope.
- Token rotation is manual (see above).
- Telemetry export ships no collector: bring your own OTLP endpoint and mind
  that it is a new outbound network path from the workspace pod. Header
  secrets are only accepted through the `telemetry.headers` Secret path.
- The published image covers linux/amd64 and linux/arm64. The arm64 variant is
  built but not exercised by any automated test.
