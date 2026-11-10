import { cloneElement, useId, useRef, type FormEvent, type ReactElement, type ReactNode } from "react";
import { ArrowDownToLine, Braces, Save } from "lucide-react";
import { Button, ErrorPlate, PageTitle, Panel } from "./ui";
import { useManifest, type DefinitionEditor } from "../hooks/use-agents";
import { useSettings } from "../hooks/use-investigations";
import type { kagentManifest, ManifestDefinitions } from "../lib/types/agent";

export function Field({ label, children }: { label: string; children: ReactElement<{ id?: string }> }) {
  const id = useId();
  return <div className="agent-field">
    <label className="field-label" htmlFor={id}>{label}</label>
    {cloneElement(children, { id })}
  </div>;
}

export function SecretFields({ name, secretKey }: { name?: string; secretKey?: string }) {
  return <div className="agent-field-grid">
    <Field label="Secret name (optional)"><input name="secret_name" defaultValue={name} className="field mono" maxLength={253} /></Field>
    <Field label="Secret key (optional)"><input name="secret_key" defaultValue={secretKey} className="field mono" maxLength={253} /></Field>
  </div>;
}

export function field(data: FormData, name: string) {
  return String(data.get(name) ?? "");
}

interface ResourceEditorProps<K extends keyof ManifestDefinitions> {
  resource: K;
  title: string;
  description: string;
  fromForm: (data: FormData) => ManifestDefinitions[K];
  children: ReactNode;
  guidance: ReactNode;
  deployment: ReactNode;
  editor: DefinitionEditor<K>;
  onPreview?: (manifest: kagentManifest, data: FormData) => void;
  workspaceConfig?: (definition: ManifestDefinitions[K]) => Record<string, unknown>;
}

export function ResourceEditor<K extends keyof ManifestDefinitions>({
  resource, title, description, fromForm, children, guidance, deployment, editor, onPreview, workspaceConfig,
}: ResourceEditorProps<K>) {
  const settings = useSettings();
  const render = useManifest(resource);
  const preview = useRef<HTMLElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const output = render.data;
  const definition = output ? render.variables : undefined;
  const filename = definition ? definition.name + ".json" : "";
  const formID = `resource-definition-${resource}`;
  const writable = settings.data?.resource_writes === true;
  const busy = render.isPending || editor.save.isPending || editor.load.isPending;

  async function saveDefinition(input: ManifestDefinitions[K]) {
    try {
      await editor.save.mutateAsync(input);
      render.reset();
    } catch { /* The save error is shown with the editor. */ }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const input = fromForm(data);
    const submitter = (event.nativeEvent as SubmitEvent).submitter as HTMLButtonElement | null;
    if (submitter?.value === "save" || (!submitter && writable)) {
      if (writable) await saveDefinition(input);
      return;
    }
    try {
      const manifest = await render.mutateAsync(input);
      onPreview?.(manifest, data);
      requestAnimationFrame(() => preview.current?.focus());
    } catch { /* The validation message is shown with the form. */ }
  }

  function download() {
    const file = new Blob([JSON.stringify(output, null, 2) + "\n"], { type: "application/json" });
    const url = URL.createObjectURL(file);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  return <>
    <PageTitle title={title} description={description} action={output ? <div className="button-row resource-actions">
      <Button disabled={busy} onClick={() => {
        render.reset();
        requestAnimationFrame(() => form.current?.focus());
      }}>Back to editor</Button>
      <Button variant={writable ? "secondary" : "primary"} onClick={download}><ArrowDownToLine size={15} aria-hidden="true" />Download JSON</Button>
      {writable && <Button variant="primary" disabled={busy || !definition} onClick={() => definition && void saveDefinition(definition)}>
        <Save size={16} aria-hidden="true" />{editor.save.isPending ? "Saving…" : "Save"}
      </Button>}
    </div> : <div className="button-row resource-actions">
      {editor.initial && <Button type="button" disabled={busy} onClick={editor.createNew}>New definition</Button>}
      <Button form={formID} type="submit" value="preview" variant={writable ? "secondary" : "primary"} disabled={busy}>
        <Braces size={16} aria-hidden="true" />{render.isPending ? "Rendering…" : "Preview manifest"}
      </Button>
      <Button form={formID} type="submit" value="save" variant={writable ? "primary" : "secondary"} disabled={!writable || busy}>
        <Save size={16} aria-hidden="true" />{editor.save.isPending ? "Saving…" : "Save"}
      </Button>
    </div>} />
    <div className="intake-layout">
      <div>
        {editor.save.isError && <ErrorPlate title="Definition could not be saved" error={editor.save.error} />}
        {editor.load.isError && <ErrorPlate title="Definition could not be opened" error={editor.load.error} />}
        <form id={formID} hidden={Boolean(output)} ref={form} tabIndex={-1} onSubmit={submit} onChange={() => { render.reset(); editor.save.reset(); }}>
          <fieldset className="section-stack agent-form" disabled={busy}>
            {editor.save.isSuccess && <p role="status" className="field-help">Saved to kagent. Deployment status updates as kagent reconciles the change.</p>}
            {render.isError && <ErrorPlate title="Manifest could not be rendered" error={render.error} />}
            {children}
          </fieldset>
        </form>
        {output && definition && <section aria-label="Manifest preview" tabIndex={-1} ref={preview}>
          <Panel title="Manifest preview" aside={<span className="quiet text-meta">{output.items?.map(item => item.kind).join(" / ") ?? output.kind}</span>}>
            <div className="panel-body agent-fields">
              <pre className="agent-code" tabIndex={0} aria-label="Rendered manifest JSON">{JSON.stringify(output, null, 2)}</pre>
              {workspaceConfig && <>
                <h3 className="field-label">Workspace configuration</h3>
                <p className="field-help">Use this config entry for CLI runs or for agents you deploy from a downloaded manifest.</p>
                <pre className="agent-code" tabIndex={0} aria-label="Manual agent configuration">{JSON.stringify(workspaceConfig(definition), null, 2)}</pre>
              </>}
            </div>
          </Panel>
        </section>}
      </div>
      <aside className="section-stack">
        <div className="intake-aside agent-aside">
          <h2>{output ? "Deploy to kagent" : "About this resource"}</h2>
          {settings.data && <p className="agent-namespace">Namespace <code>{settings.data.namespace}</code></p>}
          {settings.data && !writable && <p>Save requires a workspace connection with write access to kagent.</p>}
          {output && !writable && <p>Apply the downloaded manifest in your development cluster.<code className="resource-command">kubectl --context YOUR_DEV_CONTEXT apply -f {filename}</code></p>}
          {guidance}
        </div>
        {deployment}
      </aside>
    </div>
  </>;
}
