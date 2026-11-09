import { ArrowRight, FileCheck2, FlaskConical, Plus } from "lucide-react";
import { Link } from "react-router-dom";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Stamp, StatusStamp } from "../components/ui";
import { useRuns } from "../hooks/use-investigations";
import { caseTitle, formatDate } from "../lib/investigations";

export function InvestigationsPage() {
  const runs = useRuns();
  return <>
    <PageTitle title="Investigations" description="Reproduce an issue. Follow the experiments. Inspect the evidence." action={<ButtonLink to="/new" variant="primary"><Plus size={16} aria-hidden="true" />New investigation</ButtonLink>} />
    {runs.isPending ? <Loading label="Loading investigations…" /> : runs.isError ? <ErrorPlate title="Investigations could not load" error={runs.error} action={<Button onClick={() => void runs.refetch()}>Try again</Button>} /> : runs.data.runs.length ? <section className="plate table-plate" aria-label="Investigation history">
      <div className="table-caption"><h2>Your investigations</h2><span className="annotation">{runs.data.runs.length} total</span></div>
      <div className="table-scroll"><table><thead><tr><th>Case</th><th>Status</th><th>Experiments</th><th>Started</th><th><span className="sr-only">Open</span></th></tr></thead><tbody>{runs.data.runs.map(run => <tr key={run.id}>
        <td><Link className="run-link" to={`/runs/${run.id}`}>{caseTitle(run.description)}</Link><span className="run-id annotation">{run.id.slice(0, 8)}</span></td>
        <td><StatusStamp status={run.status} outcome={run.outcome} active={run.active} /></td>
        <td className="mono">{run.experiment_count}</td><td><time dateTime={run.started_at}>{formatDate(run.started_at)}</time></td>
        <td><Link className="icon-link" to={`/runs/${run.id}`} aria-label={`Open ${caseTitle(run.description)}`}><ArrowRight size={17} /></Link></td>
      </tr>)}</tbody></table></div>
    </section> : <section className="plate empty-state">
      <div className="empty-mark" aria-hidden="true"><FlaskConical size={35} strokeWidth={1.25} /></div>
      <div className="empty-copy"><h2>Your next issue starts here.</h2><p>Add a description and the files needed to reproduce it. TriageLab runs experiments in an isolated sandbox and records what happened.</p><p className="empty-hint">Create your first investigation using the button above.</p></div>
    </section>}
    <section className="example-preview" aria-labelledby="example-heading">
      <div className="example-intro"><FileCheck2 size={20} strokeWidth={1.5} aria-hidden="true" /><div><h2 id="example-heading">See what an investigation looks like</h2><p>A saved, real run against the included timeout fixture.</p></div></div>
      <Link to="/example" className="example-row"><div><span className="example-provenance">Example investigation</span><h3>A healthy endpoint. A client that keeps timing out.</h3><p>A configuration value turns five seconds into five milliseconds.</p></div><div className="example-result"><Stamp hue="green">Reproduced</Stamp><span className="annotation">4 experiments</span><ArrowRight size={18} aria-hidden="true" /></div></Link>
    </section>
  </>;
}
