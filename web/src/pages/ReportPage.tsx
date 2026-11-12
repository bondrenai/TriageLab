import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { ArrowDownToLine, Check, ChevronDown, File, FlaskConical, Pause, Square, Terminal } from "lucide-react";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Panel, Stamp, StatusStamp, Textarea } from "../components/ui";
import { Assessment, commandText, EvidenceInspector, EvidenceReferences, ProcessEvidence, ProcessResult, type EvidenceSelection } from "../components/report-evidence";
import { hasRetainedWorkspace, InvestigationBrief, ReportStatusBand, reproductionLabel, RoundHistory } from "../components/report-overview";
import { MarkdownBlocks, MarkdownInline } from "../components/markdown";
import { InvestigationChat } from "../components/result-chat";
import { useCancelRun, useCloseRun, usePauseRun, usePostTicketNote, useResumeRun, useRun, useSettings } from "../hooks/use-investigations";
import { useSession } from "../hooks/use-auth";
import { roleAtLeast } from "../lib/auth";
import { caseTitle, elapsed, exampleReport, fileSize, formatDate, reportURL } from "../lib/investigations";
import type { Experiment, ExperimentAssessment, InvestigationReport, TicketNote } from "../lib/types";

export function ReportPage({ example = false }: { example?: boolean }) {
  const { id = "" } = useParams();
  const query = useRun(example ? "" : id);
  if (!example && query.isPending) return <><PageTitle title="Investigation" back="Investigations" /><Loading /></>;
  if (!example && query.isError) return <><PageTitle title="Investigation" back="Investigations" /><ErrorPlate title="Investigation could not load" error={query.error} action={<Button onClick={() => void query.refetch()}>Try again</Button>} /></>;
  return <Report key={example ? "example" : id} report={example ? exampleReport : query.data!} example={example} />;
}

function Report({ report: latest, example }: { report: InvestigationReport; example: boolean }) {
  const location = useLocation();
  const navigate = useNavigate();
  const session = useSession();
  const canRun = roleAtLeast(session.data?.role, "user");
  const cancel = useCancelRun(latest.run_id);
  const pause = usePauseRun(latest.run_id);
  const close = useCloseRun(latest.run_id);
  const [selectedRound, setSelectedRound] = useState<number | null>(null);
  const report = selectedRound === null ? latest : latest.rounds?.find((item, index) => (item.round ?? index + 1) === selectedRound) ?? latest;
  const round = report.round ?? selectedRound ?? 1;
  const historical = report !== latest;
  const lifecycle = latest.lifecycle;
  const experiments = report.experiments ?? [];
  const [expanded, setExpanded] = useState<string[]>([]);
  const [evidence, setEvidence] = useState<EvidenceSelection | null>(null);
  const handledEvidenceHash = useRef("");
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const [showResume, setShowResume] = useState(false);
  const analysis = report.analysis;
  const active = Boolean(latest.active);
  const attachments = report.case?.attachments ?? [];
  const additionalFiles = report.continuation?.attachments ?? [];
  const cleanupDone = latest.cleanup?.sandbox_deleted === true && latest.cleanup?.session_deleted === true;
  const retained = hasRetainedWorkspace(latest);
  const questions = latest.analysis?.missing_information ?? [];
  const resumeLabel = latest.status === "paused" && questions.length > 0 ? "Add information and resume" : "Resume investigation";
  const messages = [...(report.errors ?? []), ...(report.cleanup?.errors ?? []), ...(latest.errors ?? []), ...(latest.cleanup?.errors ?? []), ...(latest.action_error ? [latest.action_error] : [])];

  useEffect(() => {
    const match = /^#evidence-(\d+)-(.+)$/.exec(location.hash);
    if (!match) { handledEvidenceHash.current = ""; return; }
    if (handledEvidenceHash.current === location.hash) return;
    let id: string;
    try { id = decodeURIComponent(match[2]); } catch { return; }
    const evidenceRound = Number(match[1]);
    const target = evidenceRound === (latest.round ?? 1) ? latest : latest.rounds?.find((item, index) => (item.round ?? index + 1) === evidenceRound);
    if (!target?.experiments?.some(item => item.process_id === id)) return;
    handledEvidenceHash.current = location.hash;
    setSelectedRound(evidenceRound);
    const key = `${evidenceRound}:${id}`;
    setExpanded(previous => previous.includes(key) ? previous : [...previous, key]);
    const hash = location.hash;
    requestAnimationFrame(() => {
      if (handledEvidenceHash.current === hash) document.getElementById(`evidence-${evidenceRound}-${id}`)?.scrollIntoView();
    });
  }, [location.hash, latest]);

  function openEvidence(id: string, evidenceRound = round) {
    const target = evidenceRound === (latest.round ?? 1) ? latest : latest.rounds?.find((item, index) => (item.round ?? index + 1) === evidenceRound);
    if (!target?.experiments?.some(item => item.process_id === id)) return;
    setSelectedRound(evidenceRound);
    const key = `${evidenceRound}:${id}`;
    setExpanded(previous => previous.includes(key) ? previous : [...previous, key]);
  }

  function selectRound(value: number | null) {
    setSelectedRound(value);
    setEvidence(null);
    if (location.hash) void navigate({ pathname: location.pathname, search: location.search, hash: "" }, { replace: true });
  }

  function references(ids: string[] = [], title: string, statement: string, label?: string) {
    return <EvidenceReferences report={report} context={{ ids, title, statement }} onReview={setEvidence} onOpenEvidence={openEvidence} label={label} />;
  }

  return <>
    <PageTitle title={example ? "The eleven missing orders" : caseTitle(latest.case?.description ?? "Investigation")} entity back="Investigations"
      description={<div className="report-meta"><span className="annotation">{latest.run_id.slice(0, 8)}</span><time dateTime={latest.started_at}>{formatDate(latest.started_at)}</time></div>}
      action={example ? canRun ? <ButtonLink to="/new" variant="primary"><FlaskConical size={16} aria-hidden="true" />Investigate your own issue</ButtonLink> : null : canRun ? <div className="button-row">
        {active ? <>
          {lifecycle?.can_pause && <Button onClick={() => pause.mutate()} disabled={pause.isPending}><Pause size={14} aria-hidden="true" />{pause.isPending ? "Requesting pause…" : "Pause investigation"}</Button>}
          <Button onClick={() => setConfirmCancel(true)} disabled={cancel.isPending || latest.status === "closing"}><Square size={14} aria-hidden="true" />Stop investigation</Button>
        </> : <>
          {lifecycle?.can_resume && !showResume && <Button variant="primary" onClick={() => setShowResume(true)}>{resumeLabel}</Button>}
          {lifecycle?.can_close && <Button onClick={() => setConfirmClose(true)}>Close investigation</Button>}
          {!lifecycle?.can_resume && !lifecycle?.can_close && <ButtonLink to="/new" variant="primary">New investigation</ButtonLink>}
        </>}
      </div> : null} />
    {example && <div className="example-notice"><File size={17} aria-hidden="true" /><p><strong>Example investigation.</strong> A saved run of the included missing-orders case, using a real agent and sandbox. The customer, orders, and log are fictional. The sync tool and its defect are real code.</p></div>}
    {historical && <div className="notice historical-notice" role="status"><div><strong>Viewing round {round}; latest is round {latest.round ?? 1}.</strong><p>Evidence and analysis below belong to this saved round. Execution controls apply to the current investigation.</p></div><Button onClick={() => selectRound(null)}>Return to latest round</Button></div>}
    <ReportStatusBand latest={latest} report={report} reproductionEvidence={analysis?.reproduced_process_ids?.length ? references(analysis.reproduced_process_ids, "Reproduction outcome", `${reproductionLabel(report.outcome)}. ${analysis.summary ?? ""}`, "Evidence for reproduction") : null} />
    {confirmCancel && active && <div className="notice" role="alert"><strong>Stop this investigation?</strong><p>The current run will end and cleanup will begin. Collected evidence will remain in the report.</p><div className="button-row"><Button onClick={() => { void cancel.mutateAsync().then(() => setConfirmCancel(false)).catch(() => {}); }} disabled={cancel.isPending}>{cancel.isPending ? "Stopping…" : "Stop and clean up"}</Button><Button variant="ghost" onClick={() => setConfirmCancel(false)}>Keep investigating</Button></div></div>}
    {cancel.isError && <ErrorPlate title="The stop request failed" error={cancel.error} />}
    {pause.isError && <ErrorPlate title="The pause request failed" error={pause.error} />}
    {close.isError && <ErrorPlate title="The close request failed" error={close.error} />}
    {lifecycle?.message && <div className="notice"><p>{lifecycle.message}</p></div>}
    {!active && latest.status === "paused" && <div className="notice current-action"><strong>{questions.length ? "Waiting for information" : "Investigation paused"} · current round {latest.round ?? 1}</strong>
      {questions.length > 0 && <ul className="prose-list">{questions.map((question, index) => <li key={index}>{question}</li>)}</ul>}
      <p>{cleanupDone ? "The sandbox and investigator session have been deleted. Review the saved record and lifecycle messages." : retained ? "The workspace and investigator conversation are retained. Add information to continue, or close the investigation to delete its execution resources." : "Workspace retention is unconfirmed. Review the saved record and lifecycle messages before continuing."}</p>
      {canRun && lifecycle?.can_resume && !showResume && <Button onClick={() => setShowResume(true)}>{resumeLabel}</Button>}
    </div>}
    {!active && lifecycle?.phase === "invoking" && <div className="notice"><strong>The continuation outcome is uncertain.</strong><p>The model invocation will not be replayed. Review the saved evidence and close the investigation to release its execution resources.</p></div>}
    {!active && (latest.status === "pausing" || latest.status === "resuming") && <div className="notice"><strong>A workspace operation needs to finish.</strong><p>Resume reconciles the saved operation before continuing. For a retry, supply the same information and files you previously approved.</p></div>}
    {showResume && !active && lifecycle?.can_resume && <div className="report-resume"><ResumePanel report={latest} onCancel={() => setShowResume(false)} onStarted={() => { setShowResume(false); selectRound(null); }} /></div>}
    {confirmClose && !active && lifecycle?.can_close && <div className="notice" role="alert"><strong>Close this investigation?</strong><p>This deletes the investigation’s remaining sandbox and investigator session. Saved reports remain available, but this investigation cannot be resumed afterward.</p><div className="button-row"><Button disabled={close.isPending} onClick={() => { void close.mutateAsync({ round: lifecycle.round, settings: lifecycle.resume_settings }).then(() => setConfirmClose(false)).catch(() => {}); }}>{close.isPending ? "Closing…" : "Close and delete resources"}</Button><Button variant="ghost" disabled={close.isPending} onClick={() => setConfirmClose(false)}>Keep investigation</Button></div></div>}
    {messages.length > 0 && <ErrorPlate title="This run needs attention" error={[...new Set(messages)].join("\n")} />}
    {!active && (latest.status === "running" || latest.status === "queued") && <div className="notice"><strong>This investigation was interrupted.</strong><p>The server is no longer managing this run. Inspect its saved evidence and {lifecycle?.can_close ? "close the investigation to clean up its execution resources." : "use the CLI cleanup command if resources are still present."}</p></div>}
    {(report.warnings?.length ?? 0) > 0 && <div className="notice"><strong>Round {round} notes</strong><ul>{report.warnings?.map((warning, index) => <li key={index}>{warning}</li>)}</ul></div>}
    <RoundHistory latest={latest} selectedRound={selectedRound} onSelect={selectRound} />
    <div className="report-layout">
      <div className="section-stack">
        <InvestigationBrief report={report} active={active && !historical} leadingEvidence={analysis?.root_cause ? references(analysis.root_cause.evidence_process_ids, "Leading hypothesis", analysis.root_cause.statement) : null} />
        {!example && <InvestigationChat key={latest.run_id} report={latest} canAsk={canRun} onReviewEvidence={setEvidence} onOpenEvidence={openEvidence} />}
        {active && <Panel title="Current investigation progress" aside={<StatusStamp status={latest.status} active />}><div className="panel-body"><ol className="progress-list" tabIndex={0} aria-label="Investigation progress" aria-live="polite" aria-relevant="additions">{(latest.progress?.length ? latest.progress : ["Preparing the investigation…"]).slice(-8).map((entry, index) => <li key={`${index}-${entry}`}><span className="progress-dot" />{entry}</li>)}</ol></div></Panel>}
        {(analysis?.hypotheses?.length ?? 0) > 0 && <Panel title="Hypotheses considered" aside={<span className="quiet text-meta">Agent analysis</span>}><div className="hypotheses-list">{analysis?.hypotheses?.map((hypothesis, index) => <div className="hypothesis-row" key={index}>
          <div className="hypothesis-heading"><span className="annotation">H{index + 1}</span><Stamp hue={hypothesis.status === "supported" ? "green" : hypothesis.status === "rejected" ? "gray" : "amber"}>{hypothesis.status}</Stamp></div>
          <p><MarkdownInline text={hypothesis.statement} /></p>
          {references(hypothesis.evidence_process_ids, `Hypothesis ${index + 1} · ${hypothesis.status}`, hypothesis.statement, "Cited evidence")}
          {!!hypothesis.contradicting_process_ids?.length && references(hypothesis.contradicting_process_ids, `Evidence against hypothesis ${index + 1}`, hypothesis.statement, "Contradicting evidence")}
          {hypothesis.distinguishing_observation && <div className="hypothesis-distinction"><h3>What would distinguish this explanation</h3><p><MarkdownInline text={hypothesis.distinguishing_observation} /></p></div>}
        </div>)}</div></Panel>}
        <Panel title="Experiment register" aside={<span className="annotation quiet">Round {round} · {experiments.length} recorded</span>} className="experiment-panel">
          <div className="experiment-intro"><p>Commands and output collected directly from the sandbox. Agent interpretations are labelled separately.</p><span className="register-columns" aria-hidden="true">Command / Result</span></div>
          {experiments.length ? <div className="experiment-list">{experiments.map((experiment, index) => {
            const key = `${round}:${experiment.process_id}`;
            const assessment = analysis?.experiment_assessments?.find(item => item.process_id === experiment.process_id);
            return <ExperimentRow key={key} round={round} experiment={experiment} index={index} assessment={assessment} open={expanded.includes(key)} toggle={() => setExpanded(previous => previous.includes(key) ? previous.filter(id => id !== key) : [...previous, key])} />;
          })}</div> : <div className="inline-empty"><Terminal size={23} aria-hidden="true" /><p>{active && !historical ? "Waiting for the first experiment." : "No process evidence was recorded."}</p></div>}
        </Panel>
        {(analysis?.reproduction_steps?.length ?? 0) > 0 && <Panel title="Reproduction steps" aside={<span className="quiet text-meta">Agent instructions</span>}><div className="panel-body"><ol className="prose-list numbered">{analysis?.reproduction_steps?.map((step, index) => <li key={index}><MarkdownInline text={step} /></li>)}</ol></div></Panel>}
        {!example && latest.ticket_note && <TicketNotePanel runID={latest.run_id} note={latest.ticket_note} canPost={canRun} />}
      </div>
      <aside className="report-aside section-stack">
        <Panel title={`Round ${round} record`}><div className="panel-body"><dl className="ledger">
          {report.agent && <div><dt>Agent</dt><dd className="mono run-agent-name">{report.agent.namespace}/{report.agent.name}</dd></div>}
          {report.sandbox_template && <div><dt>Sandbox</dt><dd className="mono">{report.sandbox_template}</dd></div>}
          <div><dt>{report.round_started_at ? "Round started" : "Started"}</dt><dd><time dateTime={report.round_started_at ?? report.started_at}>{formatDate(report.round_started_at ?? report.started_at)}</time></dd></div>
          <div><dt>Duration</dt><dd className="mono">{active && !historical ? "In progress" : report.finished_at ? elapsed(report.round_started_at ?? report.started_at, report.finished_at) : !historical && latest.status === "paused" ? "Paused" : "Not recorded"}</dd></div>
          <div><dt>Experiments</dt><dd className="mono">{experiments.length}</dd></div><div><dt>Original files</dt><dd className="mono">{attachments.length}</dd></div>
        </dl><div className="cleanup-record"><h3>Current resource cleanup</h3>{cleanupDone ? <p className="cleanup-success"><Check size={16} aria-hidden="true" />Sandbox and session deleted</p> : retained ? <p>Workspace and conversation retained until resumed or closed.</p> : active ? <p>Resources remain while work is active. Completion or closing attempts cleanup.</p> : latest.status === "paused" ? <p>Workspace retention is unconfirmed. Review the saved record and lifecycle messages.</p> : <p>Cleanup is not confirmed. Review the saved report and run messages.</p>}</div>
          {!example && <div className="report-downloads"><h3>Download investigation</h3>{(latest.rounds?.length ?? 0) > 0 && <p className="field-help">Downloads include all rounds.</p>}<a className="button button-secondary" href={reportURL(latest.run_id, "json")} download><ArrowDownToLine size={15} aria-hidden="true" />JSON</a><a className="button button-secondary" href={reportURL(latest.run_id, "md")} download><ArrowDownToLine size={15} aria-hidden="true" />Markdown</a></div>}
        </div></Panel>
        <Panel title="Original issue"><div className="panel-body">{report.case?.description ? <MarkdownBlocks className="original-issue" tabIndex={0} role="region" aria-label="Original issue description" text={report.case.description} /> : <div className="original-issue" tabIndex={0} role="region" aria-label="Original issue description">Original case unavailable; review the saved round reports.</div>}</div></Panel>
        {report.continuation && <Panel title={`Round ${round} input`}><div className="panel-body"><MarkdownBlocks className="original-issue" tabIndex={0} role="region" aria-label={`Additional information for round ${round}`} text={report.continuation.description} />{additionalFiles.length > 0 && <ul className="attachment-list">{additionalFiles.map(file => <li key={file.path}><File size={15} aria-hidden="true" /><div><span>{file.path}</span><span className="annotation quiet">{fileSize(file.size_bytes)}</span></div></li>)}</ul>}</div></Panel>}
        <Panel title="Case files" aside={<span className="annotation quiet">{attachments.length}</span>}><div className="panel-body">{attachments.length ? <ul className="attachment-list">{attachments.map(file => <li key={file.path}><File size={15} aria-hidden="true" /><div><span>{file.path}</span><span className="annotation quiet">{fileSize(file.size_bytes)}</span></div></li>)}</ul> : <p className="quiet">No files were supplied.</p>}</div></Panel>
      </aside>
    </div>
    {evidence && <EvidenceInspector selection={evidence} onClose={() => setEvidence(null)} onOpenRegister={openEvidence} />}
  </>;
}

function ResumePanel({ report, onCancel, onStarted }: { report: InvestigationReport; onCancel: () => void; onStarted: () => void }) {
  const [review] = useState(report.lifecycle!);
  const settings = useSettings();
  const resume = useResumeRun(report.run_id);
  const [description, setDescription] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [inputError, setInputError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const limits = settings.data;
    if (!limits) return;
    if (!description.trim()) { setInputError("Add the new information before resuming."); return; }
    if (new TextEncoder().encode(description).length > limits.max_description_bytes) { setInputError(`Keep new information under ${fileSize(limits.max_description_bytes)}.`); return; }
    if (files.length > limits.max_files || files.some(file => file.size > limits.max_file_bytes) || files.reduce((total, file) => total + file.size, 0) > limits.max_total_bytes) {
      setInputError(`Choose up to ${limits.max_files} files, ${fileSize(limits.max_file_bytes)} each and ${fileSize(limits.max_total_bytes)} combined.`);
      return;
    }
    setInputError("");
    try {
      await resume.mutateAsync({ description, files, round: review.round, settings: review.resume_settings });
      onStarted();
    } catch { /* Keep the exact reviewed inputs available for a retry. */ }
  }

  return <Panel title={`Resume investigation · round ${report.round ?? 1}`}><form className="panel-body section-stack" onSubmit={submit}>
    <p>Continue the same workspace and conversation with the information below. Previous files and evidence remain available.</p>
    {!!report.analysis?.missing_information?.length && <div className="resume-questions"><h3>Questions from the investigator</h3><ul className="prose-list">{report.analysis.missing_information.map((question, index) => <li key={index}><MarkdownInline text={question} /></li>)}</ul></div>}
    <dl className="ledger"><div><dt>Agent</dt><dd className="mono">{report.agent?.namespace}/{report.agent?.name}</dd></div><div><dt>Sandbox</dt><dd className="mono">{report.sandbox_template}</dd></div><div><dt>New round limit</dt><dd>{review.timeout_seconds} seconds / {review.max_experiments} experiments</dd></div></dl>
    <div><label className="field-label" htmlFor="resume-description">New information</label><Textarea id="resume-description" value={description} onChange={event => setDescription(event.target.value)} rows={6} required disabled={resume.isPending} /></div>
    <div><label className="field-label" htmlFor="resume-files">Additional files</label><input className="field" id="resume-files" type="file" multiple disabled={resume.isPending} onChange={event => setFiles(Array.from(event.target.files ?? []))} />{settings.data && <p className="field-help below-field">{fileSize(settings.data.max_file_bytes)} per file / {fileSize(settings.data.max_total_bytes)} total. Added files are stored separately from prior inputs.</p>}{files.length > 0 && <ul className="prose-list">{files.map((file, index) => <li key={`${file.name}-${index}`}>{file.name} · {fileSize(file.size)}</li>)}</ul>}</div>
    {settings.isError && <ErrorPlate title="Input limits could not load" error={settings.error} />}
    {inputError && <p className="field-error" role="alert">{inputError}</p>}
    {resume.isError && <ErrorPlate title="Investigation could not resume" error={resume.error} />}
    <p className="field-help">Approve this information, the selected files, and the displayed execution limits. Resuming may incur model and sandbox charges.</p>
    <div className="button-row"><Button type="submit" variant="primary" disabled={resume.isPending || !settings.data}>{resume.isPending ? "Resuming…" : "Approve and resume"}</Button><Button type="button" variant="ghost" disabled={resume.isPending} onClick={onCancel}>Cancel</Button></div>
  </form></Panel>;
}

function TicketNotePanel({ runID, note, canPost }: { runID: string; note: TicketNote; canPost: boolean }) {
  const [reviewedPlan, setReviewedPlan] = useState<string>();
  const review = !!reviewedPlan && reviewedPlan === note.plan;
  const post = usePostTicketNote(runID);
  const github = note.source === "github";
  const linear = note.source === "linear";
  const bondren = note.source === "bondren";
  const issue = github || linear;
  const provider = github ? "GitHub" : linear ? "Linear" : bondren ? "Bondren" : "Zendesk";
  return <Panel title={issue ? `${provider} investigation comment` : bondren ? "Bondren internal note" : "Zendesk internal note"}>
    <div className="panel-body ticket-detail">
      <dl className="handoff-details">
        <div><dt>Destination</dt><dd><a className="ticket-source" href={note.ticket_url} target="_blank" rel="noreferrer">{provider} #{note.ticket_id} ↗</a></dd></div>
        <div><dt>Audience</dt><dd>{github ? "Everyone with repository access; public in a public repository." : linear ? "People with access to this Linear workspace." : bondren ? "The internal team on this Bondren issue." : "Agents with access to this Zendesk ticket; an internal note."}</dd></div>
        <div><dt>Content</dt><dd>Run outcome and the experiment evidence included in the prepared note. Review the exact text before posting.</dd></div>
      </dl>
      {note.status === "posted" && <p role="status">Delivery confirmed · posted as {issue ? `${provider} comment` : "internal note"} {note.comment_id}.</p>}
      {note.status === "unconfirmed" && <p role="status">Delivery is unconfirmed. Check the {provider} conversation for run <code>{runID}</code> before attempting another post.</p>}
      {note.status === "blocked" && <>
        <p role="status">The output sanitizer held this {issue ? "comment" : "note"} for review.</p>
        {note.findings && <ul className="prose-list">{note.findings.map((finding, index) => <li key={index}>{finding}</li>)}</ul>}
        <p className="ticket-draft">{note.body}</p>
        <p>Posting stays disabled while a finding stands. Review the held text above before sharing anything with the {github ? "repository" : linear ? "workspace" : bondren ? "team on this issue" : "agents on this ticket"}.</p>
      </>}
      {note.status === "ready" && <>
        <p>{issue ? "Share the outcome and selected experiment evidence on this issue." : "Share the outcome and selected experiment evidence with agents on this ticket."}</p>
        {!review ? <div className="button-row">{canPost && <Button disabled={!note.plan} onClick={() => setReviewedPlan(note.plan)}>{issue ? "Review comment" : "Review internal note"}</Button>}</div> : <>
          <div className="handoff-preview"><h3>Exact text to be posted</h3><p className="ticket-draft">{note.body}</p></div>
          <div className="button-row">
            <Button disabled={!note.can_post || !note.plan || post.isPending} onClick={() => note.plan && post.mutate(note.plan)}>{post.isPending ? "Posting…" : issue ? "Post comment" : "Post internal note"}</Button>
            <Button variant="ghost" disabled={post.isPending} onClick={() => { post.reset(); setReviewedPlan(undefined); }}>Cancel</Button>
          </div>
          {!note.can_post && <p>{github ? "GitHub posting is unavailable. Review the repository connection in " : linear ? "Linear posting is unavailable. Review the connection in " : bondren ? "Bondren posting is unavailable. Review the agent token in " : "Connect Zendesk from "}<Link className="ticket-source" to="/tickets">Ticket cases</Link>{issue || bondren ? "." : " to post."}</p>}
        </>}
      </>}
      {post.isError && <ErrorPlate title={github ? "Comment could not be posted" : "Internal note could not be posted"} error={post.error} />}
    </div>
  </Panel>;
}

function ExperimentRow({ experiment, round, index, assessment, open, toggle }: { experiment: Experiment; round: number; index: number; assessment?: ExperimentAssessment; open: boolean; toggle: () => void }) {
  return <details className="experiment" open={open} id={`evidence-${round}-${experiment.process_id}`}>
    <summary onClick={event => { event.preventDefault(); toggle(); }}><span className="experiment-number">{String(index + 1).padStart(2, "0")}</span><code>{commandText(experiment.command)}</code><span className="experiment-exit"><ProcessResult experiment={experiment} /></span><ChevronDown size={16} className="disclosure-chevron" aria-hidden="true" /></summary>
    <div className="experiment-content"><ProcessEvidence experiment={experiment} index={index} />{assessment && <Assessment assessment={assessment} />}</div>
  </details>;
}
