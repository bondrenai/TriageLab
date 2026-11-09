# Define an investigation agent

Open **Agents** in the workspace. The **Agents**, **Models**, **MCP servers**,
**Sandboxes**, and **Egress** tabs edit separate KAgent resources. **Save**
creates or updates the resource in the workspace namespace. Use **Edit** in
Deployment status to reopen a resource saved by TriageLab. Each tab can also
preview and download Kubernetes JSON.

- In **Models**, give the model configuration a resource name and choose the
  provider and model ID. The editor supports OpenAI-compatible, Anthropic,
  Gemini, Mistral, and Ollama models. An optional endpoint overrides the
  provider default. For a local Ollama daemon, use an endpoint reachable by
  KAgent. For Ollama Cloud, select a cloud model, reference its Secret, and leave
  the endpoint blank.
- In **MCP servers**, give the server a resource name and enter its URL and
  transport. For authenticated servers, the selected Kubernetes Secret entry
  contains the full Authorization header value. Create credential values in
  Kubernetes and provide their Secret references in the editor. With live status
  enabled, the editor uses the tools KAgent discovers for the deployed server.
  Enter manual tool names for servers whose discovery is unavailable.
- In **Agents**, choose a runtime and select a model saved or previewed in
  **Models**. Select servers saved or previewed in **MCP servers** and check the tools to expose
  from each server. If discovery is unavailable and no manual names have been
  saved, enter the exact tool names on the attachment.
  The browser remembers previewed model and server names and available tools
  for the workspace namespace. Tool names must be unique across servers and
  distinct from built-in runtime and sandbox tools.
- In **Sandboxes**, define an additional sandbox template: a digest-pinned
  workload image, the Substrate worker pool and snapshot location, literal
  environment defaults, and optional egress policy references. Saved templates
  appear in the New investigation picker (with resource writes enabled); the
  workspace default template stays managed by the installation manifest. Which
  tools the investigator has is a property of the agent, not the sandbox —
  edit tool sets in **Agents**.
- In **Egress**, grant specific HTTP(S) origins as an upstream EgressPolicy.
  Sandboxes still deny every other destination, including DNS. Reference saved
  policies from a sandbox template; grants apply to running sandboxes without a
  restart, and grant problems (a missing or conflicting policy) appear in
  sandbox deployment status without blocking readiness.

Declarative agents use the Go ADK Harness from the TriageLab installation.
TriageLab adds the investigation instructions, the six standalone sandbox tools,
and the structured report schema. The selected model needs to support tool calls
and structured responses.

## Connect the workspace to KAgent

Start the workspace with an explicit kubeconfig and context to see what KAgent
has deployed in the workspace namespace:

```sh
bin/triagelab serve --config YOUR_CONFIG.json \
  --status-kubeconfig /path/to/read-only.kubeconfig --status-context YOUR_DEV_CONTEXT
```

Use `kubectl` on the workspace host. Give the kubeconfig identity a namespace
Role and RoleBinding with this rule in the workspace namespace:

```yaml
rules:
  - apiGroups: [api.kagent.dev]
    resources: [agents, modelconfigs, remotemcpservers, sandboxtemplates, egresspolicies]
    verbs: [list]
```

The workspace refreshes status while the tab is open. The Agents tab shows
runtime readiness, the Models tab shows whether configuration and references
were accepted, and the MCP servers tab shows discovery status and available
tools. Status applies to the deployed resource, so a new preview with the same
name can differ from what is running. An accepted model still needs a successful
invocation to verify provider connectivity. Agents continue to use explicit tool
selections, and refreshing a catalog never checks a tool automatically.

To enable **Save**, add `--allow-resource-writes` and use a dedicated kubeconfig
identity bound to this namespace Role instead of the read-only role above:

```yaml
rules:
  - apiGroups: [api.kagent.dev]
    resources: [agents, modelconfigs, remotemcpservers, sandboxtemplates, egresspolicies]
    verbs: [list, get, create, update]
  - apiGroups: [api.kagent.dev]
    resources: [harnesses]
    verbs: [get, create]
```

Save writes the definition to Kubernetes and KAgent reconciles it asynchronously.
TriageLab preserves the editable definition on the resource. If it changes while
you edit, reopen it before saving again. Resources created outside TriageLab
remain visible in Deployment status. For a custom image, TriageLab creates a
Harness before updating the Agent reference. Changing runtime settings can leave
an unused Harness for the cluster operator to clean up.

## CLI bundle

The CLI also accepts a bundled definition that creates an agent and its model
and MCP server resources together.

Save this example as `network-definition.json`.

```json
{
  "name": "network-investigator",
  "description": "Investigates network configuration failures",
  "system_prompt": "Check DNS and proxy configuration before testing retries.",
  "model": {
    "provider": "OpenAI",
    "name": "your-model-id",
    "endpoint": "https://models.example/v1",
    "secret_name": "model-access",
    "secret_key": "api-key"
  },
  "mcp_servers": [
    { "name": "engineering-docs", "tools": ["search_docs"] },
    {
      "name": "reference",
      "url": "https://reference.example/mcp",
      "tools": ["lookup_reference"]
    }
  ]
}
```

The `engineering-docs` entry references an existing `RemoteMCPServer`. The
second entry generates `network-investigator-mcp-reference`. Use a short DNS
label for the suffix and reserve `mcp-` at the start and `-mcp-` within it. Use `"model_ref":
"triagelab-model"` in place of the `model` object to reuse the installed model.

## Custom runtime images

Select **Bring your own image** or add a `runtime` object with `image`, a
`command` array, and optional `args`. The image must be pinned by SHA-256 and
the command must identify its executable. This creates a KAgent BYO Harness
using the development install's `kagent-default` WorkerPool and snapshot policy.
A model configuration is optional when the image handles model setup.

The image must implement KAgent rc3's private A2A gRPC, readiness, and durable
TaskStore contract. It must consume the supplied configuration for MCP bindings,
preserve caller authentication for the sandbox tool server, and record tool
activity in the canonical transcript. Return the investigation JSON described
in the generated prompt as terminal A2A text or as a KAgent canonical structured
output part. TriageLab validates the report and fetches process state, exit
status and output independently. The recorded command and arguments come from
the runtime's transcript. An ordinary reproduction image belongs in the
standalone SandboxTemplate, which can be set with
`triagelab manifest --workload-image`.

- [Pinned KAgent BYO runtime contract](https://github.com/kagent-dev/kagent/blob/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/docs/architecture/configuration-and-compilation.md#runtime-configuration)
- [Canonical tool activity and structured output parts](https://github.com/kagent-dev/kagent/tree/3548b7aaf4d91ec86b0565df2bd8a9950e9a276d/go/api/a2a)

## Deploy and use

The editor and CLI render configuration offline. Deploy the reviewed manifests
with your Kubernetes operator credentials in the same namespace as the
TriageLab installation. Apply any new model and server manifests before the
agent that references them.

Before attaching additional MCP servers, make sure the running KAgent deployment
has `config.runtimeDefaults.tokenPropagation.enabled: false`, as in the current
TriageLab Helm values. Earlier development installs enabled global propagation,
which sends the invocation bearer and caller ID to every MCP server. Update the
controller configuration and let it reconcile the agents before running a
custom agent. The trusted `triagelab-sandbox-api` server keeps
`tokenPassthrough: true`. Use a verified invocation service-account token so
KAgent can authenticate the same sandbox owner when that server receives a call.

Render and deploy the CLI definition on your development environment.

```sh
bin/triagelab agent-manifest --namespace kagent \
  --definition network-definition.json > network-investigator.json
kubectl --context kind-triagelab apply --dry-run=server -f network-investigator.json
kubectl --context kind-triagelab apply -f network-investigator.json
kubectl --context kind-triagelab -n kagent wait --for=condition=Ready \
  agent/network-investigator --timeout=5m
```

Agents saved in the workspace appear in the **New investigation** picker. You
can select them once KAgent marks them ready. The workspace reads the saved
tool selection when starting a run.

For CLI runs and agents deployed from a downloaded manifest, add an entry to
your TriageLab JSON configuration. The workspace preview generates this entry
from your tool selection. Use an empty array for an agent that only uses the six
investigation tools. An explicit configuration entry takes precedence over the
saved tool selection if both exist for the same name.

```json
{
  "manual_agents": {
    "network-investigator": ["search_docs", "lookup_reference"]
  }
}
```

Restart `triagelab serve --config YOUR_CONFIG.json` and select the agent under
**Investigator** on **New investigation**. For the CLI, pass
`--config YOUR_CONFIG.json --agent network-investigator` to
`triagelab investigate`. Manual runs use the configured standalone sandbox
and normal limits, and record the selected agent in the report. If you enable
ticket cases, keep the workspace's default agent separate from
`manual_agents`. Use **Edit procedure** in Ticket cases to select a saved agent
as the default for that procedure or route to it by tag or label. The approval
includes that agent's current saved tool selection.

Review images, provider destinations, and MCP servers before deploying. BYO
runtimes can receive the invocation credential. Review existing model and MCP
references for caller-token passthrough. Additional tools should be read-only
and intended for use with the data in the workspace. TriageLab's watchdog
checks tool names after they appear in the upstream transcript. This check
cannot prevent a tool call or undo a side effect. KAgent compiles references and prepares the runtime snapshot before marking the
Agent ready. Verify model and tool connectivity in a manual run. See [TriageLab’s security model](../SECURITY.md).
