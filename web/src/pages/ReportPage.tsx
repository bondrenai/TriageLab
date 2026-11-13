import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { ArrowDownToLine, Check, ChevronDown, Compass, File, FileCode, FileOutput, FlaskConical, Pause, RefreshCw, Square, Terminal, Wrench } from "lucide-react";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Panel, Stamp, StatusStamp, Textarea } from "../components/ui";
import { Assessment, commandText, EvidenceInspector, EvidenceReferences, ProcessEvidence, ProcessResult, type EvidenceSelection } from "../components/report-evidence";
import { hasRetainedWorkspace, InvestigationBrief, ReportStatusBand, reproductionLabel, RoundHistory } from "../components/report-overview";
import { MarkdownBlocks, MarkdownInline } from "../components/markdown";
import { InvestigationChat } from "../components/result-chat";
import { useCancelRun, useCleanupRun, useCloseRun, usePauseRun, usePostTicketNote, useResumeRun, useRun, useSettings, useSteerRun } from "../hooks/use-investigations";
import { useSession } from "../hooks/use-auth";
import { roleAtLeast } from "../lib/auth";
import { caseTitle, elapsed, exampleReport, fileSize, formatDate, reportURL } from "../lib/investigations";
import { buildWatchActivity, liveElapsed, resolveExpandedExperimentKeys, taskStateLabel, type WatchActivityItem } from "../lib/watch";
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
  const [manualToggles, setManualToggles] = useState<Record<string, boolean>>({});
  const [evidence, setEvidence] = useState<EvidenceSelection | null>(null);
  const handledEvidenceHash = useRef("");
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const [showResume, setShowResume] = useState(false);
  const [showSteer, setShowSteer] = useState(false);
  const steerRef = useRef<HTMLDivElement>(null);
  const analysis = report.analysis;
  const active = Boolean(latest.active);
  const liveWatch = active && !historical;
  const [now, setNow] = useState(() => Date.now());
  const attachments = report.case?.attachments ?? [];
  const additionalFiles = report.continuation?.attachments ?? [];
  const cleanupDone = latest.cleanup?.sandbox_deleted === true && latest.cleanup?.session_deleted === true;
  const retained = hasRetainedWorkspace(latest);
  const questions = latest.analysis?.missing_information ?? [];
  const resumeLabel = latest.status === "paused" && questions.length > 0 ? "Add information and resume" : "Resume investigation";
  const messages = [...(report.errors ?? []), ...(report.cleanup?.errors ?? []), ...(latest.errors ?? []), ...(latest.cleanup?.errors ?? []), ...(latest.action_error ? [latest.action_error] : [])];

  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);

  const expandedSet = useMemo(
    () => resolveExpandedExperimentKeys(round, experiments, liveWatch, manualToggles),
    [round, experiments, liveWatch, manualToggles],
  );
  const allExpanded = experiments.length > 0 && experiments.every(item => expandedSet.has(`${round}:${item.process_id}`));

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
    setManualToggles(previous => ({ ...previous, [key]: true }));
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
    setManualToggles(previous => ({ ...previous, [key]: true }));
    requestAnimationFrame(() => {
      document.getElementById(`evidence-${evidenceRound}-${id}`)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    });
  }

  function selectRound(value: number | null) {
    setSelectedRound(value);
    setEvidence(null);
    if (location.hash) void navigate({ pathname: location.pathname, search: location.search, hash: "" }, { replace: true });
  }

  function openSteerPanel() {
    setShowSteer(true);
    requestAnimationFrame(() => {
      steerRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    });
  }

  function toggleAllExperiments() {
    const target = !allExpanded;
    setManualToggles(previous => {
      const next = { ...previous };
      for (const item of experiments) {
        next[`${round}:${item.process_id}`] = target;
      }
      return next;
    });
  }

  function references(ids: string[] = [], title: string, statement: string, label?: string) {
    return <EvidenceReferences report={report} context={{ ids, title, statement }} onReview={setEvidence} onOpenEvidence={openEvidence} label={label} />;
  }

  const briefPanel = (
    <InvestigationBrief
      report={report}
      active={liveWatch}
      leadingEvidence={analysis?.root_cause ? references(analysis.root_cause.evidence_process_ids, "Leading hypothesis", analysis.root_cause.statement) : null}
    />
  );

  const experimentPanel = (
    <Panel
      title="Experiment register"
      aside={<div className="panel-aside-group">
        <span className="annotation quiet">Round {round} · {experiments.length} recorded</span>
        {experiments.length > 0 && <Button variant="ghost" className="compact-action" onClick={toggleAllExperiments}>{allExpanded ? "Collapse all" : "Expand all"}</Button>}
      </div>}
      className="experiment-panel"
    >
      <div className="experiment-intro"><p>Commands and output collected directly from the sandbox. Agent interpretations are labelled separately.</p><span className="register-columns" aria-hidden="true">Command / Result</span></div>
      {experiments.length ? <div className="experiment-list">{experiments.map((experiment, index) => {
        const key = `${round}:${experiment.process_id}`;
        const assessment = analysis?.experiment_assessments?.find(item => item.process_id === experiment.process_id);
        const open = expandedSet.has(key);
        return <ExperimentRow key={key} round={round} experiment={experiment} index={index} assessment={assessment} open={open} toggle={() => setManualToggles(previous => ({ ...previous, [key]: !open }))} />;
      })}</div> : <div className="inline-empty"><Terminal size={23} aria-hidden="true" /><p>{liveWatch ? "Waiting for the first command to launch in the sandbox." : "No process evidence was recorded."}</p></div>}
    </Panel>
  );

  const hypothesesPanel = (analysis?.hypotheses?.length ?? 0) > 0 && (
    <Panel title="Hypotheses considered" aside={<span className="quiet text-meta">Agent analysis</span>}><div className="hypotheses-list">{analysis?.hypotheses?.map((hypothesis, index) => <div className="hypothesis-row" key={index}>
      <div className="hypothesis-heading"><span className="annotation">H{index + 1}</span><Stamp hue={hypothesis.status === "supported" ? "green" : hypothesis.status === "rejected" ? "gray" : "amber"}>{hypothesis.status}</Stamp></div>
      <p><MarkdownInline text={hypothesis.statement} /></p>
      {references(hypothesis.evidence_process_ids, `Hypothesis ${index + 1} · ${hypothesis.status}`, hypothesis.statement, "Cited evidence")}
      {!!hypothesis.contradicting_process_ids?.length && references(hypothesis.contradicting_process_ids, `Evidence against hypothesis ${index + 1}`, hypothesis.statement, "Contradicting evidence")}
      {hypothesis.distinguishing_observation && <div className="hypothesis-distinction"><h3>What would distinguish this explanation</h3><p><MarkdownInline text={hypothesis.distinguishing_observation} /></p></div>}
    </div>)}</div></Panel>
  );

  const roundElapsed = liveWatch
    ? liveElapsed(latest.round_started_at ?? latest.started_at, undefined, now)
    : report.finished_at
      ? elapsed(report.round_started_at ?? report.started_at, report.finished_at)
      : !historical && latest.status === "paused"
        ? "Paused"
        : "Not recorded";

  return <>
    <PageTitle title={example ? "The eleven missing orders" : caseTitle(latest.case?.description ?? "Investigation")} entity back="Investigations"
      description={<div className="report-meta"><span className="annotation">{latest.run_id.slice(0, 8)}</span><time dateTime={latest.started_at}>{formatDate(latest.started_at)}</time>{active && <span className="annotation quiet">Live · round {latest.round ?? 1} · {liveElapsed(latest.round_started_at ?? latest.started_at, undefined, now)}</span>}</div>}
      action={example ? canRun ? <ButtonLink to="/new" variant="primary"><FlaskConical size={16} aria-hidden="true" />Investigate your own issue</ButtonLink> : null : canRun ? <div className="button-row">
        {active ? <>
          {lifecycle?.can_steer && !showSteer && <Button variant="primary" onClick={openSteerPanel}><Compass size={14} aria-hidden="true" />Steer investigator</Button>}
          {lifecycle?.can_pause && <Button onClick={() => pause.mutate()} disabled={pause.isPending}><Pause size={14} aria-hidden="true" />{pause.isPending ? "Requesting pause…" : "Pause investigation"}</Button>}
          <Button onClick={() => setConfirmCancel(true)} disabled={cancel.isPending || latest.status === "closing"}><Square size={14} aria-hidden="true" />Stop investigation</Button>
        </> : <>
          {lifecycle?.can_resume && !showResume && !showSteer && <Button variant="primary" onClick={() => setShowResume(true)}>{resumeLabel}</Button>}
          {lifecycle?.can_close && <Button onClick={() => setConfirmClose(true)}>Close investigation</Button>}
          {!lifecycle?.can_resume && !lifecycle?.can_close && !showSteer && <ButtonLink to="/new" variant="primary">New investigation</ButtonLink>}
        </>}
      </div> : null} />
    {example && <div className="example-notice"><File size={17} aria-hidden="true" /><p><strong>Example investigation.</strong> A saved run of the included missing-orders case, using a real agent and sandbox. The customer, orders, and log are fictional. The sync tool and its defect are real code.</p></div>}
    {historical && <div className="notice historical-notice" role="status"><div><strong>Viewing round {round}; latest is round {latest.round ?? 1}.</strong><p>Evidence and analysis below belong to this saved round. Execution controls apply to the current investigation.</p></div><Button onClick={() => selectRound(null)}>Return to latest round</Button></div>}
    <ReportStatusBand latest={latest} report={report} reproductionEvidence={analysis?.reproduced_process_ids?.length ? references(analysis.reproduced_process_ids, "Reproduction outcome", `${reproductionLabel(report.outcome)}. ${analysis.summary ?? ""}`, "Evidence for reproduction") : null} />
    {confirmCancel && active && <div className="notice" role="alert"><strong>Stop this investigation?</strong><p>The current run will end and cleanup will begin. Collected evidence will remain in the report.</p><div className="button-row"><Button onClick={() => { void cancel.mutateAsync().then(() => setConfirmCancel(false)).catch(() => {}); }} disabled={cancel.isPending}>{cancel.isPending ? "Stopping…" : "Stop and clean up"}</Button><Button variant="ghost" onClick={() => setConfirmCancel(false)}>Keep investigating</Button></div></div>}
    {cancel.isError && <ErrorPlate title="The stop request failed" error={cancel.error} />}
    {pause.isError && <ErrorPlate title="The pause request failed" error={pause.error} />}
    {close.isError && <ErrorPlate title="The close request failed" error={close.error} />}
    {lifecycle?.message && <div className="notice" role="status"><p>{lifecycle.message}</p></div>}
    {showSteer && canRun && <div className="report-resume" ref={steerRef}><SteerPanel report={latest} onCancel={() => setShowSteer(false)} onStarted={() => { setShowSteer(false); selectRound(null); }} /></div>}
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
    {!active && (latest.status === "running" || latest.status === "queued") && <div className="notice"><strong>This investigation was interrupted.</strong><p>The server is no longer managing this run. Inspect its saved evidence and {lifecycle?.can_close ? "close the investigation to clean up its execution resources." : !lifecycle && latest.cleanup ? "retry cleanup under Current resource cleanup if resources may still be present." : "use the CLI cleanup command if resources are still present."}</p></div>}
    {(report.warnings?.length ?? 0) > 0 && <div className="notice"><strong>Round {round} notes</strong><ul>{report.warnings?.map((warning, index) => <li key={index}>{warning}</li>)}</ul></div>}
    <RoundHistory latest={latest} selectedRound={selectedRound} onSelect={selectRound} />
    <div className="report-layout">
      <div className="section-stack">
        {liveWatch ? <>
          {Boolean(analysis?.summary) && briefPanel}
          <LiveWatchPanel
            report={latest}
            experiments={experiments}
            now={now}
            canSteer={Boolean(canRun && lifecycle?.can_steer && !showSteer)}
            onOpenSteer={openSteerPanel}
            onInspectProcess={processId => openEvidence(processId, round)}
          />
          {experimentPanel}
          {hypothesesPanel}
          {!analysis?.summary && briefPanel}
          {!example && <InvestigationChat key={latest.run_id} report={latest} canAsk={canRun} onReviewEvidence={setEvidence} onOpenEvidence={openEvidence} />}
        </> : <>
          {briefPanel}
          {!example && <InvestigationChat key={latest.run_id} report={latest} canAsk={canRun} onReviewEvidence={setEvidence} onOpenEvidence={openEvidence} />}
          {hypothesesPanel}
          {experimentPanel}
          {(report.tool_calls?.length ?? 0) > 0 && <ToolActivityArchive report={report} experiments={experiments} round={round} onInspectProcess={processId => openEvidence(processId, round)} />}
        </>}
        {(analysis?.reproduction_steps?.length ?? 0) > 0 && <Panel title="Reproduction steps" aside={<span className="quiet text-meta">Agent instructions</span>}><div className="panel-body"><ol className="prose-list numbered">{analysis?.reproduction_steps?.map((step, index) => <li key={index}><MarkdownInline text={step} /></li>)}</ol></div></Panel>}
        {!example && latest.ticket_note && <TicketNotePanel runID={latest.run_id} note={latest.ticket_note} canPost={canRun} />}
      </div>
      <aside className="report-aside section-stack">
        <Panel title={`Round ${round} record`}><div className="panel-body"><dl className="ledger">
          {report.agent && <div><dt>Agent</dt><dd className="mono run-agent-name">{report.agent.namespace}/{report.agent.name}</dd></div>}
          {report.sandbox_template && <div><dt>Sandbox</dt><dd className="mono">{report.sandbox_template}</dd></div>}
          <div><dt>{report.round_started_at ? "Round started" : "Started"}</dt><dd><time dateTime={report.round_started_at ?? report.started_at}>{formatDate(report.round_started_at ?? report.started_at)}</time></dd></div>
          <div><dt>Duration</dt><dd className="mono">{roundElapsed}</dd></div>
          <div><dt>Experiments</dt><dd className="mono">{experiments.length}</dd></div><div><dt>Original files</dt><dd className="mono">{attachments.length}</dd></div>
        </dl><div className="cleanup-record"><h3>Current resource cleanup</h3>{cleanupDone ? <p className="cleanup-success"><Check size={16} aria-hidden="true" />Sandbox and session deleted</p> : retained ? <p>Workspace and conversation retained until resumed or closed.</p> : active ? <p>Resources remain while work is active. Completion or closing attempts cleanup.</p> : latest.status === "paused" ? <p>Workspace retention is unconfirmed. Review the saved record and lifecycle messages.</p> : <p>Cleanup is not confirmed. Review the saved report and run messages.</p>}{!example && !active && !retained && !lifecycle && latest.cleanup && <CleanupControl report={latest} done={cleanupDone} canRetry={canRun} />}</div>
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

function CleanupControl({ report, done, canRetry }: { report: InvestigationReport; done: boolean; canRetry: boolean }) {
  const cleanup = useCleanupRun(report.run_id);
  const running = cleanup.isPending || report.cleanup_running === true;
  return <div className="cleanup-actions">
    {running && <p role="status">Cleaning up. This can take up to 90 seconds.</p>}
    {!running && !done && canRetry && <Button onClick={() => cleanup.mutate()}>Retry cleanup</Button>}
    {!running && done && cleanup.isSuccess && <p role="status">Cleanup finished.</p>}
    {!running && !done && cleanup.isSuccess && !report.cleanup_error && <p role="status">Cleanup ran but is still not confirmed. If it keeps failing, use the CLI: <code>triagelab cleanup --report reports/{report.run_id}/report.json</code> with the same configuration.</p>}
    {report.cleanup_error && <p role="alert">{report.cleanup_error}</p>}
    {cleanup.isError && <p role="alert">{cleanup.error instanceof Error ? cleanup.error.message : "Cleanup could not be started."}</p>}
  </div>;
}

function TicketNotePanel({ runID, note, canPost }: { runID: string; note: TicketNote; canPost: boolean }) {
  const [reviewedPlan, setReviewedPlan] = useState<string>();
  const [draft, setDraft] = useState("");
  const [draftError, setDraftError] = useState("");
  const review = !!reviewedPlan && reviewedPlan === note.plan;
  const post = usePostTicketNote(runID);
  const github = note.source === "github";
  const gitlab = note.source === "gitlab";
  const linear = note.source === "linear";
  const bondren = note.source === "bondren";
  const jira = note.source === "jira";
  const issue = github || gitlab || linear;
  const provider = github ? "GitHub" : gitlab ? "GitLab" : linear ? "Linear" : bondren ? "Bondren" : jira ? "Jira" : "Zendesk";
  const noun = issue ? "comment" : "internal note";
  const held = note.status === "blocked";

  function beginReview() {
    setDraft(note.body ?? "");
    setDraftError("");
    setReviewedPlan(note.plan);
  }

  function submit() {
    if (!note.plan) return;
    const body = draft === note.body ? undefined : draft;
    if (body !== undefined && body.trim() === "") {
      setDraftError("Write the comment text before posting.");
      return;
    }
    if (body !== undefined && new TextEncoder().encode(body).length > 16384) {
      setDraftError("Keep the edited text under 16 KiB.");
      return;
    }
    setDraftError("");
    post.mutate({ plan: note.plan, body });
  }

  const editor = review && <>
    <div className="handoff-preview">
      <h3>Exact text to be posted</h3>
      <label className="field-label" htmlFor="ticket-note-body">Edit before posting</label>
      <Textarea id="ticket-note-body" value={draft} rows={10} disabled={post.isPending}
        onChange={event => setDraft(event.target.value)} />
      <p className="field-help">The exact text above is posted to {provider}. Edits are held for review by the output sanitizer, never rewritten.</p>
    </div>
    {held && note.findings && <ul className="prose-list">{note.findings.map((finding, index) => <li key={index}>{finding}</li>)}</ul>}
    {held && draft === note.body && <p className="field-help">Resolve the finding above before posting: the held text cannot be posted unchanged.</p>}
    {draftError && <p className="field-error" role="alert">{draftError}</p>}
    <div className="button-row">
      <Button disabled={!note.can_post || post.isPending || (held && draft === note.body)} onClick={submit}>
        {post.isPending ? "Posting…" : issue ? "Post comment" : "Post internal note"}
      </Button>
      <Button variant="ghost" disabled={post.isPending} onClick={() => { post.reset(); setReviewedPlan(undefined); }}>Cancel</Button>
    </div>
    {!note.can_post && <p>{github ? "GitHub posting is unavailable. Review the repository connection in " : gitlab ? "GitLab posting is unavailable. Review the connection in " : linear ? "Linear posting is unavailable. Review the connection in " : bondren || jira ? `${provider} posting is unavailable. Review the token in ` : "Connect Zendesk from "}<Link className="ticket-source" to="/tickets">Ticket cases</Link>{issue || bondren || jira ? "." : " to post."}</p>}
  </>;

  return <Panel title={issue ? `${provider} investigation comment` : bondren || jira ? `${provider} internal note` : "Zendesk internal note"}>
    <div className="panel-body ticket-detail">
      <dl className="handoff-details">
        <div><dt>Destination</dt><dd><a className="ticket-source" href={note.ticket_url} target="_blank" rel="noreferrer">{provider} #{note.ticket_id} ↗</a></dd></div>
        <div><dt>Audience</dt><dd>{github ? "Everyone with repository access; public in a public repository." : gitlab ? "Everyone with access to the issue and its project." : linear ? "People with access to this Linear workspace." : bondren ? "The internal team on this Bondren issue." : jira ? "The internal team on this Jira issue." : "Agents with access to this Zendesk ticket; an internal note."}</dd></div>
        <div><dt>Content</dt><dd>Run outcome and the experiment evidence included in the prepared note. Review and edit the exact text before posting.</dd></div>
      </dl>
      {note.status === "posted" && <p role="status">Delivery confirmed · posted as {provider} {noun} {note.comment_id}{note.automatic ? " automatically by the procedure policy." : "."}</p>}
      {note.status === "unconfirmed" && <p role="status">Delivery is unconfirmed. Check the {provider} conversation for run <code>{runID}</code> before attempting another post.</p>}
      {held && <>
        <p role="status">The output sanitizer held this {noun} for review.</p>
        {!review && note.findings && <ul className="prose-list">{note.findings.map((finding, index) => <li key={index}>{finding}</li>)}</ul>}
        {!review && <p className="ticket-draft">{note.body}</p>}
        {!review
          ? <div className="button-row">{canPost && <Button disabled={!note.plan} onClick={beginReview}>Edit before posting</Button>}</div>
          : editor}
      </>}
      {note.status === "ready" && <>
        <p>{issue ? "Share the outcome and selected experiment evidence on this issue." : "Share the outcome and selected experiment evidence with agents on this ticket."}</p>
        {note.automatic && <p role="status">The procedure policy posts this {noun} automatically when the run completes. Automatic posting has not completed, so review, edit, and post it below.</p>}
        {!review
          ? <div className="button-row">{canPost && <Button disabled={!note.plan} onClick={beginReview}>{issue ? "Review comment" : "Review internal note"}</Button>}</div>
          : editor}
      </>}
      {post.isError && <ErrorPlate title={issue ? "Comment could not be posted" : "Internal note could not be posted"} error={post.error} />}
    </div>
  </Panel>;
}

function ActivityIcon({ kind }: { kind: WatchActivityItem["kind"] }) {
  switch (kind) {
    case "command": return <Terminal size={15} aria-hidden="true" />;
    case "read_file": return <FileCode size={15} aria-hidden="true" />;
    case "write_file": return <FileOutput size={15} aria-hidden="true" />;
    case "kill_process": return <Square size={14} aria-hidden="true" />;
    case "poll_group": return <RefreshCw size={14} aria-hidden="true" />;
    default: return <Wrench size={14} aria-hidden="true" />;
  }
}

function ActivityRow({ item, onInspectProcess }: { item: WatchActivityItem; onInspectProcess: (processId: string) => void }) {
  return <li className={`watch-activity-item watch-activity-${item.kind}`}>
    <span className="watch-activity-icon"><ActivityIcon kind={item.kind} /></span>
    <div className="watch-activity-body">
      <div className="watch-activity-top">
        <span className="watch-activity-label">{item.label}</span>
        {item.meta && <span className="annotation quiet">{item.meta}</span>}
      </div>
      <code className="watch-activity-detail">{item.detail}</code>
      {item.errorText && <p className="watch-activity-error">{item.errorText}</p>}
    </div>
    <div className="watch-activity-actions">
      <Stamp hue={item.tone}>{item.statusLabel}</Stamp>
      {item.processId && item.experimentNumber !== undefined && <Button variant="ghost" className="compact-action" onClick={() => onInspectProcess(item.processId!)}>Output</Button>}
    </div>
  </li>;
}

function LiveWatchPanel({ report, experiments, now, canSteer, onOpenSteer, onInspectProcess }: {
  report: InvestigationReport;
  experiments: Experiment[];
  now: number;
  canSteer: boolean;
  onOpenSteer: () => void;
  onInspectProcess: (processId: string) => void;
}) {
  const activity = useMemo(() => buildWatchActivity(report.tool_calls, experiments), [report.tool_calls, experiments]);
  const progress = report.progress?.length ? report.progress : ["Preparing the investigation…"];
  const streamRef = useRef<HTMLOListElement>(null);
  const followLatest = useRef(true);
  const lifecycle = report.lifecycle;
  const maxExperiments = lifecycle?.max_experiments ?? 0;

  useEffect(() => {
    if (streamRef.current && followLatest.current) {
      streamRef.current.scrollTop = streamRef.current.scrollHeight;
    }
  }, [activity.length]);

  return <Panel title="Live investigator watch" aside={<div className="panel-aside-group">
    <span className="annotation quiet">Round {report.round ?? 1} · {liveElapsed(report.round_started_at ?? report.started_at, undefined, now)}</span>
    <span className="annotation quiet">{experiments.length}{maxExperiments > 0 ? `/${maxExperiments}` : ""} cmds</span>
    <StatusStamp status={report.status} active />
  </div>}>
    <div className="panel-body live-watch-body">
      <div className="watch-status-bar">
        <div>
          <strong className="watch-phase">{taskStateLabel(report.task_state, report.status, lifecycle?.phase)}</strong>
          <p className="watch-latest-note">{lifecycle?.message || progress[progress.length - 1]}</p>
          {report.task_failure && <p className="watch-activity-error">{report.task_failure}</p>}
        </div>
        {canSteer && <Button variant="secondary" onClick={onOpenSteer}><Compass size={14} aria-hidden="true" />Steer agent</Button>}
      </div>

      <div className="watch-subsection">
        <div className="watch-subsection-head">
          <h3>Tool &amp; workspace activity</h3>
          <span className="annotation quiet">{activity.length ? `${activity.length} recorded` : "Waiting for first action"}</span>
        </div>
        {activity.length > 0 ? <ol
          ref={streamRef}
          className="watch-activity-list"
          tabIndex={0}
          aria-label="Live tool and command activity"
          aria-live="polite"
          aria-relevant="additions"
          onScroll={event => {
            const el = event.currentTarget;
            followLatest.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
        >
          {activity.map(item => <ActivityRow key={item.id} item={item} onInspectProcess={onInspectProcess} />)}
        </ol> : <p className="quiet text-meta">No tool calls recorded in this turn yet. Commands and file operations appear here as they start.</p>}
      </div>

      <div className="watch-subsection">
        <div className="watch-subsection-head">
          <h3>Lifecycle events</h3>
          <span className="annotation quiet">{progress.length}</span>
        </div>
        <ol className="progress-list" tabIndex={0} aria-label="Investigation progress" aria-live="polite" aria-relevant="additions">
          {progress.slice(-6).map((entry, index) => <li key={`${index}-${entry}`}><span className="progress-dot" />{entry}</li>)}
        </ol>
      </div>
    </div>
  </Panel>;
}

function ToolActivityArchive({ report, experiments, round, onInspectProcess }: {
  report: InvestigationReport;
  experiments: Experiment[];
  round: number;
  onInspectProcess: (processId: string) => void;
}) {
  const activity = useMemo(() => buildWatchActivity(report.tool_calls, experiments), [report.tool_calls, experiments]);
  if (!activity.length) return null;
  return <Panel title="Tool & workspace activity" aside={<span className="annotation quiet">Round {round} · {activity.length} actions</span>}>
    <div className="panel-body">
      <details className="tool-archive-details">
        <summary>Inspect {activity.length} recorded tool action{activity.length === 1 ? "" : "s"}</summary>
        <ol className="watch-activity-list">
          {activity.map(item => <ActivityRow key={item.id} item={item} onInspectProcess={onInspectProcess} />)}
        </ol>
      </details>
    </div>
  </Panel>;
}

function SteerPanel({ report, onCancel, onStarted }: { report: InvestigationReport; onCancel: () => void; onStarted: () => void }) {
  const [review] = useState(report.lifecycle);
  const settings = useSettings();
  const steer = useSteerRun(report.run_id);
  const [description, setDescription] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [inputError, setInputError] = useState("");
  const active = Boolean(report.active && report.lifecycle?.can_steer);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const limits = settings.data;
    if (!limits || !review) return;
    if (!description.trim()) { setInputError("Write guidance for the investigator before sending."); return; }
    if (new TextEncoder().encode(description).length > limits.max_description_bytes) { setInputError(`Keep guidance under ${fileSize(limits.max_description_bytes)}.`); return; }
    if (files.length > limits.max_files || files.some(file => file.size > limits.max_file_bytes) || files.reduce((total, file) => total + file.size, 0) > limits.max_total_bytes) {
      setInputError(`Choose up to ${limits.max_files} files, ${fileSize(limits.max_file_bytes)} each and ${fileSize(limits.max_total_bytes)} combined.`);
      return;
    }
    setInputError("");
    try {
      await steer.mutateAsync({ description, files, round: review.round, settings: review.resume_settings });
      onStarted();
    } catch { /* Keep the operator's guidance available on failure. */ }
  }

  return <Panel title={`Steer investigator · round ${review?.round ?? report.round ?? 1}`} aside={<Stamp hue="blue">In-flight redirect</Stamp>}><form className="panel-body section-stack" onSubmit={submit}>
    <p>Interrupt the current turn, stop any still-running commands, and start round {(review?.round ?? report.round ?? 1) + 1} immediately in the same sandbox workspace with your guidance.</p>
    {!active && <div className="notice" role="status"><strong>This turn is no longer steerable.</strong><p>Dismiss this panel or copy your guidance to resume once the round settles.</p></div>}
    {review && <dl className="ledger">
      <div><dt>Agent</dt><dd className="mono">{report.agent?.namespace}/{report.agent?.name}</dd></div>
      <div><dt>Sandbox</dt><dd className="mono">{report.sandbox_template}</dd></div>
      <div><dt>Next round budget</dt><dd>{review.timeout_seconds} seconds / {review.max_experiments} experiments</dd></div>
    </dl>}
    <div><label className="field-label" htmlFor="steer-description">Guidance for the investigator</label><Textarea id="steer-description" value={description} onChange={event => setDescription(event.target.value)} rows={4} required disabled={steer.isPending || !active} placeholder="Stop checking lock retries; focus on cursor advancement in rowferry.py and test created_at,id." /></div>
    <div><label className="field-label" htmlFor="steer-files">Additional files (optional)</label><input className="field" id="steer-files" type="file" multiple disabled={steer.isPending || !active} onChange={event => setFiles(Array.from(event.target.files ?? []))} />{settings.data && <p className="field-help below-field">{fileSize(settings.data.max_file_bytes)} per file / {fileSize(settings.data.max_total_bytes)} total. Uploaded under the new round directory in /data/workspace.</p>}{files.length > 0 && <ul className="prose-list">{files.map((file, index) => <li key={`${file.name}-${index}`}>{file.name} · {fileSize(file.size)}</li>)}</ul>}</div>
    {settings.isError && <ErrorPlate title="Input limits could not load" error={settings.error} />}
    {inputError && <p className="field-error" role="alert">{inputError}</p>}
    {steer.isError && <ErrorPlate title="Steering could not be queued" error={steer.error} />}
    <div className="button-row"><Button type="submit" variant="primary" disabled={steer.isPending || !settings.data || !active || !review}>{steer.isPending ? "Queuing guidance…" : "Send guidance"}</Button><Button type="button" variant="ghost" disabled={steer.isPending} onClick={onCancel}>Dismiss</Button></div>
  </form></Panel>;
}

function ExperimentRow({ experiment, round, index, assessment, open, toggle }: { experiment: Experiment; round: number; index: number; assessment?: ExperimentAssessment; open: boolean; toggle: () => void }) {
  return <details className="experiment" open={open} id={`evidence-${round}-${experiment.process_id}`}>
    <summary onClick={event => { event.preventDefault(); toggle(); }}><span className="experiment-number">{String(index + 1).padStart(2, "0")}</span><code>{commandText(experiment.command)}</code><span className="experiment-exit"><ProcessResult experiment={experiment} /></span><ChevronDown size={16} className="disclosure-chevron" aria-hidden="true" /></summary>
    <div className="experiment-content"><ProcessEvidence experiment={experiment} index={index} />{assessment && <Assessment assessment={assessment} />}</div>
  </details>;
}
