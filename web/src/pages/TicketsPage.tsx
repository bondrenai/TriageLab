import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Button, ButtonLink, ErrorPlate, Loading, PageTitle, Panel, Stamp, type StampHue } from "../components/ui";
import { useTicketAction, useTicketWorkspace, useZendeskOAuth } from "../hooks/use-tickets";
import type { TicketAction, TicketCase, TicketSettings } from "../lib/types/ticket";

const states: Record<TicketCase["state"], [string, StampHue]> = {
  excluded: ["Outside policy", "gray"],
  review: ["Needs review", "amber"],
  ask: ["Clarification ready", "blue"],
  waiting: ["Waiting for requester", "gray"],
  approval: ["Needs approval", "amber"],
  launched: ["Run requested", "blue"],
};

function CaseStamp({ ticket }: { ticket: TicketCase }) {
  const [label, hue] = ticket.paused ? ["Paused", "gray"] as const : states[ticket.state];
  return <Stamp hue={hue}>{label}</Stamp>;
}

function TicketDetail({ ticket, settings, preview, busy, perform }: {
  ticket: TicketCase;
  settings: TicketSettings;
  preview: boolean;
  busy: boolean;
  perform: (input: TicketAction) => void;
}) {
  const id = ticket.ticket_id;
  const action = (kind: TicketAction["action"]) => perform({ id, action: kind, revision: ticket.revision, plan: ticket.plan });
  const planCurrent = ticket.plan && Date.parse(ticket.plan_expires) > Date.now();
  return <Panel title={ticket.title || `Ticket #${id}`}>
    <div className="panel-body ticket-detail">
      <div className="button-row ticket-meta">
        <CaseStamp ticket={ticket} />
        <a className="ticket-source" href={`${ticket.account}/agent/tickets/${id}`} target="_blank" rel="noreferrer">Zendesk #{id} ↗</a>
      </div>
      <p aria-live="polite">{ticket.paused ? "Automatic work is paused for this ticket." : ticket.reason}</p>
      {!!ticket.facts?.length && <dl className="ledger ticket-facts">
        {ticket.facts.map(fact => <div key={fact.label}>
          <dt>{fact.label}</dt><dd>{fact.value} <span className="quiet">(comment {fact.comment_id})</span></dd>
        </div>)}
      </dl>}
      {ticket.state === "approval" && <div className="ticket-plan">
        <p>Proposed run: <strong>{settings.template}</strong>, up to {settings.max_experiments} experiments and {settings.timeout_seconds} seconds. Approval applies to these inputs and expires after 15 minutes.</p>
        <ul className="prose-list">
          {ticket.files?.map(file => <li key={file.path}>{file.path} ({file.size_bytes} bytes)<br /><code>SHA-256 {file.sha256}</code></li>)}
        </ul>
      </div>}
      {ticket.draft && <div className="ticket-plan">
        <p><strong>{ticket.state === "waiting" ? "Clarification" : "Suggested public clarification"}</strong></p>
        <p className="ticket-draft">{ticket.draft}</p>
      </div>}
      {!!ticket.questions?.length && <div>
        <p><strong>Question history</strong></p>
        <ul className="prose-list">
          {ticket.questions.map((question, index) => <li key={index}>
            {new Date(question.at).toLocaleString()} · {question.status === "conflict" ? "Ticket changed; no send" : question.status}
            <details><summary>Read question</summary><p className="ticket-draft">{question.body}</p></details>
          </li>)}
        </ul>
      </div>}
      <div className="button-row">
        {preview ? ticket.state !== "excluded" &&
          <Button variant="primary" disabled={busy} onClick={() => action("track")}>{ticket.state === "ask" ? "Track and send question" : "Track ticket"}</Button>
        : ticket.run_id ? <ButtonLink variant="primary" to={`/runs/${ticket.run_id}`}>Open investigation</ButtonLink>
        : <>
          {ticket.state === "approval" && planCurrent && !ticket.paused &&
            <Button variant="primary" disabled={busy} onClick={() => action("approve")}>Approve and start investigation</Button>}
          {!ticket.paused && <Button disabled={busy} onClick={() => action("refresh")}>
            {settings.policy.auto_questions ? "Refresh and apply policy" : "Refresh ticket"}
          </Button>}
          <Button disabled={busy} onClick={() => action(ticket.paused ? "resume" : "pause")}>{ticket.paused ? "Resume tracking" : "Pause tracking"}</Button>
        </>}
      </div>
    </div>
  </Panel>;
}

export function TicketsPage() {
  const workspace = useTicketWorkspace();
  const mutation = useTicketAction();
  const connect = useZendeskOAuth();
  const [params] = useSearchParams();
  const oauthResult = params.get("zendesk");
  const [id, setID] = useState("");
  const [selected, setSelected] = useState("");
  const [preview, setPreview] = useState<TicketCase>();
  const data = workspace.data;
  const atCallbackOrigin = data?.enabled && new URL(data.oauth.workspace_url).origin === window.location.origin;
  const active = data?.enabled ? data.cases.find(ticket => ticket.ticket_id === selected) : undefined;

  function perform(input: TicketAction) {
    mutation.mutate(input, { onSuccess: (ticket) => {
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
    perform({ id: id.trim(), action: "preview" });
  }

  return <>
    <PageTitle title="Ticket cases" description="Prepare helpdesk tickets for a useful investigation." />
    {workspace.isPending && <Loading label="Loading ticket cases" />}
    {workspace.isError && <ErrorPlate title="Could not load ticket cases" error={workspace.error} action={<Button onClick={() => void workspace.refetch()}>Try again</Button>} />}
    {data && !data.enabled && <Panel title="Connect Zendesk">
      <div className="panel-body ticket-detail">
        <p>Configure a Zendesk OAuth client and a ticket procedure to prepare cases here.</p>
        <p><a className="ticket-source" href="https://github.com/bondrenai/TriageLab/blob/main/docs/tickets.md" target="_blank" rel="noreferrer">Read the setup guide ↗</a></p>
      </div>
    </Panel>}
    {data?.enabled && <div className="section-stack">
      <Panel title="Zendesk connection" aside={<Stamp hue={data.oauth.connected ? "green" : "gray"}>{data.oauth.connected ? "Connected" : "Not connected"}</Stamp>}>
        <div className="panel-body ticket-detail">
          <p>{new URL(data.oauth.account).hostname} · {data.oauth.scopes.join(", ")}</p>
          {oauthResult === "denied" && <p role="status">Zendesk access was declined.</p>}
          {oauthResult === "failed" && <ErrorPlate title="Zendesk authorization failed" error="Check the client identifier, redirect URL and allowed scopes, then connect again." />}
          {connect.isError && <ErrorPlate title="Could not start Zendesk authorization" error={connect.error} />}
          <div className="button-row">
            {atCallbackOrigin ? <Button variant={data.oauth.connected ? "secondary" : "primary"} disabled={connect.isPending || mutation.isPending} onClick={() => connect.mutate()}>
              {connect.isPending ? "Opening Zendesk…" : data.oauth.connected ? "Reconnect Zendesk" : "Connect Zendesk"}
            </Button> : <a className="button button-secondary" href={data.oauth.workspace_url}>Open the workspace at the OAuth origin</a>}
          </div>
        </div>
      </Panel>
      <Panel title="Python timeout procedure">
        <div className="panel-body ticket-detail">
          <p>Matches open tickets tagged <code>{data.settings.policy.tag}</code>{data.settings.policy.priorities?.length ? ` with priority ${data.settings.policy.priorities.join(" or ")}` : ""}{data.settings.policy.field_id ? ` and custom field ${data.settings.policy.field_id} set to ${data.settings.policy.field_value}` : ""}.</p>
          <p>{data.settings.policy.auto_questions ? `The policy may send up to ${data.settings.policy.max_questions} public clarification attempts per ticket.` : "Clarification questions are prepared for human review."} Every run requires approval.</p>
          {data.oauth.connected && <form className="ticket-form" onSubmit={inspect}>
            <div><label className="field-label" htmlFor="ticket-id">Zendesk ticket ID</label>
              <input className="field" id="ticket-id" inputMode="numeric" pattern="[1-9][0-9]{0,18}" maxLength={19} required disabled={mutation.isPending} value={id} onChange={event => setID(event.currentTarget.value)} /></div>
            <Button type="submit" variant="primary" disabled={mutation.isPending}>Preview ticket</Button>
          </form>}
        </div>
      </Panel>
      {mutation.isPending && <p role="status" className="quiet">Checking ticket…</p>}
      {mutation.isError && <ErrorPlate title="Ticket action could not complete" error={mutation.error} />}
      {data.cases.length > 0 && <Panel title="Tracked tickets">
        <div className="panel-body ticket-queue">
          {data.cases.map(ticket => <div key={ticket.ticket_id}>
            <Button disabled={!data.oauth.connected || mutation.isPending} aria-pressed={ticket.ticket_id === selected && !preview} onClick={() => {
              mutation.reset(); setSelected(ticket.ticket_id); setPreview(undefined);
            }}>#{ticket.ticket_id} {ticket.title}</Button>
            <CaseStamp ticket={ticket} />
          </div>)}
        </div>
      </Panel>}
      {data.oauth.connected && (active || preview) && <TicketDetail ticket={active || preview!} settings={data.settings} preview={!active}
        busy={mutation.isPending} perform={perform} />}
    </div>}
  </>;
}
