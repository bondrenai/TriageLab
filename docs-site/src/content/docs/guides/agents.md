---
title: Defining agents
description: Edit KAgent models, MCP servers, agents, sandbox templates, and egress policies from the workspace or the CLI.
---

Open **Agents** in the workspace. The **Agents**, **Models**, **MCP servers**,
**Sandboxes**, and **Egress** tabs edit separate KAgent resources. **Save**
creates or updates the resource in the workspace namespace; each tab can also
preview and download Kubernetes JSON for deployment with your own operator
credentials.

- **Models** — name the model configuration and choose the provider and model
  ID (OpenAI-compatible, Anthropic, Gemini, Mistral, or Ollama). An optional
  endpoint overrides the provider default; for a local Ollama daemon use an
  endpoint reachable by KAgent.
- **MCP servers** — enter the server URL and transport. For authenticated
  servers, the referenced Kubernetes Secret entry contains the full
  `Authorization` header value. With live status enabled, the editor uses the
  tools KAgent discovers; otherwise enter manual tool names.
- **Agents** — choose a runtime, select a model, attach MCP servers, and check
  the exact tools to expose from each. Tool names must be unique across
  servers and distinct from built-in runtime and sandbox tools.
- **Sandboxes** — define additional sandbox templates: a digest-pinned
  workload image, the Substrate worker pool and snapshot location, literal
  environment defaults, and optional egress policy references. Saved templates
  appear in the New investigation picker. Which tools the investigator has is a
  property of the agent, not the sandbox.
- **Egress** — grant specific HTTP(S) origins as an upstream EgressPolicy.
  Sandboxes still deny every other destination, including DNS. Grants apply to
  running sandboxes without a restart; grant problems surface in deployment
  status without blocking readiness.

## Connect the workspace to KAgent

Start the workspace with an explicit, read-only kubeconfig and context to see
what KAgent has deployed in the workspace namespace:

```sh
bin/triagelab serve --config YOUR_CONFIG.json \
  --status-kubeconfig /path/to/read-only.kubeconfig --status-context YOUR_DEV_CONTEXT
```

Give the kubeconfig identity a namespace Role with `list` on
`agents`, `modelconfigs`, `remotemcpservers`, `sandboxtemplates`, and
`egresspolicies` (`api.kagent.dev`). To enable **Save**, add
`--allow-resource-writes` and a dedicated identity with `list, get, create,
update` on those resources plus `get, create` on `harnesses`.

Save writes the definition to Kubernetes and KAgent reconciles it
asynchronously. If a resource changes while you edit, reopen it before saving
again. For a custom runtime image, TriageLab creates a Harness before updating
the Agent reference; changing runtime settings later can leave an unused
Harness for the cluster operator to clean up.

## Custom runtime images

Select **Bring your own image** to use a SHA-256-pinned image with an explicit
command array. The image must implement KAgent rc3's private A2A gRPC,
readiness, and durable TaskStore contract, preserve caller authentication for
the sandbox tool server, record tool activity in the canonical transcript, and
return the investigation JSON as terminal A2A text or a canonical structured
output part. An ordinary reproduction image does not belong here — put it in
the standalone SandboxTemplate via `triagelab manifest --workload-image`.

## CLI bundle

The CLI also renders a bundled definition that creates an agent together with
its model and MCP server resources:

```sh
bin/triagelab agent-manifest --namespace kagent \
  --definition network-definition.json > network-investigator.json
kubectl --context kind-triagelab apply -f network-investigator.json
```

A definition contains a `name`, `system_prompt`, a `model` object (or
`"model_ref": "triagelab-model"` to reuse the installed model), and optional
`mcp_servers` entries with explicit tool lists. Flags and the full example are
in the [CLI reference](\/TriageLab\/reference/cli/).

## Use a saved agent

Agents saved in the workspace appear in the **New investigation** picker once
KAgent marks them ready. For CLI runs, add the agent to your configuration and
select it:

```json
{
  "manual_agents": {
    "network-investigator": ["search_docs", "lookup_reference"]
  }
}
```

```sh
./bin/triagelab investigate --config YOUR_CONFIG.json --agent network-investigator ...
```

An explicit `manual_agents` entry takes precedence over a saved tool selection
for the same name; use an empty array for an agent that only uses the six
investigation tools. Manual runs use the configured standalone sandbox and
normal limits and record the selected agent in the report.

## Review before deploying

Review images, provider destinations, and MCP servers before deploying. BYO
runtimes can receive the invocation credential; check existing model and MCP
references for caller-token passthrough. Before attaching additional MCP
servers, make sure the running KAgent deployment has
`config.runtimeDefaults.tokenPropagation.enabled: false`, as in the current
TriageLab Helm values. The trusted `triagelab-sandbox-api` server keeps
`tokenPassthrough: true`. TriageLab's watchdog checks tool names after they
appear in the upstream transcript — it cannot prevent a tool call or undo a
side effect. Verify model and tool connectivity in a manual run.
