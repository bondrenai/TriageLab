import { useEffect, useId, useRef } from "react";
import { X } from "lucide-react";
import type { Experiment, ExperimentAssessment, InvestigationReport } from "../lib/types";
import { MarkdownBlocks, MarkdownInline } from "./markdown";
import { Button, Stamp } from "./ui";

export interface EvidenceContext {
  title: string;
  statement: string;
  ids: string[];
}

export interface EvidenceSelection extends EvidenceContext {
  report: InvestigationReport;
}

export function commandText(command: string[]) {
  return command.map(argument => /\s|["'\\]/.test(argument) || !argument ? JSON.stringify(argument) : argument).join(" ");
}

export function ProcessResult({ experiment }: { experiment: Experiment }) {
  if (typeof experiment.exit_code === "number") {
    return <span className="process-result-stamps">
      <Stamp hue={experiment.exit_code === 0 ? "green" : "amber"}>Exit {experiment.exit_code}</Stamp>
      {experiment.exit_code === 0 && Boolean(experiment.stderr?.trim()) && <Stamp hue="amber">stderr</Stamp>}
    </span>;
  }
  if (experiment.state === "PROCESS_STATE_RUNNING") {
    return <Stamp hue="blue">Running</Stamp>;
  }
  if (experiment.stopped_for_pause) {
    return <Stamp hue="amber">Stopped</Stamp>;
  }
  return <Stamp>{experiment.state?.replace(/^PROCESS_STATE_/, "").toLowerCase() || "State unavailable"}</Stamp>;
}

export function EvidenceReferences({ context, report, onReview, onOpenEvidence, label = "Cited experiments" }: {
  context: EvidenceContext;
  report: InvestigationReport;
  onReview: (selection: EvidenceSelection) => void;
  onOpenEvidence: (id: string) => void;
  label?: string;
}) {
  const experiments = report.experiments ?? [];
  const ids = [...new Set(context.ids)];
  if (!ids.length) return <p className="evidence-absence">No process evidence cited.</p>;
  return <div className="evidence-reference-group">
    <p className="evidence-label">{label}</p>
    <div className="evidence-reference-actions">
      <span className="evidence-refs">{ids.map(id => {
        const index = experiments.findIndex(item => item.process_id === id);
        return index >= 0
          ? <a key={id} href={`#evidence-${report.round ?? 1}-${encodeURIComponent(id)}`} onClick={() => onOpenEvidence(id)}>Experiment {index + 1}</a>
          : <span key={id} className="annotation">Unavailable process: {id}</span>;
      })}</span>
      <Button variant="ghost" onClick={() => onReview({ ...context, ids, report })} aria-haspopup="dialog">Review sources</Button>
    </div>
  </div>;
}

export function Assessment({ assessment }: { assessment: ExperimentAssessment }) {
  return <div className="experiment-assessment">
    <h4>Agent interpretation</h4>
    <dl>
      <div><dt>Question tested</dt><dd><MarkdownInline text={assessment.question} /></dd></div>
      <div><dt>Expected observation</dt><dd><MarkdownInline text={assessment.expected_observation} /></dd></div>
      <div><dt>Interpretation</dt><dd><MarkdownInline text={assessment.interpretation} /></dd></div>
    </dl>
  </div>;
}

export function ProcessEvidence({ experiment, index, showCommand = false }: { experiment: Experiment; index: number; showCommand?: boolean }) {
  const isRunning = experiment.state === "PROCESS_STATE_RUNNING";
  const streaming = isRunning && experiment.outputs_complete !== true;
  const envEntries = Object.entries(experiment.env ?? {});
  const emptyStdout = streaming ? "Waiting for standard output…" : experiment.outputs_complete === true ? "No standard output." : "Output not collected.";
  const emptyStderr = streaming ? "Waiting for standard error…" : experiment.outputs_complete === true ? "No standard error." : "Output not collected.";
  const showIncompleteWarning = !isRunning && experiment.outputs_complete === false;
  return <>
    {showCommand && <><div className="output-label">Recorded command</div><pre className="process-output command-output" tabIndex={0} aria-label={`Command for experiment ${index + 1}`}>{commandText(experiment.command)}</pre></>}
    <div className="process-context">
      <span className="annotation">Process {experiment.process_id}</span>
      {experiment.cwd && <span className="annotation">Directory {experiment.cwd}</span>}
      <span className="annotation">{experiment.state?.replace(/^PROCESS_STATE_/, "").toLowerCase() || "State unavailable"}</span>
      {envEntries.length > 0 && <span className="annotation">Env {envEntries.map(([k, v]) => `${k}=${v}`).join(" ")}</span>}
    </div>
    <div className="output-label">Standard output{streaming && <span className="annotation live-tag"> · live</span>}</div>
    <pre className="process-output" tabIndex={0} role="region" aria-label={`Standard output for experiment ${index + 1}`}>{experiment.stdout || emptyStdout}</pre>
    <div className="output-label">Standard error{streaming && <span className="annotation live-tag"> · live</span>}</div>
    <pre className="process-output stderr" tabIndex={0} role="region" aria-label={`Standard error for experiment ${index + 1}`}>{experiment.stderr || emptyStderr}</pre>
    {(experiment.timed_out || experiment.stopped_for_pause || experiment.truncated || showIncompleteWarning || experiment.outputs_complete === undefined) && <p className="output-warning">{experiment.timed_out ? "The process exceeded its time limit. " : ""}{experiment.stopped_for_pause ? "Stopped early to pause or redirect this round. " : ""}{experiment.truncated ? "Output was truncated. " : ""}{showIncompleteWarning ? "Output collection is incomplete." : experiment.outputs_complete === undefined && !isRunning ? "Output completeness was not recorded." : ""}</p>}
  </>;
}

export function EvidenceInspector({ selection, onClose, onOpenRegister }: {
  selection: EvidenceSelection;
  onClose: () => void;
  onOpenRegister: (id: string, round: number) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const headingId = useId();
  const report = selection.report;
  const round = report.round ?? 1;
  const experiments = report.experiments ?? [];
  useEffect(() => {
    const previous = document.activeElement;
    const element = dialog.current;
    element?.showModal();
    return () => {
      element?.close();
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus({ preventScroll: true });
    };
  }, []);

  return <dialog ref={dialog} className="evidence-dialog" aria-labelledby={headingId} onCancel={event => { event.preventDefault(); onClose(); }}>
    <header className="evidence-dialog-header rule-cut"><div><p className="annotation">Round {round} · saved process evidence</p><h2 id={headingId}>Review evidence</h2></div><Button variant="ghost" onClick={onClose} aria-label="Close evidence review"><X size={18} aria-hidden="true" /></Button></header>
    <div className="evidence-dialog-layout">
      <section className="evidence-claim"><h3>{selection.title}</h3><MarkdownBlocks className="evidence-claim-statement" text={selection.statement} /><p className="analysis-note">This statement is agent analysis. The recorded process output is collected separately.</p></section>
      <div className="evidence-inspector-processes">{selection.ids.map(id => {
        const index = experiments.findIndex(item => item.process_id === id);
        const experiment = experiments[index];
        const assessment = report.analysis?.experiment_assessments?.find(item => item.process_id === id);
        return <section key={id} className="evidence-inspector-process" aria-label={experiment ? `Recorded experiment ${index + 1}` : "Unavailable evidence"}>
          {experiment ? <>
            <div className="evidence-process-heading"><h3>Experiment {index + 1}</h3><ProcessResult experiment={experiment} /></div>
            <ProcessEvidence experiment={experiment} index={index} showCommand />
            {assessment && <Assessment assessment={assessment} />}
            <a className="ticket-source evidence-register-link" href={`#evidence-${round}-${encodeURIComponent(id)}`} onClick={() => { onClose(); onOpenRegister(id, round); }}>Open in experiment register</a>
          </> : <><h3>Evidence unavailable</h3><p>No saved process record matches <code>{id}</code> in round {round}. This reference cannot be reviewed.</p></>}
        </section>;
      })}</div>
    </div>
  </dialog>;
}
