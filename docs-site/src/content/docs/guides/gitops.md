---
title: GitOps connections and procedures
description: Reconcile ticket sources and ordered procedures from Kubernetes resources.
---

TriageLab can continuously reconcile namespaced `TicketConnection` and
`TicketProcedure` resources in `triagelab.bondren.ai/v1alpha1`. Enable this
option on the existing workspace process. Helm still deploys the single
workspace; kagent's existing `Agent`, `SandboxTemplate`, and `EgressPolicy`
resources remain the source of truth for agent and sandbox configuration.

## Install

Use an explicitly selected dedicated cluster. Install the definitions before
applying custom resources; substitute your cluster context below:

```sh
kubectl --context YOUR-CONTEXT apply --server-side -f chart/triagelab/crds/
```

The chart includes the definitions in its `crds/` directory for new installs,
which requires permission to create cluster-scoped CRDs. Use Helm's `--skip-crds`
when an administrator already manages them separately or GitOps is disabled.
Helm does not upgrade or delete existing CRDs ([Helm CRD lifecycle](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)). Keep their versioned definitions
as a separate GitOps application and apply updates before upgrading the
workspace. In Argo CD use sync ordering for CRDs, Secrets, and the workspace;
in Flux use dependency ordering. Custom resources may arrive after the
workspace starts; reconciliation retries automatically.

Add these settings to the [Helm deployment](/TriageLab/guides/helm/):

```yaml
gitops:
  enabled: true
  workspace: support
  pollSeconds: 30
  secretNames:
    - github-support-token
```

Place `TicketConnection`, `TicketProcedure`, and referenced Secrets in the
workspace release namespace. `spec.workspace` must match `gitops.workspace`
(the release's full name by default). Run only one workspace process for each
namespace/workspace pair. This selector is a routing convention, not a tenant
security boundary; use separate namespaces and service accounts for isolation.

Create the named Secret through your existing secret delivery process. Commit
only its reference, never the credential. The chart grants `get` on precisely
the names in `gitops.secretNames`, `list` on these two CRDs, and `patch` on
connection status. It grants no Secret enumeration, resource-spec writes, or
sandbox privileges. A projected service-account token authenticates API reads
and status updates and is reread on each request as Kubernetes rotates it.
The workspace remains authenticated and loopback-only, with port-forward
access and one replica.

## Define the resources

Start with `deploy/gitops-example.yaml`. Replace the namespace, workspace,
repository, and Secret reference. The example uses the existing investigator
and Python sandbox, requires evidence, and leaves runs and replies subject to
the existing approval controls.

```yaml
apiVersion: triagelab.bondren.ai/v1alpha1
kind: TicketConnection
metadata:
  name: github-support
  namespace: triagelab
spec:
  workspace: support
  label: GitHub support
  config:
    source: github
    repository: YOUR-ORG/YOUR-REPO
    poll_seconds: 60
    policy:
      tag: reproduce
      auto_questions: false
      max_questions: 0
      auto_runs: false
  tokenSecretRef:
    name: github-support-token
    key: token
  procedureRefs:
    - python-timeout
```

`config` uses the ticket connector's existing JSON field names, except that
`token_env` is replaced by `tokenSecretRef`. GitHub public reads may omit the
Secret. GitLab, Linear, Bondren, and Jira use a Secret credential. Zendesk
keeps its existing browser OAuth grant; declare its connection settings and
then connect from the UI. Do not set `tokenSecretRef` for Zendesk.

A procedure resource wraps the existing procedure schema:

```yaml
apiVersion: triagelab.bondren.ai/v1alpha1
kind: TicketProcedure
metadata:
  name: python-timeout
  namespace: triagelab
spec:
  workspace: support
  procedure:
    policy:
      tag: reproduce
      auto_questions: false
      max_questions: 0
    evidence:
      - kind: field
        match: Actual
        required: true
        question: "Actual: What happened instead?"
    clarification: Please provide the missing reproduction details.
    default_agent: triagelab-investigator
    sandbox: triagelab-python
    routes: []
```

Each connection references one to eight procedures in evaluation order. The
first matching procedure wins; the last must also match its admission rules.
Procedures can be shared by connections in the same namespace and workspace.
They use the existing CEL, evidence, clarification, route, agent, and sandbox
validation. The connection's send and automatic-run settings still bound what
its procedures may do. An enabled automatic-run procedure still needs tracked,
ready evidence and the existing durable launch reservation. Applying a CR
does not submit a case, send a reply, or launch an investigation by itself.

Existing run-target authorization also applies. The chart's default setup
permits its configured default agent with `triagelab-python`. Alternate
per-procedure agents and templates still require the existing explicit
`--status-kubeconfig`, `--status-context`, and `--allow-resource-writes`
setup; this GitOps option does not enable that separate resource-editor
integration or expand its permissions.

Treat permission to change these resources and their referenced Secrets like
workspace administrator access: it can enable automatic runs and ticket
messages. The reconciler never infers those permissions from ticket content.

## Observe and update

```sh
kubectl --context YOUR-CONTEXT -n triagelab get ticketconnections
kubectl --context YOUR-CONTEXT -n triagelab describe ticketconnection github-support
```

Connections expose `status.observedGeneration`, `status.connectionID`, and a
`Ready` condition with a fixed diagnostic reason. `Ready=True` means the
configuration and its referenced procedures were applied; it does not prove
provider connectivity, kagent readiness, or a successful investigation.
Procedure validation errors are reported on each referencing connection.
Status writes use UID and resource-version preconditions; concurrent changes
retry on the next poll. Status and logs contain no credential values or
provider response bodies.

The Connections page identifies Git-managed entries and any reconciliation
error. Connection and procedure edits are rejected by the server and disabled
in the UI. Local UI-managed connections can coexist and are never adopted.
Secret rotation and procedure changes take effect on a subsequent successful
poll. Existing evidence/approval checks run again before a new launch.

## Deletion, failures, and retained state

- Removing a connection stops that connection's polling and removes its live
  workspace entry. It retains its private case state under
  `<output>/.tickets/gitops/` and leaves investigation reports intact.
- Recreating the same name reuses its source-bound state and reservations.
  Source identity fields and the workspace selector are immutable. Create a
  new connection name to change accounts or repositories. There is no
  automatic purge or conversion of UI-owned connections.
- Missing Secrets or procedures, invalid configuration, and API read failures
  stop affected managed flows; an unreadable resource list stops all managed
  flows. The workspace retries and resumes them after recovery. Local
  connections are unaffected. A status-write failure alone does not stop an
  otherwise valid flow; it is logged and retried.
- Already launched investigations keep the existing run limits and cleanup
  workflow. Removing a connection does not cancel a run or delete a sandbox.
  Reconnect the source to use its ticket delivery actions again.
- Polling is eventually consistent. Changes across CRs and Secrets are not a
  transaction; a missing intermediate reference can temporarily stop a
  connection. Allow the configured poll interval plus bounded API requests
  and any finishing ticket action. An in-flight send may finish before the
  old flow closes.

The first API is alpha, supports eight managed connections per workspace, and
refuses lists larger than 100 resources of either kind in the selected
namespace. There is no leader election, separate operator, investigation CRD,
conversion webhook, or automatic cluster discovery. Back up the reports volume:
Git reconstructs configuration, not cases, reservations, OAuth grants, reports,
or retained sandbox state.

## Run outside the chart

Pass `serve --gitops /private/path/gitops.json` with an explicit API connection:

```json
{
  "server": "https://YOUR-KUBERNETES-API",
  "namespace": "triagelab",
  "workspace": "support",
  "ca_file": "/private/path/cluster-ca.pem",
  "token_file": "/private/path/api-token",
  "poll_seconds": 30,
  "secret_names": ["github-support-token"]
}
```

Use a CA-verified HTTPS origin and a least-privilege service-account token.
Token files must be refreshed by the deployment. No ambient kubeconfig,
exec-auth plugin, insecure TLS option, or Kubernetes proxy is used. These
credentials are distinct from the kagent invocation token and ticket Secrets.
