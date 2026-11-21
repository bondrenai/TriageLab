import { useState } from "react";
import { ArrowRight, FileCheck2, FlaskConical, Plus } from "lucide-react";
import { Link } from "react-router-dom";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Stamp, StatusStamp } from "../components/ui";
import { useRuns } from "../hooks/use-investigations";
import { useSession } from "../hooks/use-auth";
import { APIError } from "../lib/api-client";
import { roleAtLeast } from "../lib/auth";
import { filterRunHistory, type RunHistoryFilter } from "../lib/intake";
import { caseTitle, exampleReport, formatDate } from "../lib/investigations";
import type { RunSummary } from "../lib/types";

const resourceLabels: Record<RunSummary["resource_state"], string> = {
  active: "Resources managed by active run",
  queued: "Waiting in workspace queue",
  retained: "Workspace retained",
  deleted: "Sandbox and session deleted",
  not_created: "No execution resources created",
  unconfirmed: "Cleanup not confirmed",
};

const historyFilters: { id: RunHistoryFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "reproduced", label: "Reproduced" },
  { id: "not_reproduced", label: "Not reproduced" },
  { id: "inconclusive", label: "Inconclusive" },
  { id: "attention", label: "Needs attention" },
];

function RunGroup({ title, runs, canRun }: { title: string; runs: RunSummary[]; canRun: boolean }) {
  if (!runs.length) return null;
  return <section className="plate table-plate" aria-label={title}>
    <div className="table-caption"><h2>{title}</h2><span className="annotation">{runs.length}</span></div>
    <div className="table-scroll"><table>
      <thead><tr><th>Case</th><th>Status</th><th>Next step</th><th>Experiments</th><th>Started</th></tr></thead>
      <tbody>{runs.map(run => <tr key={run.id}>
        <td><Link className="run-link" to={`/runs/${run.id}`}>{caseTitle(run.description)}</Link><span className="run-id annotation">{run.id.slice(0, 8)} · Round {run.round}{run.queued && run.queue_position ? ` · Queue #${run.queue_position}` : ""}</span></td>
        <td><StatusStamp status={run.status} outcome={run.outcome} active={run.active} queued={run.queued} /><span className="run-id text-meta quiet">{resourceLabels[run.resource_state]}</span></td>
        <td>{run.attention && <p className="text-meta">{run.attention.reason}</p>}<Link className="run-link" to={`/runs/${run.id}`}>{run.active ? "Follow investigation" : run.queued ? "View queued run" : canRun ? run.attention?.action ?? "Review report" : "Review report"}<span className="sr-only">: {caseTitle(run.description)}</span></Link></td>
        <td className="mono">{run.experiment_count}</td>
        <td><time dateTime={run.started_at}>{formatDate(run.started_at)}</time></td>
      </tr>)}</tbody>
    </table></div>
  </section>;
}

export function InvestigationsPage() {
  const runs = useRuns();
  const session = useSession();
  const canRun = roleAtLeast(session.data?.role, "user");
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<RunHistoryFilter>("all");
  const denied = runs.error instanceof APIError && (runs.error.status === 401 || runs.error.status === 403);
  const confirmed = denied ? undefined : runs.data?.runs;
  const filtered = confirmed ? filterRunHistory(confirmed, search, filter) : undefined;
  const retry = <Button onClick={() => void runs.refetch()} disabled={runs.isFetching}>Try again</Button>;

  return <>
    <PageTitle title="Investigations" description="Follow active work, answer blocking questions, and inspect the evidence." action={canRun ? <ButtonLink to="/new" variant="primary"><Plus size={16} aria-hidden="true" />New investigation</ButtonLink> : undefined} />
    {denied ? <ErrorPlate title="Investigations are unavailable" error="Your current session does not have access to these investigations." />
      : !confirmed ? runs.isPending ? <Loading label="Loading investigations…" /> : <ErrorPlate title="Investigations could not load" error={runs.error} action={retry} />
      : <>
        {runs.isError && <ErrorPlate title="Investigation refresh failed" error={`Showing the last confirmed history from ${formatDate(new Date(runs.dataUpdatedAt).toISOString())}. Run status and available actions may have changed.`} action={retry} />}
        {confirmed.length ? <div className="section-stack">
          <div className="history-toolbar" role="search" aria-label="Filter investigations">
            <input
              type="search"
              className="field history-search"
              placeholder="Search by description, run ID, status, or outcome…"
              aria-label="Search investigations"
              value={search}
              onChange={event => setSearch(event.target.value)}
            />
            <div className="filter-chips" role="group" aria-label="Outcome filter">
              {historyFilters.map(item => (
                <button
                  key={item.id}
                  type="button"
                  className={filter === item.id ? "filter-chip active" : "filter-chip"}
                  aria-pressed={filter === item.id}
                  onClick={() => setFilter(item.id)}
                >
                  {item.label}
                </button>
              ))}
            </div>
          </div>
          {filtered && filtered.length > 0 ? <>
            <RunGroup title="Active investigation" runs={filtered.filter(run => run.active)} canRun={canRun} />
            <RunGroup title="Queued investigations" runs={filtered.filter(run => run.queued).sort((a, b) => (a.queue_position ?? 0) - (b.queue_position ?? 0))} canRun={canRun} />
            <RunGroup title="Needs attention" runs={filtered.filter(run => !run.active && !run.queued && run.attention)} canRun={canRun} />
            <RunGroup title="Recent reports" runs={filtered.filter(run => !run.active && !run.queued && !run.attention)} canRun={canRun} />
          </> : <section className="plate empty-state">
            <div className="empty-copy">
              <h2>No matching investigations.</h2>
              <p>Try clearing the search query or switching the filter back to All.</p>
              <div className="button-row"><Button type="button" onClick={() => { setSearch(""); setFilter("all"); }}>Reset filters</Button></div>
            </div>
          </section>}
        </div> : <section className="plate empty-state">
          <div className="empty-mark" aria-hidden="true"><FlaskConical size={35} strokeWidth={1.25} /></div>
          <div className="empty-copy"><h2>{runs.isError ? "No investigations in the last confirmed history." : "Your next issue starts here."}</h2><p>{runs.isError ? "Refresh the history before relying on this empty view." : "Add a description and the files needed to reproduce it. TriageLab runs experiments in an isolated sandbox and records what happened."}</p>{!runs.isError && canRun && <p className="empty-hint">Create your first investigation using the button above.</p>}</div>
        </section>}
      </>}
    <section className="example-preview" aria-labelledby="example-heading">
      <div className="example-intro"><FileCheck2 size={20} strokeWidth={1.5} aria-hidden="true" /><div><h2 id="example-heading">See what an investigation looks like</h2><p>A saved, real run of the included missing-orders case.</p></div></div>
      <Link to="/example" className="example-row"><div><span className="example-provenance">Example investigation</span><h3>A sync that reports success. Eleven orders that never arrive.</h3><p>The customer blames lock retries. The replay loses the same orders without one.</p></div><div className="example-result"><Stamp hue="green">Reproduced</Stamp><span className="annotation">{exampleReport.experiments?.length ?? 0} experiments</span><ArrowRight size={18} aria-hidden="true" /></div></Link>
    </section>
  </>;
}
