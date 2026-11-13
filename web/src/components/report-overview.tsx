import type { ReactNode } from "react";
import type { InvestigationReport } from "../lib/types";
import { formatDate } from "../lib/investigations";
import { MarkdownBlocks, MarkdownInline } from "./markdown";
import { Button, Panel, Stamp, type StampHue } from "./ui";

export function reproductionLabel(outcome: string) {
  return outcome === "reproduced" ? "Reproduced in sandbox" : outcome === "not_reproduced" ? "Not reproduced" : outcome === "inconclusive" ? "Inconclusive" : "Not established";
}

function executionState(report: InvestigationReport): { label: string; hue: StampHue } {
  if (!report.active && report.lifecycle?.phase === "invoking") return { label: "Continuation uncertain", hue: "amber" };
  if (["pausing", "resuming", "closing"].includes(report.status)) return { label: report.status, hue: report.active ? "blue" : "amber" };
  if (report.active) return { label: report.status === "queued" ? "Queued" : "Investigating", hue: "blue" };
  if (["running", "queued"].includes(report.status)) return { label: "Interrupted", hue: "amber" };
  if (report.status === "paused") return { label: "Paused", hue: "amber" };
  return { label: report.status.replaceAll("_", " ") || "Unknown", hue: report.status === "error" ? "red" : "gray" };
}

export function hasRetainedWorkspace(report: InvestigationReport) {
  return !report.active && report.lifecycle?.phase === "paused"
    && report.cleanup?.sandbox_deleted !== true && report.cleanup?.session_deleted !== true;
}

export function ReportStatusBand({ latest, report, reproductionEvidence }: { latest: InvestigationReport; report: InvestigationReport; reproductionEvidence: ReactNode }) {
  const execution = executionState(latest);
  const deleted = latest.cleanup?.sandbox_deleted === true && latest.cleanup?.session_deleted === true;
  const retained = hasRetainedWorkspace(latest);
  const resource = deleted ? "Deleted" : retained ? "Retained" : latest.active ? "Managed by active run" : latest.status === "paused" ? "Retention unconfirmed" : "Cleanup unconfirmed";
  const resourceHint = deleted ? "Sandbox and session deletion confirmed." : retained ? "Workspace and conversation remain until resumed or closed." : latest.active ? "Completion or closing attempts cleanup." : latest.status === "paused" ? "Review the saved record and lifecycle messages before continuing." : "Review the run messages and saved cleanup record.";
  return <section className="plate report-status-band" aria-label="Investigation state">
    <div><h2>Execution · current</h2><Stamp hue={execution.hue}>{execution.label}</Stamp><p>Round {latest.round ?? 1}</p></div>
    <div><h2>Reproduction · round {report.round ?? 1}</h2><Stamp hue={report.outcome === "reproduced" ? "green" : "amber"}>{reproductionLabel(report.outcome)}</Stamp><p>Scoped to the recorded sandbox experiments.</p>{reproductionEvidence}</div>
    <div><h2>Resources · current</h2><strong>{resource}</strong><p>{resourceHint}</p></div>
  </section>;
}

function BriefList({ items }: { items: string[] }) {
  return <><ul className="prose-list">{items.slice(0, 3).map((item, index) => <li key={index}><MarkdownInline text={item} /></li>)}</ul>{items.length > 3 && <details className="brief-more"><summary>Show {items.length - 3} more</summary><ul className="prose-list">{items.slice(3).map((item, index) => <li key={index}><MarkdownInline text={item} /></li>)}</ul></details>}</>;
}

export function InvestigationBrief({ report, active, leadingEvidence }: { report: InvestigationReport; active: boolean; leadingEvidence: ReactNode }) {
  const analysis = report.analysis;
  const observations = analysis?.observations ?? [];
  const blockers = analysis?.missing_information ?? [];
  const next = analysis?.next_actions ?? [];
  return <Panel title="Investigation brief" aside={<span className="quiet text-meta">Agent analysis · round {report.round ?? 1}</span>}>
    <div className="panel-body investigation-brief">
      {analysis?.summary ? <MarkdownBlocks className="finding-summary" text={analysis.summary} /> : <p className="finding-summary">{active ? "The investigator is gathering evidence. Findings will appear when a validated analysis is saved." : "No validated analysis was recorded. Review the process evidence and run messages."}</p>}
      {analysis?.root_cause?.statement && <div className="brief-leading"><h3>Leading hypothesis</h3><p><MarkdownInline text={analysis.root_cause.statement} /></p>{leadingEvidence}</div>}
      {observations.length > 0 && <section><h3>Agent-reported observations</h3><BriefList items={observations} /></section>}
      {(blockers.length > 0 || next.length > 0) && <div className="brief-next-grid">
        {blockers.length > 0 && <section><h3>Information needed to continue</h3><BriefList items={blockers} /></section>}
        {next.length > 0 && <section><h3>Suggested next actions</h3><BriefList items={next} /></section>}
      </div>}
      {analysis && <p className="analysis-note">The agent’s observations and explanations are interpretations. A reproduced symptom in the sandbox does not confirm the cause in the reported environment.</p>}
    </div>
  </Panel>;
}

export function RoundHistory({ latest, selectedRound, onSelect }: { latest: InvestigationReport; selectedRound: number | null; onSelect: (round: number | null) => void }) {
  const rounds = [...(latest.rounds ?? []).map((report, index) => ({ report, round: report.round ?? index + 1, latest: false })), { report: latest, round: latest.round ?? 1, latest: true }];
  if (rounds.length < 2) return null;
  return <details className="plate round-history">
    <summary>Investigation rounds <span className="annotation">{rounds.length} recorded · viewing {selectedRound ?? latest.round ?? 1}</span></summary>
    <ol>{rounds.map(item => {
      const selected = (selectedRound ?? latest.round ?? 1) === item.round;
      const input = item.report.continuation;
      const blockers = item.report.analysis?.missing_information ?? [];
      const experiments = item.report.experiments ?? [];
      return <li key={item.round} aria-current={selected ? "step" : undefined}>
        <div className="round-history-heading"><div><h3>Round {item.round}{item.latest ? " · latest" : ""}</h3><time dateTime={item.report.round_started_at ?? item.report.started_at}>{formatDate(item.report.round_started_at ?? item.report.started_at)}</time></div><Button variant="ghost" onClick={() => onSelect(item.latest ? null : item.round)} disabled={selected}>{selected ? "Viewing" : `View round ${item.round}`}</Button></div>
        <p className="text-meta quiet">{experiments.length} recorded experiment{experiments.length === 1 ? "" : "s"} · {reproductionLabel(item.report.outcome)}</p>
        {input ? <div className="round-input"><h4>Information added</h4><MarkdownBlocks className="round-text" text={input.description} /><span className="annotation">{input.attachments?.length ?? 0} additional files</span></div> : <p className="text-meta quiet">Original case · {item.report.case?.attachments?.length ?? 0} supplied files</p>}
        {item.report.analysis?.summary && <div className="round-analysis"><h4>Saved agent analysis</h4><MarkdownBlocks className="round-text" text={item.report.analysis.summary} /></div>}
        {blockers.length > 0 && <details className="brief-more"><summary>{blockers.length} blocking question{blockers.length === 1 ? "" : "s"} recorded</summary><ul className="prose-list">{blockers.map((question, index) => <li key={index}><MarkdownInline text={question} /></li>)}</ul></details>}
      </li>;
    })}</ol>
    <p className="analysis-note">Each round preserves its own inputs, analysis, and process evidence. Earlier findings are historical.</p>
  </details>;
}
