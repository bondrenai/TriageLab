import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { File, FlaskConical, Paperclip, X } from "lucide-react";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Panel, Textarea } from "../components/ui";
import { useCreateRun, useRuns, useSettings } from "../hooks/use-investigations";
import { fileSize } from "../lib/investigations";

export function NewInvestigationPage() {
  const settings = useSettings();
  const runs = useRuns();
  const create = useCreateRun();
  const navigate = useNavigate();
  const input = useRef<HTMLInputElement>(null);
  const [description, setDescription] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [fileError, setFileError] = useState("");
  const [descriptionError, setDescriptionError] = useState("");
  const busy = runs.data?.runs.find(run => run.active);

  function addFiles(selected: FileList | null) {
    if (!selected || !settings.data) return;
    const next = [...files];
    for (const file of Array.from(selected)) {
      const previous = next.findIndex(item => item.name === file.name);
      if (previous >= 0) next[previous] = file; else next.push(file);
    }
    const limits = settings.data;
    const oversized = next.find(file => file.size > limits.max_file_bytes);
    if (oversized) setFileError(`${oversized.name} is larger than ${fileSize(limits.max_file_bytes)}.`);
    else if (next.length > limits.max_files) setFileError(`Choose no more than ${limits.max_files} files.`);
    else if (next.reduce((total, file) => total + file.size, 0) > limits.max_total_bytes) setFileError(`Keep the combined files under ${fileSize(limits.max_total_bytes)}.`);
    else { setFiles(next); setFileError(""); }
    if (input.current) input.current.value = "";
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!description.trim()) { setDescriptionError("Describe the issue before starting an investigation."); document.getElementById("description")?.focus(); return; }
    if (settings.data && new TextEncoder().encode(description.trim()).length > settings.data.max_description_bytes) {
      setDescriptionError(`Keep the description under ${fileSize(settings.data.max_description_bytes)}.`);
      document.getElementById("description")?.focus();
      return;
    }
    setDescriptionError("");
    try {
      const run = await create.mutateAsync({ description: description.trim(), files });
      navigate(`/runs/${run.id}`);
    } catch { /* The mutation error is rendered beside the form. */ }
  }

  return <>
    <PageTitle title="New investigation" back="Investigations" description="Give the investigator a clear starting point." action={<Button form="new-investigation" type="submit" variant="primary" disabled={!settings.data || create.isPending || Boolean(busy)} title={busy ? "An investigation is already running" : undefined}><FlaskConical size={16} aria-hidden="true" />{create.isPending ? "Starting…" : "Start investigation"}</Button>} />
    {settings.isPending ? <Loading label="Loading investigation limits…" /> : settings.isError ? <ErrorPlate title="The workspace is unavailable" error={settings.error} action={<Button onClick={() => void settings.refetch()}>Try again</Button>} /> : <div className="intake-layout">
      <form id="new-investigation" onSubmit={submit} className="section-stack">
        {busy && <div className="notice"><strong>An investigation is already running.</strong><p>This workspace runs one investigation at a time.</p><ButtonLink to={`/runs/${busy.id}`}>View running investigation</ButtonLink></div>}
        {create.isError && <ErrorPlate title="Investigation could not start" error={create.error} />}
        <Panel title="Describe the issue"><div className="panel-body"><label className="field-label" htmlFor="description">What happened?</label><p className="field-help" id="description-help">Include what you expected, what actually happened, and how to trigger the problem.</p><Textarea id="description" value={description} onChange={event => { setDescription(event.target.value); setDescriptionError(""); }} aria-describedby={`description-help${descriptionError ? " description-error" : ""}`} aria-invalid={Boolean(descriptionError)} placeholder="Our client times out after a configuration change. We expected a five-second timeout, but every request fails…" rows={9} maxLength={65536} disabled={create.isPending} />{descriptionError && <p className="field-error" id="description-error">{descriptionError}</p>}<p className="field-help below-field">Include commands as context. They will only be executed inside the investigation sandbox.</p></div></Panel>
        <Panel title="Supporting files" aside={<span className="quiet text-meta">Optional</span>}><div className="panel-body"><p className="field-help" id="files-help">Add source files, configuration, or logs that help reproduce the issue. Remove credentials and customer data first.</p><input className="sr-only" ref={input} id="files" aria-label="Supporting files" type="file" multiple onChange={event => addFiles(event.target.files)} tabIndex={-1} disabled={create.isPending} /><Button type="button" onClick={() => input.current?.click()} disabled={create.isPending} aria-describedby="files-help file-limits"><Paperclip size={16} aria-hidden="true" />Choose files</Button><span className="file-limits" id="file-limits">{fileSize(settings.data.max_file_bytes)} per file / {fileSize(settings.data.max_total_bytes)} total</span>{fileError && <p className="field-error" role="alert">{fileError}</p>}{files.length > 0 && <ul className="file-list">{files.map(file => <li key={file.name}><File size={16} aria-hidden="true" /><span>{file.name}</span><span className="annotation quiet">{fileSize(file.size)}</span><Button variant="ghost" type="button" aria-label={`Remove ${file.name}`} disabled={create.isPending} onClick={() => setFiles(files.filter(item => item !== file))}><X size={15} aria-hidden="true" /></Button></li>)}</ul>}</div></Panel>
      </form>
      <aside className="intake-aside"><h2>From issue to evidence</h2><ol className="process-list"><li><span>1</span><div><h3>Prepare a sandbox</h3><p>Your files go into a temporary, isolated environment.</p></div></li><li><span>2</span><div><h3>Run the investigation</h3><p>The agent tests hypotheses and records process output.</p></div></li><li><span>3</span><div><h3>Review the result</h3><p>Inspect the experiments, conclusions, and anything still unknown.</p></div></li></ol><div className="intake-limits"><h3>Run limits</h3><dl><div><dt>Investigation</dt><dd>{Math.round(settings.data.timeout_seconds / 60)} min</dd></div><div><dt>Experiments</dt><dd>{settings.data.max_experiments} max</dd></div></dl><p>Cleanup is attempted after every run. The report shows its result.</p><p>Runs use your configured model and sandbox. Provider charges may apply.</p></div></aside>
    </div>}
  </>;
}
