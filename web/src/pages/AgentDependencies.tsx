import { useState, type ReactNode } from "react";
import { Panel } from "../components/ui";
import { Field, ResourceEditor, SecretFields, field } from "../components/resource-editor";
import { parseToolNames } from "../lib/agents";
import type { DefinitionEditor } from "../hooks/use-agents";
import type { KAgentManifest } from "../lib/types/agent";

const providers = [
  ["OpenAI", "OpenAI or compatible gateway"], ["Anthropic", "Anthropic"],
  ["Gemini", "Google Gemini"], ["Mistral", "Mistral"], ["Ollama", "Ollama"],
];

export function ModelEditor({ onPreview, deployment, editor }: {
  onPreview: (manifest: KAgentManifest) => void; deployment: ReactNode; editor: DefinitionEditor<"models">;
}) {
  const model = editor.initial?.model;
  const [provider, setProvider] = useState(model?.provider ?? "OpenAI");

  return <ResourceEditor resource="models" title="Models" description="Define a reusable model and its provider." onPreview={onPreview} deployment={deployment} editor={editor} fromForm={data => ({
    name: field(data, "name").trim(),
    model: {
      provider, name: field(data, "model_id").trim(), endpoint: field(data, "endpoint").trim(),
      secret_name: field(data, "secret_name").trim(), secret_key: field(data, "secret_key").trim(),
    },
  })} guidance={<p>Save this model to use it in an agent. Preview manifest exports a definition you can deploy separately.</p>}>
    <Panel title="Model definition"><div className="panel-body agent-fields">
      <Field label="Model resource name"><input name="name" defaultValue={editor.initial?.name} readOnly={Boolean(editor.initial)} className="field mono" placeholder="incident-model" required maxLength={253} /></Field>
      <div className="agent-field-grid">
        <Field label="Provider"><select className="field" value={provider} onChange={e => setProvider(e.target.value)}>
          {providers.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select></Field>
        <Field label="Model ID"><input name="model_id" defaultValue={model?.name} className="field mono" required maxLength={256} /></Field>
      </div>
      <Field label="Provider endpoint (optional)"><input name="endpoint" defaultValue={model?.endpoint} className="field mono" type="url" placeholder="https://models.example/v1" maxLength={2048} /></Field>
      <SecretFields name={model?.secret_name} secretKey={model?.secret_key} />
      <p className="field-help">{provider === "Ollama"
        ? "Enter the daemon endpoint, or leave it blank and reference a Secret for an Ollama Cloud model."
        : "For authenticated providers, enter the name and key of an existing Kubernetes Secret."}</p>
    </div></Panel>
  </ResourceEditor>;
}

export function MCPServerEditor({ onPreview, deployment, editor }: {
  onPreview: (manifest: KAgentManifest, tools: string[]) => void; deployment: ReactNode; editor: DefinitionEditor<"mcp-servers">;
}) {
  const saved = editor.initial;
  return <ResourceEditor resource="mcp-servers" title="MCP servers" description="Define a remote server that agents can share."
    deployment={deployment} editor={editor}
    onPreview={(manifest, data) => onPreview(manifest, [...new Set(parseToolNames(field(data, "tool_catalog")))])} fromForm={data => ({
    name: field(data, "name").trim(), url: field(data, "url").trim(), protocol: field(data, "protocol"),
    secret_name: field(data, "secret_name").trim(), secret_key: field(data, "secret_key").trim(),
    manual_tools: [...new Set(parseToolNames(field(data, "tool_catalog")))],
  })} guidance={<p>Save this server to make it available to agents. Each agent selects the tools it can use. Set KAgent’s global runtime token propagation to off before deploying additional servers.</p>}>
    <Panel title="Server definition"><div className="panel-body agent-fields">
      <Field label="MCP server name"><input name="name" defaultValue={saved?.name} readOnly={Boolean(saved)} className="field mono" placeholder="engineering-docs" required maxLength={253} /></Field>
      <div className="agent-field-grid">
        <Field label="Server URL"><input name="url" defaultValue={saved?.url} className="field mono" type="url" placeholder="https://tools.example/mcp" required maxLength={2048} /></Field>
        <Field label="Transport"><select name="protocol" defaultValue={saved?.protocol ?? "STREAMABLE_HTTP"} className="field"><option value="STREAMABLE_HTTP">Streamable HTTP</option><option value="SSE">SSE</option></select></Field>
      </div>
      <SecretFields name={saved?.secret_name} secretKey={saved?.secret_key} />
      <p className="field-help">For authenticated servers, the Secret entry supplies the full Authorization header value.</p>
      <Field label="Manual tool names (optional)">
        <input name="tool_catalog" defaultValue={saved?.manual_tools?.join(", ")} className="field mono" placeholder="search_docs, read_document" maxLength={3300} />
      </Field>
      <p className="field-help">KAgent discovers tools after deployment. Enter tool names here for agents to pick from when discovery is unavailable.</p>
    </div></Panel>
  </ResourceEditor>;
}
