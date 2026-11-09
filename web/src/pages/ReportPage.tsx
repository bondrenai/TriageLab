import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowDownToLine, Check, ChevronDown, File, FlaskConical, Square, Terminal } from "lucide-react";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Panel, Stamp, StatusStamp } from "../components/ui";
import { useCancelRun, usePostTicketNote, useRun } from "../hooks/use-investigations";
import { caseTitle, elapsed, exampleReport, fileSize, formatDate, reportURL } from "../lib/investigations";
import type { Experiment, InvestigationReport, TicketNote } from "../lib/types";

export function ReportPage({ example = false }: { example?: boolean }) {
  const { id = "" } = useParams();
  const query = useRun(example ? "" : id);
  if (!example && query.isPending) return <><PageTitle title="Investigation" back="Investigations" /><Loading /></>;
  if (!example && query.isError) return <><PageTitle title="Investigation" back="Investigations" /><ErrorPlate title="Investigation could not load" error={query.error} action={<Button onClick={() => void query.refetch()}>Try again</Button>} /></>;
  return <Report key={example ? "example" : id} report={example ? exampleReport : query.data!} example={example} />;
}

function Report({ report, example }: { report: InvestigationReport; example: boolean }) {
  const cancel = useCancelRun(report.run_id);
  const experiments = report.experiments ?? [];
  const [expanded, setExpanded] = useState<string[]>(experiments[0] ? [experiments[0].process_id] : []);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const analysis = report.analysis;
  const active = Boolean(report.active);
  const attachments = report.case.attachments ?? [];
  const cleanupDone = report.cleanup?.sandbox_deleted === true && report.cleanup?.session_deleted === true;
  const messages = [...(report.errors ?? []), ...(report.cleanup?.errors ?? [])];

  function openEvidence(id: string) {
    setExpanded(previous => previous.includes(id) ? previous : [...previous, id]);
  }

  function evidenceRefs(ids: string[] = []) {
    return <span className="evidence-refs">{ids.map(id => {
      const index = experiments.findIndex(item => item.process_id === id);
      return index >= 0 ? <a key={id} href={`#evidence-${id}`} onClick={() => openEvidence(id)}>Experiment {index + 1}</a> : <span key={id} className="annotation">{id}</span>;
    })}</span>;
  }

  return <>
    <PageTitle title={example ? "The five-millisecond timeout" : caseTitle(report.case.description)} entity back="Investigations" description={<div className="report-meta"><StatusStamp status={report.status} outcome={report.outcome} active={active} /><span className="annotation">{report.run_id.slice(0, 8)}</span><time dateTime={report.started_at}>{formatDate(report.started_at)}</time></div>} action={example ? <ButtonLink to="/new" variant="primary"><FlaskConical size={16} aria-hidden="true" />Investigate your own issue</ButtonLink> : active ? <Button onClick={() => setConfirmCancel(true)} disabled={cancel.isPending || cancel.isSuccess}><Square size={14} aria-hidden="true" />{cancel.isSuccess ? "Stopping…" : "Stop investigation"}</Button> : <ButtonLink to="/new" variant="primary">New investigation</ButtonLink>} />
    {example && <div className="example-notice"><File size={17} aria-hidden="true" /><p><strong>Example investigation.</strong> A saved run from the included Python fixture, using a real agent and sandbox. The fixture simulates a health check; no external service was contacted.</p></div>}
    {confirmCancel && active && <div className="notice" role="alert"><strong>Stop this investigation?</strong><p>The current run will end and cleanup will begin. Collected evidence will remain in the report.</p><div className="button-row"><Button onClick={() => { void cancel.mutateAsync().then(() => setConfirmCancel(false)).catch(() => {}); }} disabled={cancel.isPending}>{cancel.isPending ? "Stopping…" : "Stop and clean up"}</Button><Button variant="ghost" onClick={() => setConfirmCancel(false)}>Keep investigating</Button></div></div>}
    {cancel.isError && <ErrorPlate title="The stop request failed" error={cancel.error} />}
    {messages.length > 0 && <ErrorPlate title="This run needs attention" error={[...new Set(messages)].join("\n")} />}
    {!active && (report.status === "running" || report.status === "queued") && <div className="notice"><strong>This investigation was interrupted.</strong><p>The server is no longer managing this run. Inspect its saved evidence and use the CLI cleanup command if resources are still present.</p></div>}
    {(report.warnings?.length ?? 0) > 0 && <div className="notice"><strong>Investigation notes</strong><ul>{report.warnings?.map((warning, index) => <li key={index}>{warning}</li>)}</ul></div>}
    <div className="report-layout">
      <div className="section-stack">
        {active && <Panel title="Investigation in progress" aside={<Stamp hue="blue">Running</Stamp>}><div className="panel-body"><p>The investigator is working in its sandbox. Evidence appears as processes are collected.</p><ol className="progress-list" tabIndex={0} aria-label="Investigation progress" aria-live="polite" aria-relevant="additions">{(report.progress?.length ? report.progress : ["Preparing the investigation…"]).slice(-8).map((entry, index) => <li key={`${index}-${entry}`}><span className="progress-dot" />{entry}</li>)}</ol></div></Panel>}
        {analysis?.summary ? <Panel title="Investigation findings" aside={<span className="quiet text-meta">Agent analysis</span>}><div className="panel-body"><p className="finding-summary">{analysis.summary}</p>{analysis.root_cause?.statement && <div className="hypothesis-inset"><h3>Model hypothesis</h3><p>{analysis.root_cause.statement}</p>{evidenceRefs(analysis.root_cause.evidence_process_ids)}</div>}<p className="analysis-note">These conclusions are the agent’s interpretation. Review the recorded experiments below.</p></div></Panel> : !active && <Panel title="Investigation findings"><div className="panel-body"><p>No validated analysis was recorded. Review the process evidence and run messages for details.</p></div></Panel>}
        <Panel title="Experiment register" aside={<span className="annotation quiet">{experiments.length} recorded</span>} className="experiment-panel">
          <div className="experiment-intro"><p>Commands and output collected directly from the sandbox.</p><span className="register-columns" aria-hidden="true">Command / Result</span></div>
          {experiments.length ? <div className="experiment-list">{experiments.map((experiment, index) => <ExperimentRow key={experiment.process_id} experiment={experiment} index={index} open={expanded.includes(experiment.process_id)} toggle={() => setExpanded(previous => previous.includes(experiment.process_id) ? previous.filter(id => id !== experiment.process_id) : [...previous, experiment.process_id])} />)}</div> : <div className="inline-empty"><Terminal size={23} aria-hidden="true" /><p>{active ? "Waiting for the first experiment." : "No process evidence was recorded."}</p></div>}
        </Panel>
        {(analysis?.hypotheses?.length ?? 0) > 0 && <Panel title="Hypotheses tested"><div className="hypotheses-list">{analysis?.hypotheses?.map((hypothesis, index) => <div className="hypothesis-row" key={index}><div className="hypothesis-heading"><span className="annotation">H{index + 1}</span><Stamp hue={hypothesis.status === "supported" ? "green" : hypothesis.status === "rejected" ? "gray" : "amber"}>{hypothesis.status}</Stamp></div><p>{hypothesis.statement}</p>{evidenceRefs(hypothesis.evidence_process_ids)}</div>)}</div></Panel>}
        {!example && report.ticket_note && <TicketNotePanel runID={report.run_id} note={report.ticket_note} />}
        {(analysis?.reproduction_steps?.length ?? 0) > 0 && <Panel title="Reproduction steps"><div className="panel-body"><ol className="prose-list numbered">{analysis?.reproduction_steps?.map((step, index) => <li key={index}>{step}</li>)}</ol></div></Panel>}
        {((analysis?.next_actions?.length ?? 0) > 0 || (analysis?.missing_information?.length ?? 0) > 0) && <Panel title="What comes next"><div className="panel-body">{(analysis?.next_actions?.length ?? 0) > 0 && <ul className="prose-list">{analysis?.next_actions?.map((action, index) => <li key={index}>{action}</li>)}</ul>}{(analysis?.missing_information?.length ?? 0) > 0 && <div className="unknowns"><h3>Still unknown</h3><ul className="prose-list">{analysis?.missing_information?.map((item, index) => <li key={index}>{item}</li>)}</ul></div>}</div></Panel>}
      </div>
      <aside className="report-aside section-stack">
        <Panel title="Run record"><div className="panel-body"><dl className="ledger"><div><dt>Started</dt><dd><time dateTime={report.started_at}>{formatDate(report.started_at)}</time></dd></div><div><dt>Duration</dt><dd className="mono">{active ? "In progress" : report.finished_at ? elapsed(report.started_at, report.finished_at) : "Not recorded"}</dd></div><div><dt>Experiments</dt><dd className="mono">{experiments.length}</dd></div><div><dt>Files supplied</dt><dd className="mono">{attachments.length}</dd></div></dl><div className="cleanup-record"><h3>Resource cleanup</h3>{cleanupDone ? <p className="cleanup-success"><Check size={16} aria-hidden="true" />Sandbox and session deleted</p> : active ? <p>Scheduled when the run ends.</p> : <p>Cleanup is not confirmed. Review the saved report before starting another run.</p>}</div>{!example && <div className="report-downloads"><h3>Download report</h3><a className="button button-secondary" href={reportURL(report.run_id, "json")} download><ArrowDownToLine size={15} aria-hidden="true" />JSON</a><a className="button button-secondary" href={reportURL(report.run_id, "md")} download><ArrowDownToLine size={15} aria-hidden="true" />Markdown</a></div>}</div></Panel>
        <Panel title="Original issue"><div className="panel-body"><div className="original-issue" tabIndex={0} role="region" aria-label="Original issue description">{report.case.description.replace(/^#+\s*/, "")}</div></div></Panel>
        <Panel title="Case files" aside={<span className="annotation quiet">{attachments.length}</span>}><div className="panel-body">{attachments.length ? <ul className="attachment-list">{attachments.map(file => <li key={file.path}><File size={15} aria-hidden="true" /><div><span>{file.path}</span><span className="annotation quiet">{fileSize(file.size_bytes)}</span></div></li>)}</ul> : <p className="quiet">No files were supplied.</p>}</div></Panel>
      </aside>
    </div>
  </>;
}

function TicketNotePanel({ runID, note }: { runID: string; note: TicketNote }) {
  const [reviewedPlan, setReviewedPlan] = useState<string>();
  const review = !!reviewedPlan && reviewedPlan === note.plan;
  const post = usePostTicketNote(runID);
  const github = note.source === "github";
  const provider = github ? "GitHub" : "Zendesk";
  return <Panel title={github ? "GitHub investigation comment" : "Zendesk internal note"}>
    <div className="panel-body ticket-detail">
      <p><a className="ticket-source" href={note.ticket_url} target="_blank" rel="noreferrer">{provider} #{note.ticket_id} ↗</a></p>
      {note.status === "posted" && <p role="status">Posted as {github ? "GitHub comment" : "internal note"} {note.comment_id}.</p>}
      {note.status === "unconfirmed" && <p role="status">Delivery is unconfirmed. Check the {provider} conversation for run <code>{runID}</code>.</p>}
      {note.status === "ready" && <>
        <p>{github ? "Share the outcome and selected experiment evidence on this issue." : "Share the outcome and selected experiment evidence with agents on this ticket."}</p>
        {github && <p>This comment is visible to everyone with access to the repository. In a public repository, it is public.</p>}
        {!review ? <div className="button-row"><Button disabled={!note.plan} onClick={() => setReviewedPlan(note.plan)}>{github ? "Review comment" : "Review internal note"}</Button></div> : <>
          <p className="ticket-draft">{note.body}</p>
          <div className="button-row">
            <Button disabled={!note.can_post || !note.plan || post.isPending} onClick={() => note.plan && post.mutate(note.plan)}>{post.isPending ? "Posting…" : github ? "Post comment" : "Post internal note"}</Button>
            <Button variant="ghost" disabled={post.isPending} onClick={() => { post.reset(); setReviewedPlan(undefined); }}>Cancel</Button>
          </div>
          {!note.can_post && <p>{github ? "GitHub posting is unavailable. Review the repository connection in " : "Connect Zendesk from "}<Link className="ticket-source" to="/tickets">Ticket cases</Link>{github ? "." : " to post."}</p>}
        </>}
      </>}
      {post.isError && <ErrorPlate title={github ? "Comment could not be posted" : "Internal note could not be posted"} error={post.error} />}
    </div>
  </Panel>;
}

function ExperimentRow({ experiment, index, open, toggle }: { experiment: Experiment; index: number; open: boolean; toggle: () => void }) {
  const hasExit = typeof experiment.exit_code === "number";
  return <details className="experiment" open={open} id={`evidence-${experiment.process_id}`}>
    <summary onClick={event => { event.preventDefault(); toggle(); }}><span className="experiment-number">{String(index + 1).padStart(2, "0")}</span><code>{experiment.command.map(argument => /\s/.test(argument) ? JSON.stringify(argument) : argument).join(" ")}</code><span className="experiment-exit">{hasExit ? <Stamp hue={experiment.exit_code === 0 ? "green" : "amber"}>Exit {experiment.exit_code}</Stamp> : <Stamp>{experiment.state?.replace(/^PROCESS_STATE_/, "").toLowerCase() || "State unavailable"}</Stamp>}</span><ChevronDown size={16} className="disclosure-chevron" aria-hidden="true" /></summary>
    <div className="experiment-content"><div className="process-context"><span className="annotation">{experiment.process_id}</span>{experiment.cwd && <span className="annotation">{experiment.cwd}</span>}</div><div className="output-label">Standard output</div><pre className="process-output" tabIndex={0} role="region" aria-label={`Standard output for experiment ${index + 1}`}>{experiment.stdout || (experiment.outputs_complete === true ? "No standard output." : "Output not collected.")}</pre>{experiment.stderr && <><div className="output-label">Standard error</div><pre className="process-output stderr" tabIndex={0} role="region" aria-label={`Standard error for experiment ${index + 1}`}>{experiment.stderr}</pre></>}{(experiment.truncated || experiment.outputs_complete === false || experiment.timed_out) && <p className="output-warning">{experiment.timed_out ? "The process exceeded its time limit. " : ""}{experiment.truncated ? "Output was truncated. " : ""}{experiment.outputs_complete === false ? "Output collection is incomplete." : ""}</p>}</div>
  </details>;
}
