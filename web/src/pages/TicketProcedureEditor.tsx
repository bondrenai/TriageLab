import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { TicketConditionEditor } from "../components/ticket-condition-editor";
import { Field } from "../components/resource-editor";
import { Button, ErrorPlate, Panel } from "../components/ui";
import { useDeploymentStatus } from "../hooks/use-agents";
import { useSettings } from "../hooks/use-investigations";
import { usePreviewProcedure, useSaveProcedure } from "../hooks/use-tickets";
import { orderedProcedures, ticketReference } from "../lib/tickets";
import type { EvidenceRule, ProcedureVersion, TicketCase, TicketProcedure, TicketSource } from "../lib/types/ticket";

const priorities = ["low", "normal", "high", "urgent"];

export function TicketProcedureEditor({ connection, version, source, connected, template, clarificationLimit, autoRunsAllowed, renderPreview }: {
  connection: string;
  version: ProcedureVersion;
  source: TicketSource;
  connected: boolean;
  template: string;
  clarificationLimit: number;
  autoRunsAllowed: boolean;
  renderPreview: (ticket: TicketCase, procedure: TicketProcedure) => ReactNode;
}) {
  const [procedures, setProcedures] = useState(() => orderedProcedures(version));
  const [selected, setSelected] = useState(0);
  const draft = procedures[selected];
  const [revision, setRevision] = useState(version.revision);
  const [ticketID, setTicketID] = useState("");
  const save = useSaveProcedure();
  const preview = usePreviewProcedure();
  const settings = useSettings();
  const connectedToCluster = settings.data?.resource_writes === true;
  const agents = useDeploymentStatus("agents", connectedToCluster);
  const sandboxes = useDeploymentStatus("sandboxes", connectedToCluster);
  const agentNames = [...new Set([
    settings.data?.agent ?? version.procedure.default_agent,
    ...(connectedToCluster ? agents.data?.resources.filter(agent => agent.managed && agent.state !== "deleting").map(agent => agent.name) ?? [] : []),
  ])];
  const sandboxNames = [...new Set([
    template, ...(connectedToCluster ? sandboxes.data?.resources.filter(item => item.state !== "deleting").map(item => item.name) ?? [] : []),
  ])];
  const runNamespaces = settings.data?.run_namespaces ?? [];
  const busy = save.isPending || preview.isPending;
  const stale = version.revision !== revision;
  const zendesk = source === "zendesk";
  const bondren = source === "bondren";
  const jira = source === "jira";
  const gitlab = source === "gitlab";
  const kind = zendesk ? "ticket" : "issue";
  const tagLabel = source === "github" ? "GitHub label" : source === "gitlab" ? "GitLab label" : source === "linear" ? "Linear label" : source === "bondren" ? "Bondren tag" : source === "jira" ? "Jira label" : "Zendesk tag";

  function updateProcedures(next: TicketProcedure[], index = selected) {
    setProcedures(next);
    setSelected(index);
    preview.reset();
    save.reset();
  }

  function update(value: TicketProcedure) {
    updateProcedures(procedures.map((procedure, i) => i === selected ? value : procedure));
  }

  function newProcedure() {
    const next = [...procedures];
    const index = next.length - 1;
    next.splice(index, 0, {
      policy: { tag: "", auto_questions: false, max_questions: 0 },
      evidence: [], clarification: "", routes: [], default_agent: draft.default_agent, sandbox: draft.sandbox,
    });
    updateProcedures(next, index);
  }

  function moveProcedure(direction: -1 | 1) {
    const index = selected + direction;
    const next = [...procedures];
    [next[selected], next[index]] = [next[index], next[selected]];
    updateProcedures(next, index);
  }

  function updatePolicy(value: Partial<TicketProcedure["policy"]>) {
    update({ ...draft, policy: { ...draft.policy, ...value } });
  }

  function updateEvidence(index: number, patch: Partial<EvidenceRule>) {
    update({ ...draft, evidence: draft.evidence.map((rule, i) => i === index ? { ...rule, ...patch } : rule) });
  }

  function addEvidence(kind: EvidenceRule["kind"]) {
    update({ ...draft, evidence: [...draft.evidence, { kind, match: "", required: true }] });
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    save.mutate({ connection, version: {
      procedure: procedures[procedures.length - 1],
      additional_procedures: procedures.slice(0, -1),
      revision,
    } }, {
      onSuccess: saved => {
        setRevision(saved.revision);
        setProcedures(orderedProcedures(saved));
        preview.reset();
      },
    });
  }

  function previewDraft(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    preview.mutate({ connection, id: ticketReference(source, ticketID), procedure: draft });
  }

  function agentOptions(current: string) {
    return <>
      {!agentNames.includes(current) && <option value={current}>{current} (unavailable)</option>}
      {agentNames.map(name => <option key={name} value={name}>{name}</option>)}
    </>;
  }

  return <Panel title="Edit ticket procedures">
    <div className="panel-body ticket-detail">
      <form className="agent-fields" onSubmit={submit}>
        <fieldset className="agent-fields agent-inset" disabled={busy}>
          <legend className="field-label">Procedure order</legend>
          <p className="field-help">The first procedure whose match rules fit the case supplies its evidence, clarification, and routing settings.</p>
          <Field label="Procedure to edit">
            <select className="field" value={selected} onChange={e => {
              setSelected(Number(e.target.value));
              preview.reset();
            }}>
              {procedures.map((p, i) => <option key={i} value={i}>{i + 1}. {p.policy.tag || "New procedure"}</option>)}
            </select>
          </Field>
          <div className="button-row">
            <Button type="button" disabled={procedures.length >= 8} onClick={newProcedure}>Add procedure</Button>
            <Button type="button" disabled={selected === 0} onClick={() => moveProcedure(-1)}>Move earlier</Button>
            <Button type="button" disabled={selected === procedures.length - 1} onClick={() => moveProcedure(1)}>Move later</Button>
            <Button type="button" disabled={procedures.length === 1} onClick={() => updateProcedures(
              procedures.filter((_, i) => i !== selected), Math.min(selected, procedures.length - 2),
            )}>Remove procedure</Button>
          </div>
        </fieldset>
        <fieldset className="agent-fields agent-inset" disabled={busy}>
          <legend className="field-label">Match cases</legend>
          <Field label={draft.policy.match_condition ? "Procedure label" : `Required ${tagLabel}`}>
            <input className="field" required maxLength={100} value={draft.policy.tag} onChange={e => updatePolicy({ tag: e.target.value })} />
          </Field>
          {zendesk && <>
            <div className="agent-fields">
              <span className="field-label">Allowed priorities</span>
              <p className="field-help">Leave all unchecked to allow any priority.</p>
              <div className="button-row">
                {priorities.map(priority => <label className="agent-tool-option" key={priority}>
                  <input type="checkbox" checked={draft.policy.priorities?.includes(priority) ?? false} onChange={e => {
                    const previous = draft.policy.priorities ?? [];
                    updatePolicy({ priorities: e.target.checked ? [...previous, priority] : previous.filter(value => value !== priority) });
                  }} />{priority}
                </label>)}
              </div>
            </div>
            <div className="agent-field-grid">
              <Field label="Custom field ID (optional)">
                <input className="field" type="number" min={1} max={Number.MAX_SAFE_INTEGER} step={1} value={draft.policy.field_id ?? ""}
                  onChange={e => updatePolicy({ field_id: e.target.value ? Number(e.target.value) : undefined })} />
              </Field>
              <Field label="Required field value">
                <input className="field" maxLength={200} required={Boolean(draft.policy.field_id)} value={draft.policy.field_value ?? ""}
                  onChange={e => updatePolicy({ field_value: e.target.value })} />
              </Field>
            </div>
          </>}
        </fieldset>

        <TicketConditionEditor key={`match-${selected}`} connection={connection} source={source} label="Procedure match condition"
          value={draft.policy.match_condition ?? ""} emptyHint="An empty condition uses the procedure’s tag to match tickets."
          hint="A CEL match condition takes the place of the tag check. Use tags in the condition if the procedure still needs them."
          onChange={value => updatePolicy({ match_condition: value || undefined })} />

        <fieldset className="agent-fields agent-inset" disabled={busy}>
          <legend className="field-label">Evidence and clarification</legend>
          <p className="field-help">Choose what to collect from the {zendesk ? "requester" : "issue author"}. Missing required items block a run and add a question to the clarification.</p>
          {draft.evidence.length === 0 && <p>Matching cases can proceed without an evidence checklist.</p>}
          {draft.evidence.map((rule, index) => <fieldset className="agent-fields agent-inset" key={index}>
            <legend className="field-label">{rule.kind === "field" ? "Text field" : "Attachment"} {index + 1}</legend>
            {rule.kind === "field" ? <>
              <div className="agent-field-grid">
                <Field label="Field label">
                  <input className="field" required maxLength={100} placeholder="Request ID" value={rule.match}
                    onChange={e => updateEvidence(index, { match: e.target.value })} />
                </Field>
                <Field label="Value pattern (optional)">
                  <input className="field" maxLength={120} value={rule.pattern ?? ""}
                    onChange={e => updateEvidence(index, { pattern: e.target.value || undefined })} />
                </Field>
              </div>
              <p className="field-help">Looks for a reply line such as <code>{rule.match || "Request ID"}: …</code>. A regular expression can restrict the value.</p>
            </> : <>
              <Field label="Filename or extension">
                <input className="field" required maxLength={100} placeholder="trace.log or .log" value={rule.match}
                  onChange={e => updateEvidence(index, { match: e.target.value })} />
              </Field>
              <p className="field-help">Supported file types: .py, .json, .txt, and .log.</p>
            </>}
            <label className="agent-tool-option">
              <input type="checkbox" checked={rule.required} onChange={e => updateEvidence(index, { required: e.target.checked, required_when: e.target.checked ? rule.required_when : undefined })} />
              Required for investigation
            </label>
            {rule.required && <Field label="Question when missing">
              <input className="field" required maxLength={500} value={rule.question ?? ""}
                onChange={e => updateEvidence(index, { question: e.target.value })} />
            </Field>}
            {rule.required && <details>
              <summary>Conditional requirement</summary>
              <TicketConditionEditor connection={connection} source={source} label="Required when"
                value={rule.required_when ?? ""} emptyHint="An empty condition makes this item required for every matching ticket."
                onChange={value => updateEvidence(index, { required_when: value || undefined })} />
            </details>}
            <div><Button type="button" onClick={() => update({ ...draft, evidence: draft.evidence.filter((_, i) => i !== index) })}>Remove evidence item</Button></div>
          </fieldset>)}
          <div className="button-row">
            <Button type="button" disabled={draft.evidence.length >= 12} onClick={() => addEvidence("field")}>Add text field</Button>
            <Button type="button" disabled={draft.evidence.length >= 12} onClick={() => addEvidence("attachment")}>Add attachment rule</Button>
          </div>
          <Field label="Clarification message (optional)">
            <textarea className="field" rows={3} maxLength={500} value={draft.clarification}
              onChange={e => update({ ...draft, clarification: e.target.value })} />
          </Field>
          <p className="field-help">The draft adds your question for each required item still missing. Leave the message blank to show just those questions.</p>
          {clarificationLimit > 0 && <>
            <label className="agent-tool-option">
              <input type="checkbox" checked={draft.policy.auto_questions}
                onChange={e => updatePolicy({ auto_questions: e.target.checked, max_questions: draft.policy.max_questions || 1 })} />
              Allow clarification sends after approval
            </label>
            {draft.policy.auto_questions && <Field label="Maximum clarification attempts">
              <input className="field" type="number" min={1} max={clarificationLimit} required value={draft.policy.max_questions}
                onChange={e => updatePolicy({ max_questions: Number(e.target.value) })} />
            </Field>}
          </>}
        </fieldset>

        <fieldset className="agent-fields agent-inset" disabled={busy}>
          <legend className="field-label">Routing and run target</legend>
          {autoRunsAllowed && <label className="agent-tool-option">
            <input type="checkbox" checked={Boolean(draft.policy.auto_runs)}
              onChange={e => updatePolicy({ auto_runs: e.target.checked })} />
            Start automatically when evidence is ready
          </label>}
          {autoRunsAllowed && draft.policy.auto_runs && <TicketConditionEditor key={`start-${selected}`}
            connection={connection} source={source} label="Automatic start condition" value={draft.policy.auto_run_condition ?? ""}
            emptyHint="An empty condition allows automatic starts when evidence is ready."
            onChange={value => updatePolicy({ auto_run_condition: value || undefined })} />}
          <div className="agent-field-grid">
            <Field label="Default agent">
              <select className="field" value={draft.default_agent} onChange={e => update({ ...draft, default_agent: e.target.value })}>
                {agentOptions(draft.default_agent)}
              </select>
            </Field>
            <Field label="Sandbox template">
              <select className="field" value={draft.sandbox} onChange={e => update({ ...draft, sandbox: e.target.value })}>
                {!sandboxNames.includes(draft.sandbox) && <option value={draft.sandbox}>{draft.sandbox} (unavailable)</option>}
                {sandboxNames.map(name => <option key={name} value={name}>{name}</option>)}
              </select>
            </Field>
            {runNamespaces.length > 0 && <Field label="Namespace">
              <select className="field mono" value={draft.namespace ?? ""} onChange={e => update({ ...draft, namespace: e.target.value || undefined })}>
                <option value="">{settings.data?.namespace} (workspace default)</option>
                {!runNamespaces.includes(draft.namespace ?? "") && draft.namespace && <option value={draft.namespace}>{draft.namespace} (unavailable)</option>}
                {runNamespaces.map(name => <option key={name} value={name}>{name}</option>)}
              </select>
            </Field>}
          </div>
          <p className="field-help">The default agent handles cases with no matching rule. Edit an agent’s investigation instructions in <Link to="/agents" className="ticket-source">Agents</Link>.</p>
          {connectedToCluster && (agents.isError || sandboxes.isError) && <p className="field-help" role="status">Some deployment choices could not be loaded.</p>}
          {draft.routes.map((rule, index) => <fieldset className="agent-fields agent-inset" key={index}>
            <legend className="field-label">Rule {index + 1}</legend>
            <div className="agent-field-grid">
              <Field label={`Product ${tagLabel}`}>
                <input className="field" required maxLength={100} value={rule.tag} onChange={e => update({
                  ...draft, routes: draft.routes.map((item, i) => i === index ? { ...item, tag: e.target.value } : item),
                })} />
              </Field>
              <Field label="Route to agent">
                <select className="field" value={rule.agent} onChange={e => update({
                  ...draft, routes: draft.routes.map((item, i) => i === index ? { ...item, agent: e.target.value } : item),
                })}>{agentOptions(rule.agent)}</select>
              </Field>
            </div>
            <div><Button type="button" onClick={() => update({ ...draft, routes: draft.routes.filter((_, i) => i !== index) })}>Remove rule</Button></div>
          </fieldset>)}
          <div><Button type="button" disabled={draft.routes.length >= 20}
            onClick={() => update({ ...draft, routes: [...draft.routes, { tag: "", agent: draft.default_agent }] })}>Add routing rule</Button></div>
        </fieldset>
        {stale && <p role="status">The saved procedure changed. Reload it to continue editing.</p>}
        {save.isError && <ErrorPlate title="Could not save procedure" error={save.error} />}
        {save.isSuccess && <p role="status">Procedures saved. Refresh tracked cases to review their updated plans.</p>}
        <div className="button-row">
          <Button type="submit" variant="primary" disabled={busy || stale}>{save.isPending ? "Saving…" : "Save procedures"}</Button>
          {stale && <Button type="button" disabled={busy} onClick={() => {
            updateProcedures(orderedProcedures(version), 0);
            setRevision(version.revision);
          }}>Reload saved procedure</Button>}
        </div>
      </form>
      <form className="ticket-form" onSubmit={previewDraft}>
        <Field label={jira ? "Preview Jira issue key, URL, or id" : gitlab ? "Preview GitLab issue number or URL" : bondren ? "Preview Bondren issue ID or number" : `Preview ${kind} number`}>
          <input className="field" inputMode={bondren || jira || gitlab ? undefined : "numeric"} pattern={bondren || jira || gitlab ? undefined : "[1-9][0-9]{0,18}"} maxLength={bondren || jira ? 512 : gitlab ? 320 : 19} required value={ticketID}
            disabled={busy || !connected} onChange={e => { setTicketID(e.target.value); preview.reset(); }} />
        </Field>
        <Button type="submit" disabled={busy || !connected}>{preview.isPending ? "Checking…" : `Preview draft on ${kind}`}</Button>
      </form>
      {preview.isError && <ErrorPlate title="Could not preview draft" error={preview.error} />}
      {preview.data && renderPreview(preview.data, draft)}
    </div>
  </Panel>;
}
