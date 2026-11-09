import { useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Plus, X } from "lucide-react";
import { Button, Panel } from "../components/ui";
import { Field, ResourceEditor, field } from "../components/resource-editor";
import { useDeploymentStatus, type DefinitionEditor } from "../hooks/use-agents";
import { parseToolNames } from "../lib/agents";
import type { EgressDestinationDefinition, EgressPolicyDefinition, KAgentManifest, SandboxTemplateDefinition } from "../lib/types/agent";

export function SandboxTemplateEditor({ egressChoices, egressStatus, onPreview, deployment, editor }: {
  egressChoices: string[];
  egressStatus: ReturnType<typeof useDeploymentStatus>;
  onPreview: (manifest: KAgentManifest) => void;
  deployment: ReactNode;
  editor: DefinitionEditor<"sandboxes">;
}) {
  const saved = editor.initial;
  const initialEnv = saved?.env ?? [];
  const [rows, setRows] = useState(() => initialEnv.map((_, index) => index + 1));
  const nextRow = useRef(rows.length);
  const [selected, setSelected] = useState<string[]>(saved?.egress_policy_refs ?? []);
  const [manualRefs, setManualRefs] = useState((saved?.egress_policy_refs ?? []).join(", "));

  function fromForm(data: FormData): SandboxTemplateDefinition {
    return {
      name: field(data, "name").trim(),
      workload_image: field(data, "workload_image").trim(),
      worker_pool: field(data, "worker_pool").trim(),
      snapshot_location: field(data, "snapshot_location").trim(),
      env: rows.map(id => ({ name: field(data, `env-name-${id}`).trim(), value: field(data, `env-value-${id}`) }))
        .filter(variable => variable.name !== ""),
      egress_policy_refs: [...new Set([...data.getAll("egress_refs").map(String), ...parseToolNames(manualRefs)])],
    };
  }

  return <ResourceEditor resource="sandboxes" title="Sandboxes" description="Define the isolated environment an investigation runs in."
    deployment={deployment} editor={editor} onPreview={onPreview} fromForm={fromForm}
    workspaceConfig={definition => ({ template: definition.name })}
    guidance={<>
      <p>Saved templates appear in the New investigation picker once KAgent marks them ready.</p>
      <p>Sandboxes deny all network access, including DNS, unless a template references a granted <Link className="underline" to="/agents/egress-policies">egress policy</Link>.</p>
    </>}>
    <Panel title="Sandbox template"><div className="panel-body agent-fields">
      <Field label="Template name"><input name="name" defaultValue={saved?.name} readOnly={Boolean(saved)} className="field mono" placeholder="network-lab" required maxLength={253} /></Field>
      <Field label="Workload image"><input name="workload_image" defaultValue={saved?.workload_image} className="field mono" placeholder="docker.io/library/python@sha256:…" required maxLength={512} aria-describedby="sandbox-image-help" /></Field>
      <p className="field-help" id="sandbox-image-help">Pinned by SHA-256 digest. The image stays immutable for every run that uses this template.</p>
      <div className="agent-field-grid">
        <Field label="Worker pool"><input name="worker_pool" defaultValue={saved?.worker_pool ?? "kagent-default"} className="field mono" required maxLength={253} /></Field>
        <Field label="Snapshot location"><input name="snapshot_location" defaultValue={saved?.snapshot_location ?? "gs://ate-snapshots/triagelab/"} className="field mono" required maxLength={512} /></Field>
      </div>
      <p className="field-help">The worker pool places the sandbox (gVisor or microVM); snapshots go to the configured storage location.</p>
    </div></Panel>
    <Panel title="Environment defaults" aside={<span className="quiet text-meta">Optional</span>}><div className="panel-body agent-fields">
      <p className="field-help">Literal variables for the sandbox workload. Do not enter credentials: the code under investigation can read them.</p>
      {rows.map((id, index) => <div key={id} className="agent-field-grid">
        <Field label="Name"><input name={`env-name-${id}`} defaultValue={initialEnv[index]?.name} className="field mono" placeholder="MAX_RETRIES" maxLength={128} /></Field>
        <Field label="Value"><input name={`env-value-${id}`} defaultValue={initialEnv[index]?.value} className="field mono" maxLength={4096} /></Field>
        <Button type="button" variant="ghost" aria-label={`Remove environment variable ${index + 1}`} onClick={() => setRows(previous => previous.filter(row => row !== id))}><X size={15} aria-hidden="true" />Remove</Button>
      </div>)}
      <div><Button type="button" onClick={() => setRows(previous => [...previous, ++nextRow.current])} disabled={rows.length >= 100}><Plus size={16} aria-hidden="true" />Add variable</Button></div>
    </div></Panel>
    <Panel title="Network access"><div className="panel-body agent-fields">
      <p className="field-help">Every sandbox starts with no network access, including DNS. Reference egress policies to grant specific HTTPS destinations.</p>
      {egressChoices.length > 0 && <fieldset className="agent-tool-list">
        <legend className="field-label">Granted policies</legend>
        {egressChoices.map(name => <label key={name} className="agent-tool-option">
          <input type="checkbox" name="egress_refs" value={name} checked={selected.includes(name)}
            onChange={event => {
              const next = event.target.checked ? [...selected, name] : selected.filter(ref => ref !== name);
              setSelected(next);
              setManualRefs(next.join(", "));
            }} />
          <span className="mono">{name}</span>
        </label>)}
      </fieldset>}
      <Field label="Policy names (optional)"><input name="manual_egress_refs" value={manualRefs} className="field mono"
        placeholder="pypi-egress, docs-egress" maxLength={2048} onChange={event => {
          setManualRefs(event.target.value);
          setSelected(parseToolNames(event.target.value));
        }} aria-describedby="egress-refs-help" /></Field>
      <p className="field-help" id="egress-refs-help">Define policies in the <Link className="underline" to="/agents/egress-policies">Egress tab</Link>. Grant problems appear in sandbox deployment status.</p>
      {egressStatus.isError && <p className="field-help" role="status">Could not refresh saved egress policies.</p>}
    </div></Panel>
  </ResourceEditor>;
}

export function EgressPolicyEditor({ onPreview, deployment, editor }: {
  onPreview: (manifest: KAgentManifest) => void;
  deployment: ReactNode;
  editor: DefinitionEditor<"egress-policies">;
}) {
  const saved = editor.initial;
  const initialDestinations = saved?.destinations ?? [];
  const [rows, setRows] = useState(() => initialDestinations.map((_, index) => index + 1));
  const nextRow = useRef(rows.length);

  function fromForm(data: FormData): EgressPolicyDefinition {
    return {
      name: field(data, "name").trim(),
      destinations: rows.map(id => {
        const header = field(data, `header-${id}`).trim();
        const destination: EgressDestinationDefinition = { origin: field(data, `origin-${id}`).trim() };
        if (header) {
          destination.header = header;
          destination.prefix = field(data, `prefix-${id}`).trim();
          destination.secret_name = field(data, `secret-name-${id}`).trim();
          destination.secret_key = field(data, `secret-key-${id}`).trim();
        }
        return destination;
      }).filter(destination => destination.origin !== ""),
    };
  }

  return <ResourceEditor resource="egress-policies" title="Egress policies" description="Grant sandboxes additional HTTPS destinations."
    deployment={deployment} editor={editor} onPreview={onPreview} fromForm={fromForm}
    guidance={<>
      <p>Only HTTP(S) to the listed origins is allowed; every other destination, including DNS, stays denied.</p>
      <p>Reference saved policies from a <Link className="underline" to="/agents/sandboxes">sandbox template</Link>. Grants apply to running sandboxes without a restart.</p>
    </>}>
    <Panel title="Egress policy"><div className="panel-body agent-fields">
      <Field label="Policy name"><input name="name" defaultValue={saved?.name} readOnly={Boolean(saved)} className="field mono" placeholder="pypi-egress" required maxLength={253} /></Field>
      <p className="field-help">Origins are a scheme, host, and optional port. A wildcard replaces the leftmost label only, for example https://*.example.com.</p>
      {rows.map((id, index) => <fieldset key={id} className="agent-inset agent-fields">
        <legend className="field-label">Destination {index + 1}</legend>
        <Field label="Origin"><input name={`origin-${id}`} defaultValue={initialDestinations[index]?.origin} className="field mono" placeholder="https://pypi.org" required maxLength={2048} /></Field>
        <div className="agent-field-grid">
          <Field label="Auth header (optional)"><input name={`header-${id}`} defaultValue={initialDestinations[index]?.header} className="field mono" placeholder="Authorization" maxLength={128} /></Field>
          <Field label="Value prefix (optional)"><input name={`prefix-${id}`} defaultValue={initialDestinations[index]?.prefix} className="field mono" placeholder="Bearer " maxLength={256} /></Field>
        </div>
        <div className="agent-field-grid">
          <Field label="Secret name (optional)"><input name={`secret-name-${id}`} defaultValue={initialDestinations[index]?.secret_name} className="field mono" maxLength={253} /></Field>
          <Field label="Secret key (optional)"><input name={`secret-key-${id}`} defaultValue={initialDestinations[index]?.secret_key} className="field mono" maxLength={253} /></Field>
        </div>
        <p className="field-help">The gateway injects the header from the Secret; its value never reaches the sandbox.</p>
        <div><Button type="button" variant="ghost" aria-label={`Remove destination ${index + 1}`} onClick={() => setRows(previous => previous.filter(row => row !== id))}><X size={15} aria-hidden="true" />Remove destination</Button></div>
      </fieldset>)}
      <div><Button type="button" onClick={() => setRows(previous => [...previous, ++nextRow.current])} disabled={rows.length >= 64}><Plus size={16} aria-hidden="true" />Add destination</Button></div>
    </div></Panel>
  </ResourceEditor>;
}
