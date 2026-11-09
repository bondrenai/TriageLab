import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link, NavLink, Navigate, useParams } from "react-router-dom";
import { Plus, X } from "lucide-react";
import { Button, Panel, Textarea } from "../components/ui";
import { Field, ResourceEditor, field } from "../components/resource-editor";
import { DeploymentPanel } from "../components/deployment-status";
import { useDefinitionEditor, useDeploymentStatus, type DefinitionEditor } from "../hooks/use-agents";
import { useSettings } from "../hooks/use-investigations";
import { MCPServerEditor, ModelEditor } from "./AgentDependencies";
import { EgressPolicyEditor, SandboxTemplateEditor } from "./SandboxTemplates";
import { parseToolNames } from "../lib/agents";
import type { AgentDefinition, Deployment, DiscoveredTool, KAgentManifest } from "../lib/types/agent";

type ResourceChoice = NonNullable<KAgentManifest["metadata"]> & { tools?: string[] };

function loadResourceChoices(storageKey: string): ResourceChoice[] {
  try {
    const choices: unknown = JSON.parse(localStorage.getItem(storageKey) ?? "[]");
    if (Array.isArray(choices)) {
      return choices.filter(choice => typeof choice?.name === "string" && typeof choice?.namespace === "string")
        .slice(0, 100)
        .map(({ name, namespace, tools }) => ({
          name, namespace, tools: Array.isArray(tools) ? tools.filter(tool => typeof tool === "string") : undefined,
        }));
    }
  } catch { /* Suggestions are optional when storage is unavailable. */ }
  return [];
}

function usePreviewedResources(storageKey: string, kind: string, namespace?: string) {
  const [choices, setChoices] = useState(() => loadResourceChoices(storageKey));
  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(choices));
    } catch { /* Suggestions still work while this editor is open. */ }
  }, [choices, storageKey]);

  function remember(manifest: KAgentManifest, tools?: string[]) {
    const resource = manifest.metadata;
    if (manifest.kind !== kind || !resource) return;
    setChoices(previous => [
      { ...resource, tools },
      ...previous.filter(choice => choice.namespace !== resource.namespace || choice.name !== resource.name),
    ].slice(0, 100));
  }

  return { choices: choices.filter(choice => choice.namespace === namespace), remember };
}

export function AgentsPage() {
  const { "*": path } = useParams();
  const resource = path?.replace(/\/$/, "") || "";
  const settings = useSettings().data;
  const namespace = settings?.namespace;
  const writable = settings?.resource_writes === true;
  const agents = usePreviewedResources("triagelab.previewed-agents", "Agent", namespace);
  const models = usePreviewedResources("triagelab.previewed-models", "ModelConfig", namespace);
  const mcpServers = usePreviewedResources("triagelab.previewed-mcp-servers", "RemoteMCPServer", namespace);
  const sandboxes = usePreviewedResources("triagelab.previewed-sandboxes", "SandboxTemplate", namespace);
  const egressPolicies = usePreviewedResources("triagelab.previewed-egress-policies", "EgressPolicy", namespace);
  const agentStatus = useDeploymentStatus("agents", resource === "");
  const modelStatus = useDeploymentStatus("models", resource === "models" || (resource === "" && writable));
  const mcpStatus = useDeploymentStatus("mcp-servers", resource === "" || resource === "mcp-servers");
  const sandboxStatus = useDeploymentStatus("sandboxes", resource === "sandboxes");
  const egressStatus = useDeploymentStatus("egress-policies", resource === "sandboxes" || resource === "egress-policies");
  const agentEditor = useDefinitionEditor("agents");
  const modelEditor = useDefinitionEditor("models");
  const mcpEditor = useDefinitionEditor("mcp-servers");
  const sandboxEditor = useDefinitionEditor("sandboxes");
  const egressEditor = useDefinitionEditor("egress-policies");

  const modelNames = [...new Set([
    ...models.choices.map(model => model.name),
    ...(modelStatus.data?.resources ?? []).filter(item => item.managed).map(item => item.name),
    ...(agentEditor.initial?.model_ref ? [agentEditor.initial.model_ref] : []),
  ])];
  const serverChoices = new Map(mcpServers.choices.map(item => [item.name, item]));
  if (namespace) {
    for (const item of mcpStatus.data?.resources ?? []) {
      if (item.managed) serverChoices.set(item.name, { name: item.name, namespace, tools: item.manual_tools });
    }
    for (const server of agentEditor.initial?.mcp_servers ?? []) {
      if (!serverChoices.has(server.name)) serverChoices.set(server.name, { name: server.name, namespace });
    }
  }

  const egressChoices = [...new Set([
    ...egressPolicies.choices.map(policy => policy.name),
    ...(egressStatus.data?.resources ?? []).filter(item => item.state === "ready").map(item => item.name),
  ])];

  if (resource !== "" && resource !== "models" && resource !== "mcp-servers" && resource !== "sandboxes" && resource !== "egress-policies") {
    return <Navigate to="/agents" replace />;
  }

  return <>
    <nav className="resource-nav" aria-label="Agent resources">
      <NavLink to="/agents" end>Agents</NavLink>
      <NavLink to="/agents/models" end>Models</NavLink>
      <NavLink to="/agents/mcp-servers" end>MCP servers</NavLink>
      <NavLink to="/agents/sandboxes" end>Sandboxes</NavLink>
      <NavLink to="/agents/egress-policies" end>Egress</NavLink>
    </nav>
    <div hidden={resource !== ""}><AgentEditor key={agentEditor.key} editor={agentEditor} modelNames={modelNames} mcpServers={[...serverChoices.values()]}
      onPreview={agents.remember} mcpStatus={mcpStatus}
      deployment={<DeploymentPanel resource="agents" status={agentStatus} previewed={agents.choices.map(agent => agent.name)}
        onEdit={writable ? agentEditor.load.mutate : undefined} editing={agentEditor.load.isPending || agentEditor.save.isPending} />} /></div>
    <div hidden={resource !== "models"}><ModelEditor key={modelEditor.key} editor={modelEditor} onPreview={manifest => models.remember(manifest)}
      deployment={<DeploymentPanel resource="models" status={modelStatus} previewed={models.choices.map(model => model.name)}
        onEdit={writable ? modelEditor.load.mutate : undefined} editing={modelEditor.load.isPending || modelEditor.save.isPending} />} /></div>
    <div hidden={resource !== "mcp-servers"}><MCPServerEditor key={mcpEditor.key} editor={mcpEditor} onPreview={mcpServers.remember}
      deployment={<DeploymentPanel resource="mcp-servers" status={mcpStatus} previewed={mcpServers.choices.map(server => server.name)}
        onEdit={writable ? mcpEditor.load.mutate : undefined} editing={mcpEditor.load.isPending || mcpEditor.save.isPending} />} /></div>
    <div hidden={resource !== "sandboxes"}><SandboxTemplateEditor key={sandboxEditor.key} editor={sandboxEditor} onPreview={sandboxes.remember}
      egressChoices={egressChoices} egressStatus={egressStatus}
      deployment={<DeploymentPanel resource="sandboxes" status={sandboxStatus} previewed={sandboxes.choices.map(sandbox => sandbox.name)}
        onEdit={writable ? sandboxEditor.load.mutate : undefined} editing={sandboxEditor.load.isPending || sandboxEditor.save.isPending} />} /></div>
    <div hidden={resource !== "egress-policies"}><EgressPolicyEditor key={egressEditor.key} editor={egressEditor} onPreview={egressPolicies.remember}
      deployment={<DeploymentPanel resource="egress-policies" status={egressStatus} previewed={egressPolicies.choices.map(policy => policy.name)}
        onEdit={writable ? egressEditor.load.mutate : undefined} editing={egressEditor.load.isPending || egressEditor.save.isPending} />} /></div>
  </>;
}

function AgentEditor({ modelNames, mcpServers, onPreview, mcpStatus, deployment, editor }: {
  modelNames: string[];
  mcpServers: ResourceChoice[];
  onPreview: (manifest: KAgentManifest) => void;
  mcpStatus: ReturnType<typeof useDeploymentStatus>;
  deployment: ReactNode;
  editor: DefinitionEditor<"agents">;
}) {
  const saved = editor.initial;
  const initialServers = saved?.mcp_servers ?? [];
  const [runtime, setRuntime] = useState(saved?.runtime ? "image" : "declarative");
  const [servers, setServers] = useState(() => initialServers.map((_, index) => index + 1));
  const nextServer = useRef(servers.length);
  const modelHelpId = useId();
  const byImage = runtime === "image";

  function fromForm(data: FormData): AgentDefinition {
    const args = field(data, "args");
    return {
      name: field(data, "name").trim(),
      description: field(data, "description").trim(),
      system_prompt: field(data, "system_prompt"),
      model_ref: field(data, "model_ref").trim(),
      runtime: byImage ? {
        image: field(data, "image").trim(), command: [field(data, "command").trim(), ...(saved?.runtime?.command.slice(1) ?? [])],
        args: args ? args.replace(/\n$/, "").split("\n") : [],
      } : undefined,
      mcp_servers: servers.map(id => ({
        name: field(data, `server-${id}`).trim(),
        tools: [...data.getAll(`tools-${id}`).map(String), ...parseToolNames(field(data, `manual-tools-${id}`))],
      })),
    };
  }

  return <ResourceEditor resource="agents" title="Agents" description="Define the investigator and choose its model and tools." fromForm={fromForm}
    deployment={deployment} editor={editor} onPreview={manifest => {
      const agent = manifest.items?.find(item => item.kind === "Agent");
      if (agent) onPreview(agent);
    }}
    workspaceConfig={definition => ({ manual_agents: { [definition.name]: (definition.mcp_servers ?? []).flatMap(server => server.tools) } })}
    guidance={<>
      <p>Save models and MCP servers in their respective tabs before saving an agent that uses them.</p>
      <p>Saved agents appear in the New investigation picker and can run once KAgent marks them ready.</p>
    </>}>
    <Panel title="Agent definition"><div className="panel-body agent-fields">
      <div className="agent-field-grid">
        <Field label="Agent name"><input name="name" defaultValue={saved?.name} readOnly={Boolean(saved)} className="field mono" placeholder="network-investigator" required maxLength={48} /></Field>
        <Field label="Runtime"><select className="field" value={runtime} onChange={e => setRuntime(e.target.value)}>
          <option value="declarative">Declarative (Go ADK)</option><option value="image">Bring your own image</option>
        </select></Field>
      </div>
      <div className="agent-field-grid">
        <div className="agent-fields">
          <Field label={byImage ? "Model resource name (optional)" : "Model resource name"}>
            <select name="model_ref" className="field mono" defaultValue={saved?.model_ref ?? ""} aria-describedby={modelHelpId} required={!byImage}>
              <option value="" disabled={!byImage}>
                {byImage ? "Use the image’s model" : modelNames.length ? "Choose a model" : "No models defined"}
              </option>
              {modelNames.map(name => <option key={name} value={name}>{name}</option>)}
            </select>
          </Field>
          <p className="field-help" id={modelHelpId}>
            Define and preview models in the <Link to="/agents/models" className="underline">Models tab</Link> to add them here.
          </p>
        </div>
        <Field label="Description (optional)"><input name="description" defaultValue={saved?.description} className="field" maxLength={1024} /></Field>
      </div>
      {byImage && <div className="agent-fields agent-inset">
        <p className="field-help">Use a trusted image that implements KAgent’s private A2A runtime contract and emits TriageLab’s report format.</p>
        <Field label="Agent image"><input name="image" defaultValue={saved?.runtime?.image} className="field mono" placeholder="registry.example/agent@sha256:…" required maxLength={512} /></Field>
        <Field label="Executable"><input name="command" defaultValue={saved?.runtime?.command[0]} className="field mono" placeholder="/app/agent" required maxLength={4096} /></Field>
        <Field label="Arguments (one per line)"><Textarea name="args" defaultValue={saved?.runtime?.args?.join("\n")} rows={3} maxLength={32768} /></Field>
      </div>}
      <Field label="Investigation instructions (optional)"><Textarea name="system_prompt" defaultValue={saved?.system_prompt} rows={4} maxLength={32768} aria-describedby="agent-prompt-help" /></Field>
      <p className="field-help" id="agent-prompt-help">These instructions extend TriageLab’s sandbox and evidence instructions.</p>
    </div></Panel>
    <Panel title="MCP tool attachments"><div className="panel-body agent-fields">
      <p className="field-help">Sandbox tools are included. Select a server and choose the exact tools to expose.</p>
      {mcpServers.length === 0 && <p className="field-help">
        Define and preview a server in the <Link to="/agents/mcp-servers" className="underline">MCP servers tab</Link> to attach it here.
      </p>}
      {servers.map((id, index) => <MCPAttachment key={id} id={id} index={index} servers={mcpServers}
        initial={initialServers[id - 1]}
        deployments={mcpStatus.data?.resources ?? []} statusUnavailable={mcpStatus.isError}
        remove={() => setServers(previous => previous.filter(server => server !== id))} />)}
      <div><Button type="button" onClick={() => {
        const id = ++nextServer.current;
        setServers(previous => [...previous, id]);
      }} disabled={mcpServers.length === 0 || servers.length >= 12}><Plus size={16} aria-hidden="true" />Attach MCP server</Button></div>
    </div></Panel>
  </ResourceEditor>;
}

function MCPAttachment({ id, index, servers, deployments, statusUnavailable, initial, remove }: {
  id: number;
  index: number;
  servers: ResourceChoice[];
  deployments: Deployment[];
  statusUnavailable: boolean;
  initial?: { name: string; tools: string[] };
  remove: () => void;
}) {
  const [serverName, setServerName] = useState(initial?.name ?? "");
  const [selected, setSelected] = useState(initial?.tools ?? []);
  const [manualInput, setManualInput] = useState(initial?.tools.join(", ") ?? "");
  const discovered = deployments.find(server => server.name === serverName)?.tools;
  const manual = servers.find(server => server.name === serverName)?.tools;
  const catalog: DiscoveredTool[] | undefined = discovered ?? (manual?.length ? manual.map(name => ({ name, description: "" })) : undefined);
  const tools: DiscoveredTool[] | undefined = catalog && [...catalog, ...[...new Set(selected)].filter(name => !catalog.some(tool => tool.name === name))
    .map(name => ({ name, description: "Previously selected. This tool is missing from the current list." }))];
  const toolField = `tools-${id}`;

  return <fieldset className="agent-inset agent-fields">
    <legend className="field-label">Attachment {index + 1}</legend>
    <Field label="MCP server name">
      <select name={`server-${id}`} className="field mono" value={serverName} onChange={e => {
        setServerName(e.target.value);
        setSelected([]);
        setManualInput("");
      }} required>
        <option value="" disabled>Choose a server</option>
        {servers.map(server => <option key={server.name} value={server.name}>{server.name}</option>)}
      </select>
    </Field>
    {serverName && (tools?.length ? <fieldset key={serverName} className="agent-tool-list">
      <legend className="field-label">Tools to expose</legend>
      <p className="field-help">{discovered ? statusUnavailable ? "Last tools discovered by KAgent. Live status is unavailable." : "Discovered by KAgent." : "Tool names saved on the MCP servers tab."}</p>
      {tools.map(tool => <label key={tool.name} className="agent-tool-option">
        <input type="checkbox" name={toolField} value={tool.name} disabled={Boolean(tool.selection_error)} checked={selected.includes(tool.name)}
          onChange={e => {
            const next = e.target.checked ? [...selected, tool.name] : selected.filter(name => name !== tool.name);
            setSelected(next);
            setManualInput(next.join(", "));
          }} />
        <span><span className="mono">{tool.name}</span>
          {tool.description && <span className="agent-tool-description field-help">{tool.description}</span>}
          {tool.selection_error && <span className="agent-tool-description field-help">{tool.selection_error}</span>}
        </span>
      </label>)}
    </fieldset> : discovered ? <p className="field-help">KAgent discovered no tools on this server.</p> : <Field label="Tool names">
      <input key={serverName} name={`manual-tools-${id}`} value={manualInput} onChange={e => {
        setManualInput(e.target.value);
        setSelected(parseToolNames(e.target.value));
      }} className="field mono" placeholder="search_docs, read_document" required maxLength={3300} />
    </Field>)}
    <div><Button type="button" variant="ghost" aria-label={`Remove attachment ${index + 1}`} onClick={remove}>
      <X size={15} aria-hidden="true" />Remove attachment
    </Button></div>
  </fieldset>;
}
