import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Panel, Stamp, type StampHue } from "../components/ui";
import { useTicketAction, useTicketWorkspace, useZendeskOAuth } from "../hooks/use-tickets";
import { useSession } from "../hooks/use-auth";
import { roleAtLeast } from "../lib/auth";
import { orderedProcedures, ticketReference } from "../lib/tickets";
import type { TicketAction, TicketCase, TicketConnectionView, TicketSettings, TicketSource } from "../lib/types/ticket";
import { TicketProcedureEditor } from "./TicketProcedureEditor";

const providerName = (source?: TicketSource) => source === "github" ? "GitHub" : source === "linear" ? "Linear" : source === "bondren" ? "Bondren" : source === "jira" ? "Jira" : "Zendesk";

const states: Record<TicketCase["state"], [string, StampHue]> = {
  excluded: ["Outside policy", "gray"],
  review: ["Needs review", "amber"],
  ask: ["Clarification ready", "blue"],
  waiting: ["Waiting for requester", "gray"],
  approval: ["Needs approval", "amber"],
  launched: ["Run requested", "blue"],
};

function CaseStamp({ ticket }: { ticket: TicketCase }) {
  const [label, hue] = ticket.paused ? ["Paused", "gray"] as const
    : ticket.auto_run && ticket.state === "approval" ? ["Evidence ready", "blue"] as const
      : ticket.unattended && ticket.state === "launched" ? ["Started by policy", "blue"] as const
        : states[ticket.state];
  return <Stamp hue={hue}>{label}</Stamp>;
}

function TicketDetail({ ticket, settings, mode, procedureRevision, busy, perform, canAct }: {
  ticket: TicketCase;
  settings: TicketSettings;
  mode: "tracked" | "preview" | "draft";
  procedureRevision?: string;
  busy: boolean;
  perform: (input: TicketAction) => void;
  canAct?: boolean;
}) {
  const id = ticket.ticket_id;
  const source = ticket.source ?? "zendesk";
  const provider = providerName(source);
  const issue = source !== "zendesk";
  const kind = issue ? "issue" : "ticket";
  const sourceURL = ticket.url || `${ticket.account}/agent/tickets/${id}`;
  const requesterURL = ticket.requester?.url || (source === "zendesk" && ticket.requester ? `${ticket.account}/agent/users/${ticket.requester.id}` : undefined);
  const action = (kind: TicketAction["action"]) => perform({ connection: "", id, action: kind, revision: ticket.revision, plan: ticket.plan });
  const planCurrent = ticket.plan && Date.parse(ticket.plan_expires) > Date.now();
  const stale = mode !== "draft" && Boolean(procedureRevision && ticket.procedure_revision !== procedureRevision);
  return <Panel title={mode === "draft" ? `Draft preview for ${kind} #${id}` : ticket.title || `${issue ? "Issue" : "Ticket"} #${id}`}>
    <div className="panel-body ticket-detail">
      <div className="button-row ticket-meta">
        <CaseStamp ticket={ticket} />
        <a className="ticket-source" href={sourceURL} target="_blank" rel="noreferrer">{provider} #{id} ↗</a>
      </div>
      <p aria-live="polite">{ticket.paused ? `Tracking is paused for this ${kind}.` : ticket.reason}</p>
      {mode === "draft" && <p className="field-help">This preview checks the selected draft on its own. Published cases use the saved procedure order.</p>}
      {stale && !ticket.run_id && <p role="status">The procedure changed. {mode === "preview" ? `Preview this ${kind} again.` : `Refresh this ${kind} before taking action.`}</p>}
      {ticket.matched_tag && <dl className="ledger ticket-facts">
        <div><dt>Matched procedure</dt><dd><code>{ticket.matched_tag}</code></dd></div>
        <div><dt>Agent</dt><dd>{ticket.agent ? <code>{ticket.agent}</code> : "Reviewer selection required"}
          {ticket.matches?.length ? <> · {ticket.matches.map(rule => `${rule.tag} → ${rule.agent}`).join(", ")}</> : " · Procedure default"}
        </dd></div>
        {ticket.sandbox && <div><dt>Sandbox</dt><dd><code>{ticket.sandbox}</code></dd></div>}
      </dl>}
      {!stale && mode === "tracked" && !ticket.paused && !ticket.run_id && canAct !== false && (ticket.agent_choices?.length ?? 0) > 1 && <div className="button-row">
        {ticket.agent_choices?.filter(agent => agent !== ticket.agent).map(agent => <Button key={agent} type="button" disabled={busy}
          onClick={() => perform({ connection: "", id, action: "route", revision: ticket.revision, agent })}>Use {agent}</Button>)}
      </div>}
      {!!ticket.missing?.length && <details><summary>Missing evidence ({ticket.missing.length})</summary>
        <ul className="prose-list">{ticket.missing.map(item => <li key={item}>{item}</li>)}</ul>
      </details>}
      {!!ticket.facts?.length && <dl className="ledger ticket-facts">
        {ticket.facts.map(fact => <div key={fact.label}>
          <dt>{fact.label}</dt><dd>{fact.value} {fact.url
            ? <a className="ticket-source" href={fact.url} target="_blank" rel="noreferrer">{fact.url === sourceURL ? "Issue body" : `Comment ${fact.comment_id}`} ↗</a>
            : <span className="quiet">(comment {fact.comment_id})</span>}</dd>
        </div>)}
      </dl>}
      {(ticket.state === "approval" || Boolean(ticket.files?.length)) && <div className="ticket-plan">
        <p><strong>{ticket.sandbox || settings.template}</strong> · up to {settings.max_experiments} experiments and {settings.timeout_seconds} seconds.
          {ticket.state === "approval" && !ticket.auto_run && " Approval applies to these inputs and expires after 15 minutes."}
        </p>
        <ul className="prose-list">
          {ticket.files?.map(file => <li key={file.path}>{file.path} ({file.size_bytes} bytes)
            {file.source_url && <> · <a className="ticket-source" href={file.source_url} target="_blank" rel="noreferrer">Source attachment ↗</a></>}
            <br /><code>SHA-256 {file.sha256}</code></li>)}
        </ul>
      </div>}
      {ticket.draft && <div className="ticket-plan">
        <p><strong>{ticket.state === "waiting" ? "Clarification" : issue ? "Suggested issue clarification" : "Suggested public clarification"}</strong></p>
        {ticket.requester && <p>{issue ? "Issue author" : "Requester"} {requesterURL
          ? <a className="ticket-source" href={requesterURL} target="_blank" rel="noreferrer">{ticket.requester.name || `${provider} user #${ticket.requester.id}`} ↗</a>
          : ticket.requester.name || `${provider} user #${ticket.requester.id}`}{ticket.requester.email ? ` · ${ticket.requester.email}` : source === "zendesk" && " · Verify the address in Zendesk"}</p>}
        {ticket.state === "ask" && <p>Matched open {kind} with {issue ? "label" : "tag"} <code>{settings.policy.tag}</code>{settings.policy.priorities?.length ? ` and priority ${settings.policy.priorities.join(" or ")}` : ""}{settings.policy.field_id ? ` and custom field ${settings.policy.field_id} set to ${settings.policy.field_value}` : ""}.</p>}
        <p className="ticket-draft">{ticket.draft}</p>
        {issue && !settings.policy.auto_questions && <p>Review and post this clarification in {provider}, then refresh the issue after the author replies.</p>}
        {ticket.state === "ask" && <>
          {source === "github" && <p>This clarification is visible to everyone with access to the repository. In a public repository, it is public.</p>}
          {source === "linear" && <p>This clarification is visible to everyone with access to the workspace.</p>}
          {source === "bondren" && <p>This clarification is visible to the requester and everyone with access to the issue.</p>}
          {source === "jira" && <p>This clarification is visible to the requester on Jira Service Management issues and to everyone with access to the issue otherwise.</p>}
          <p>Approval sends this {issue ? "issue comment" : "public reply"} and allows up to {settings.policy.max_questions} total clarification attempts on this {kind} under the current policy.</p>
        </>}
      </div>}
      {!!ticket.questions?.length && <div>
        <p><strong>Question history</strong></p>
        <ul className="prose-list">
          {ticket.questions.map((question, index) => <li key={index}>
            {new Date(question.at).toLocaleString()} · {question.status === "conflict" ? `${issue ? "Issue" : "Ticket"} changed; no send` : question.status}
            <details><summary>Read question</summary><p className="ticket-draft">{question.body}</p></details>
          </li>)}
        </ul>
      </div>}
      {mode !== "draft" && canAct === false && <p className="quiet">Acting on {kind}s requires the user role.</p>}
      {mode !== "draft" && canAct !== false && <div className="button-row">
        {mode === "preview" ? ticket.state !== "excluded" &&
          <Button variant="primary" disabled={busy || stale} onClick={() => action("track")}>
            {settings.auto_runs_allowed && settings.policy.auto_runs ? "Track and start when ready" : `Track ${kind}`}
          </Button>
        : ticket.run_id ? <ButtonLink variant="primary" to={`/runs/${ticket.run_id}`}>Open investigation</ButtonLink>
        : <>
          {ticket.state === "approval" && planCurrent && !ticket.paused && !stale &&
            <Button variant="primary" disabled={busy} onClick={() => action("approve")}>Approve and start investigation</Button>}
          {ticket.state === "ask" && !ticket.paused && !stale &&
            <Button variant="primary" disabled={busy} onClick={() => action("clarify")}>Approve and send clarification</Button>}
          {!ticket.paused && <Button disabled={busy} onClick={() => action("refresh")}>
            {settings.policy.auto_questions || (settings.auto_runs_allowed && settings.policy.auto_runs) ? "Refresh and apply policy" : `Refresh ${kind}`}
          </Button>}
          <Button disabled={busy} onClick={() => action(ticket.paused ? "resume" : "pause")}>{ticket.paused ? "Resume tracking" : "Pause tracking"}</Button>
        </>}
      </div>}
    </div>
  </Panel>;
}

function SourcePanel({ connection, commentsEnabled, oauthResult }: {
  connection: TicketConnectionView;
  commentsEnabled: boolean;
  oauthResult: string | null;
}) {
  const mutation = useTicketAction();
  const connect = useZendeskOAuth();
  const session = useSession();
  const isAdmin = roleAtLeast(session.data?.role, "admin");
  const source = connection.source;
  const account = connection.account.replace("https://", "");
  const title = connection.label || (source === "github" ? "GitHub repository" : source === "linear" ? "Linear team"
    : source === "bondren" ? "Bondren workspace" : source === "jira" ? "Jira site" : "Zendesk connection");
  if (source !== "zendesk") {
    return <Panel title={title} aside={<Stamp hue="blue">{commentsEnabled ? (source === "linear" || source === "github" ? "Comments enabled" : "Notes enabled") : "Read-only"}</Stamp>}>
      <div className="panel-body ticket-detail">
        <p><a className="ticket-source" href={connection.account} target="_blank" rel="noreferrer">{connection.repository || account} ↗</a></p>
        {source === "jira" && <>
          <p>Import issue descriptions, comments, and supported text attachments from this Jira Cloud site. Jira always requires an Atlassian account email and API token configured on the server.</p>
          {commentsEnabled && <p>Posting requires the configured API token. Clarifications are customer-visible replies on Jira Service Management issues; investigation summaries are internal notes.</p>}
          {connection.settings.internal_notes && <p>Completed investigations offer an internal note preview for you to review before posting to the issue.</p>}
        </>}
        {source === "bondren" && <>
          <p>Import issue conversations, requester messages, and supported text attachments from this Bondren workspace. Bondren always requires an agent token configured on the server.</p>
          {commentsEnabled && <p>Posting requires the configured agent token. Clarifications are public messages; investigation summaries are internal notes.</p>}
          {connection.settings.internal_notes && <p>Completed investigations offer an internal note preview for you to review before posting to the issue.</p>}
        </>}
        {source === "linear" && <>
          <p>Import issue descriptions, author comments and supported text attachments from this team in the configured workspace. Linear always requires an API key configured on the server.</p>
          {commentsEnabled && <p>Posting requires the configured API key. Comments are visible to everyone with access to the workspace.</p>}
          {connection.settings.investigation_comments && <p>Completed investigations offer a comment preview for you to review before posting to the issue.</p>}
        </>}
        {source === "github" && <>
          <p>Import issue bodies, author comments and supported text attachments from this repository. Private repository access requires a token configured on the server.</p>
          {commentsEnabled && <p>Posting requires a token with Issues write permission. Comments are visible to everyone with repository access and are public in public repositories.</p>}
          {connection.settings.investigation_comments && <p>Completed investigations offer a comment preview for you to review before posting to the issue.</p>}
        </>}
      </div>
    </Panel>;
  }
  const oauth = connection.oauth;
  if (!oauth) return null;
  const atCallbackOrigin = new URL(oauth.workspace_url).origin === window.location.origin;
  return <Panel title={title} aside={<Stamp hue={oauth.connected ? "green" : "gray"}>{oauth.connected ? "Connected" : "Not connected"}</Stamp>}>
    <div className="panel-body ticket-detail">
      <p>{new URL(oauth.account).hostname} · {oauth.scopes.join(", ")}</p>
      {oauthResult === "denied" && <p role="status">Zendesk access was declined.</p>}
      {oauthResult === "failed" && <ErrorPlate title="Zendesk authorization failed" error="Check the client identifier, redirect URL and allowed scopes, then connect again." />}
      {connect.isError && <ErrorPlate title="Could not start Zendesk authorization" error={connect.error} />}
      <div className="button-row">
        {!isAdmin && oauth.connected && <p className="quiet">Reconnecting Zendesk requires the admin role.</p>}
        {atCallbackOrigin ? isAdmin && <Button variant={oauth.connected ? "secondary" : "primary"} disabled={connect.isPending || mutation.isPending} onClick={() => connect.mutate(connection.id)}>
          {connect.isPending ? "Opening Zendesk…" : oauth.connected ? "Reconnect Zendesk" : "Connect Zendesk"}
        </Button> : <a className="button button-secondary" href={oauth.workspace_url}>Open the workspace at the OAuth origin</a>}
      </div>
    </div>
  </Panel>;
}

function ConnectionSection({ connection, canAct, isAdmin, oauthResult }: {
  connection: TicketConnectionView;
  canAct: boolean;
  isAdmin: boolean;
  oauthResult: string | null;
}) {
  const mutation = useTicketAction();
  const [id, setID] = useState("");
  const [selected, setSelected] = useState("");
  const [preview, setPreview] = useState<TicketCase>();
  const [editProcedure, setEditProcedure] = useState(false);
  const source = connection.source;
  const issue = source !== "zendesk";
  const kind = issue ? "issue" : "ticket";
  const active = connection.cases.find(ticket => ticket.ticket_id === selected);
  const procedures = orderedProcedures(connection.procedure);
  const matched = procedures.find(p => p.policy.tag === (active ?? preview)?.matched_tag);
  const caseSettings = matched ? { ...connection.settings, policy: matched.policy } : undefined;
  const commentsEnabled = connection.settings.investigation_comments || procedures.some(p => p.policy.auto_questions);

  function perform(input: TicketAction) {
    mutation.mutate({ ...input, connection: connection.id }, { onSuccess: (ticket) => {
      if (input.action === "preview") {
        setPreview(ticket);
        setSelected(ticket.ticket_id);
      } else {
        setSelected(ticket.ticket_id);
        setPreview(undefined);
      }
    } });
  }

  function inspect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    perform({ connection: connection.id, id: ticketReference(source, id), action: "preview" });
  }

  return <>
    <SourcePanel connection={connection} commentsEnabled={commentsEnabled} oauthResult={oauthResult} />
    <Panel title="Ticket procedures" aside={connection.procedure && isAdmin && <Button type="button"
      onClick={() => setEditProcedure(editing => !editing)} aria-expanded={editProcedure}>
      {editProcedure ? "Close editor" : "Edit procedures"}
    </Button>}>
      <div className="panel-body ticket-detail">
        <p>Open {issue ? "issues" : "tickets"} use the first matching procedure. Review is the default.</p>
        <ol className="prose-list">{procedures.map(p => <li key={p.policy.tag}>
          <code>{p.policy.tag}</code>{connection.settings.auto_runs_allowed && p.policy.auto_runs && " · Starts automatically"}
        </li>)}</ol>
        {connection.connected && canAct && <form className="ticket-form" onSubmit={inspect}>
          <div><label className="field-label" htmlFor={`ticket-id-${connection.id}`}>{source === "jira" ? "Jira issue key, URL, or id" : source === "bondren" ? "Bondren issue ID or number" : source === "linear" ? "Linear issue number" : source === "github" ? "GitHub issue number" : "Zendesk ticket ID"}</label>
            <input className="field" id={`ticket-id-${connection.id}`} inputMode={source === "bondren" || source === "jira" ? undefined : "numeric"} pattern={source === "bondren" || source === "jira" ? undefined : "[1-9][0-9]{0,18}"} maxLength={source === "bondren" || source === "jira" ? 512 : 19} required disabled={mutation.isPending} value={id} onChange={event => setID(event.currentTarget.value)} />
            {source === "jira" && <p className="field-help">Enter an issue key like OPS-42, paste the issue URL, or use the numeric issue id.</p>}
            {source === "bondren" && <p className="field-help">Paste the issue ID from the Bondren issue URL, the issue URL itself, or the issue number.</p>}</div>
          <Button type="submit" variant="primary" disabled={mutation.isPending}>Preview {kind}</Button>
        </form>}
      </div>
    </Panel>
    {editProcedure && connection.procedure && <TicketProcedureEditor key={connection.account} connection={connection.id} version={connection.procedure}
      source={connection.source} connected={connection.connected} template={connection.settings.template} clarificationLimit={connection.settings.clarification_limit}
      autoRunsAllowed={connection.settings.auto_runs_allowed}
      renderPreview={(ticket, draft) => <TicketDetail ticket={ticket} settings={{ ...connection.settings, policy: draft.policy }}
        mode="draft" busy={false} perform={perform} canAct={canAct} />} />}
    {mutation.isPending && <p role="status" className="quiet">Checking {kind}…</p>}
    {mutation.isError && <ErrorPlate title={`${issue ? "Issue" : "Ticket"} action could not complete`} error={mutation.error} />}
    {connection.cases.length > 0 && <Panel title={`Tracked ${kind}s`}>
      <div className="panel-body ticket-queue">
        {connection.cases.map(ticket => <div key={ticket.ticket_id}>
          <Button disabled={!connection.connected || mutation.isPending} aria-pressed={ticket.ticket_id === selected && !preview} onClick={() => {
            mutation.reset(); setSelected(ticket.ticket_id); setPreview(undefined);
          }}>#{ticket.ticket_id} {ticket.title}</Button>
          <CaseStamp ticket={ticket} />
        </div>)}
      </div>
    </Panel>}
    {connection.connected && (active || preview) && <TicketDetail ticket={active || preview!} settings={caseSettings ?? connection.settings} mode={active ? "tracked" : "preview"}
      procedureRevision={connection.procedure?.revision}
      busy={mutation.isPending} perform={perform} canAct={canAct} />}
  </>;
}

export function TicketsPage() {
  const workspace = useTicketWorkspace();
  const session = useSession();
  const canAct = roleAtLeast(session.data?.role, "user");
  const isAdmin = roleAtLeast(session.data?.role, "admin");
  const [params] = useSearchParams();
  const oauthResult = params.get("zendesk");
  const [activeID, setActiveID] = useState("");
  const data = workspace.data;
  const connections = data?.enabled ? data.connections : [];
  const current = connections.find(connection => connection.id === activeID) ?? connections[0];

  return <>
    <PageTitle title="Ticket cases" description="Prepare helpdesk tickets and GitHub, Linear, Bondren, or Jira issues for a useful investigation." />
    {workspace.isPending && <Loading label="Loading ticket cases" />}
    {workspace.isError && <ErrorPlate title="Could not load ticket cases" error={workspace.error} action={<Button onClick={() => void workspace.refetch()}>Try again</Button>} />}
    {data && !data.enabled && <Panel title="Connect a case source">
      <div className="panel-body ticket-detail">
        <p>Choose a Zendesk account, GitHub repository, Linear team, Bondren workspace, or Jira site and configure a ticket procedure to prepare cases here.</p>
        <div className="button-row">
          {isAdmin
            ? <ButtonLink to="/connections" variant="primary">Open connections</ButtonLink>
            : <p className="quiet">Ask an administrator to connect a ticket or issue source.</p>}
          <a className="ticket-source" href="https://github.com/bondrenai/TriageLab/blob/main/docs/tickets.md" target="_blank" rel="noreferrer">Setup guides ↗</a>
        </div>
      </div>
    </Panel>}
    {current && <div className="section-stack">
      {connections.length > 1 && <div className="button-row" role="tablist" aria-label="Ticket connections">
        {connections.map(connection => <Button key={connection.id} type="button"
          aria-pressed={connection.id === current.id} onClick={() => setActiveID(connection.id)}>
          {providerName(connection.source)} · {connection.label || connection.account.replace("https://", "")}
        </Button>)}
      </div>}
      <ConnectionSection key={current.id} connection={current} canAct={canAct} isAdmin={isAdmin} oauthResult={oauthResult} />
    </div>}
  </>;
}
