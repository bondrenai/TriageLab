import { useState, type FormEvent } from "react";
import { Check, X } from "lucide-react";
import { useSession } from "../hooks/use-auth";
import {
  useConnections, useCreateConnection, useRemoveConnection, useTestConnection, useUpdateConnection,
  useZendeskConnect,
} from "../hooks/use-connections";
import { roleAtLeast } from "../lib/auth";
import { Button, ErrorPlate, Loading, Panel, PageTitle, Stamp } from "../components/ui";
import { ConnectorIcon } from "../components/connector-icon";
import type {
  ConnectionConfig, ConnectionCredential, ConnectionInput, ConnectionStatus, ConnectionTestResult,
} from "../lib/types/connection";
import type { TicketSource } from "../lib/types/ticket";

const SOURCES: TicketSource[] = ["zendesk", "github", "linear", "bondren", "jira"];

const sourceName = (source: TicketSource) =>
  source === "github" ? "GitHub" : source === "linear" ? "Linear" : source === "bondren" ? "Bondren"
    : source === "jira" ? "Jira" : "Zendesk";

const sourceDescription = (source: TicketSource) =>
  source === "github" ? "Repository issues, author comments, and text attachments."
    : source === "linear" ? "Workspace issues for one team, keyed by issue number."
      : source === "bondren" ? "Support mailbox issues and requester conversations."
        : source === "jira" ? "Jira Cloud site issues with internal-note summaries."
          : "Helpdesk tickets and conversations through a browser OAuth grant.";

const writeCapability = (source: TicketSource) =>
  source === "github" || source === "linear"
    ? { field: "investigation_comments" as const, label: "Post investigation comments on completed runs" }
    : { field: "internal_notes" as const, label: "Post internal notes on completed runs" };

const emptyConfig = (source: TicketSource): ConnectionConfig => ({
  source, poll_seconds: 0, internal_notes: false, investigation_comments: false,
  policy: { tag: source === "zendesk" ? "support" : "reproduce", auto_questions: false, max_questions: 0 },
});

interface FormState {
  label: string;
  source: TicketSource;
  repository: string;
  workspace: string;
  team: string;
  origin: string;
  email: string;
  subdomain: string;
  clientID: string;
  redirectURI: string;
  credential: ConnectionCredential["mode"];
  token: string;
  tokenEnv: string;
  pollSeconds: string;
  writes: boolean;
}

function formFrom(source: TicketSource, status?: ConnectionStatus): FormState {
  const config = status?.config;
  const capability = writeCapability(source).field;
  return {
    label: status?.label ?? "",
    source,
    repository: config?.repository ?? "",
    workspace: config?.workspace ?? "",
    team: config?.team ?? "",
    origin: config?.origin ?? "",
    email: config?.email ?? "",
    subdomain: config?.subdomain ?? "",
    clientID: config?.client_id ?? "",
    redirectURI: config?.redirect_uri ?? "",
    credential: status?.credential.mode ?? "stored",
    token: "",
    tokenEnv: config?.token_env ?? "",
    pollSeconds: String(config?.poll_seconds ?? 0),
    writes: config ? Boolean(config[capability]) : false,
  };
}

function configFrom(form: FormState): ConnectionConfig {
  const config = emptyConfig(form.source);
  config.poll_seconds = Number(form.pollSeconds) || 0;
  config[writeCapability(form.source).field] = form.writes;
  const tokenEnv = form.credential === "env" ? form.tokenEnv.trim() : "";
  switch (form.source) {
    case "github":
      config.repository = form.repository.trim();
      config.token_env = tokenEnv;
      break;
    case "linear":
      config.workspace = form.workspace.trim();
      config.team = form.team.trim().toUpperCase();
      config.token_env = tokenEnv;
      break;
    case "bondren":
      config.origin = form.origin.trim().replace(/\/+$/, "");
      config.token_env = tokenEnv;
      break;
    case "jira":
      config.origin = form.origin.trim().replace(/\/+$/, "");
      config.email = form.email.trim();
      config.token_env = tokenEnv;
      break;
    default:
      config.subdomain = form.subdomain.trim();
      config.client_id = form.clientID.trim();
      config.redirect_uri = form.redirectURI.trim();
  }
  return config;
}

function ConnectionForm({ source, status, onDone }: { source: TicketSource; status?: ConnectionStatus; onDone: () => void }) {
  const create = useCreateConnection();
  const update = useUpdateConnection();
  const test = useTestConnection(status !== undefined);
  const [form, setForm] = useState<FormState>(() => formFrom(source, status));
  const [confirmReset, setConfirmReset] = useState(false);
  const [result, setResult] = useState<ConnectionTestResult | null>(null);
  const editing = status !== undefined;
  const busy = create.isPending || update.isPending || test.isPending;
  const error = create.error ?? update.error;
  const resetRequired = error instanceof Error && error.message.includes("Confirm the reset");

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm(previous => ({ ...previous, [key]: value }));

  // A blank stored token keeps the saved credential when editing; the server
  // also falls back to the saved credential when a saved-connection test
  // arrives without one.
  const input = (): ConnectionInput => ({
    label: form.label.trim(),
    config: configFrom(form),
    ...(form.credential === "stored" && form.token !== "" ? { token: form.token } : {}),
    ...(confirmReset ? { confirm_reset: true } : {}),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setResult(null);
    if (editing) {
      update.mutate({ id: status.id, input: input() }, { onSuccess: onDone });
    } else {
      create.mutate(input(), { onSuccess: onDone });
    }
  };

  return <form className="account-create" onSubmit={submit}>
    <div>
      <label className="field-label" htmlFor="connection-label">Label</label>
      <input id="connection-label" className="field" maxLength={60} placeholder="Support desk"
        value={form.label} onChange={e => set("label", e.target.value)} />
    </div>
    {form.source === "github" &&
      <div>
        <label className="field-label" htmlFor="connection-repository">Repository</label>
        <input id="connection-repository" className="field mono" required spellCheck={false} placeholder="owner/repo"
          value={form.repository} onChange={e => set("repository", e.target.value)} />
      </div>}
    {form.source === "linear" && <>
      <div>
        <label className="field-label" htmlFor="connection-workspace">Workspace</label>
        <input id="connection-workspace" className="field mono" required spellCheck={false} placeholder="acme"
          value={form.workspace} onChange={e => set("workspace", e.target.value)} />
      </div>
      <div>
        <label className="field-label" htmlFor="connection-team">Team key</label>
        <input id="connection-team" className="field mono" required spellCheck={false} placeholder="ENG"
          value={form.team} onChange={e => set("team", e.target.value)} />
      </div>
    </>}
    {form.source === "bondren" &&
      <div>
        <label className="field-label" htmlFor="connection-origin">Origin</label>
        <input id="connection-origin" className="field mono" required spellCheck={false} placeholder="https://app.bondren.example"
          value={form.origin} onChange={e => set("origin", e.target.value)} />
      </div>}
    {form.source === "jira" && <>
      <div>
        <label className="field-label" htmlFor="connection-origin">Site</label>
        <input id="connection-origin" className="field mono" required spellCheck={false} placeholder="https://your-site.atlassian.net"
          value={form.origin} onChange={e => set("origin", e.target.value)} />
      </div>
      <div>
        <label className="field-label" htmlFor="connection-email">Atlassian account email</label>
        <input id="connection-email" className="field mono" required type="email" placeholder="you@example.com"
          value={form.email} onChange={e => set("email", e.target.value)} />
      </div>
    </>}
    {form.source === "zendesk" && <>
      <div>
        <label className="field-label" htmlFor="connection-subdomain">Subdomain</label>
        <input id="connection-subdomain" className="field mono" required spellCheck={false} placeholder="example"
          value={form.subdomain} onChange={e => set("subdomain", e.target.value)} />
      </div>
      <div>
        <label className="field-label" htmlFor="connection-client">OAuth client identifier</label>
        <input id="connection-client" className="field mono" required spellCheck={false} placeholder="triagelab-local"
          value={form.clientID} onChange={e => set("clientID", e.target.value)} />
      </div>
      <div>
        <label className="field-label" htmlFor="connection-redirect">Redirect URL</label>
        <input id="connection-redirect" className="field mono" spellCheck={false}
          placeholder="http://127.0.0.1:8080/oauth/zendesk/callback"
          value={form.redirectURI} onChange={e => set("redirectURI", e.target.value)} />
      </div>
    </>}
    {form.source !== "zendesk" &&
      <div>
        <label className="field-label" htmlFor="connection-credential">Credential</label>
        <select id="connection-credential" className="field field-compact" value={form.credential}
          onChange={e => set("credential", e.target.value as ConnectionCredential["mode"])}>
          <option value="stored">Token stored by the server</option>
          <option value="env">Environment variable</option>
          {form.source === "github" && <option value="none">Anonymous read</option>}
        </select>
        {form.credential === "stored" && <input className="field credential-input" type="password" autoComplete="new-password"
          required={!editing && form.source !== "github"}
          placeholder={editing ? "Leave blank to keep the saved token" : "Paste the API token"}
          aria-label="API token" value={form.token} onChange={e => set("token", e.target.value)} />}
        {form.credential === "env" && <input className="field mono credential-input" required spellCheck={false}
          placeholder="TRIAGELAB_SOURCE_TOKEN" aria-label="Token environment variable"
          value={form.tokenEnv} onChange={e => set("tokenEnv", e.target.value)} />}
      </div>}
    <div>
      <label className="field-label" htmlFor="connection-poll">Polling seconds (0 for manual)</label>
      <input id="connection-poll" className="field" type="number" min={0} max={3600} step={1}
        value={form.pollSeconds} onChange={e => set("pollSeconds", e.target.value)} />
    </div>
    <label className="field-label capability-toggle">
      <input type="checkbox" checked={form.writes} onChange={e => set("writes", e.target.checked)} />
      {writeCapability(form.source).label}
    </label>
    <div className="form-actions">
      <Button type="submit" variant="primary" disabled={busy}>
        <Check size={15} aria-hidden="true" />{editing ? "Save connection" : "Add connection"}
      </Button>
      <Button type="button" disabled={busy} onClick={() => test.mutate({ id: status?.id, input: input() }, { onSuccess: setResult })}>
        Test settings
      </Button>
      <Button type="button" variant="ghost" onClick={onDone}><X size={15} aria-hidden="true" />Cancel</Button>
    </div>
    {resetRequired && <label className="field-label capability-toggle">
      <input type="checkbox" checked={confirmReset} onChange={e => setConfirmReset(e.target.checked)} />
      Reset this connection's tracked cases and procedure for the new account
    </label>}
    {error && !busy && <p className="account-error" role="alert">{error instanceof Error ? error.message : "The connection was not saved."}</p>}
    {result && <p className="quiet" role="status">
      {result.ok ? <Stamp hue="green">Verified</Stamp> : <Stamp hue="red">Failed</Stamp>} {result.detail ?? ""}
    </p>}
    {test.isError && !busy && <p className="account-error" role="alert">{test.error instanceof Error ? test.error.message : "The test could not run."}</p>}
  </form>;
}

function ConnectionRow({ status, onEdit }: { status: ConnectionStatus; onEdit: () => void }) {
  const remove = useRemoveConnection();
  const test = useTestConnection(true);
  const connect = useZendeskConnect();
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [result, setResult] = useState<ConnectionTestResult | null>(null);
  const busy = remove.isPending || test.isPending || connect.isPending;
  return <tr>
    <td><span className="connector-plate-head"><ConnectorIcon source={status.source} connected={status.running} />{sourceName(status.source)}</span></td>
    <td className="mono">{status.label || status.account}</td>
    <td className="quiet">
      {status.credential.mode === "stored" ? "Stored token"
        : status.credential.mode === "env" ? `Environment: ${status.credential.env}` : "Anonymous"}
    </td>
    <td>
      {!status.running ? <Stamp hue="red">Not running</Stamp>
        : !status.connected ? <Stamp hue="amber">Not connected</Stamp>
          : <Stamp hue="green">Connected</Stamp>}
    </td>
    <td className="account-actions">
      <div className="button-row">
        <Button type="button" onClick={onEdit} disabled={busy}>Edit</Button>
        <Button type="button" disabled={busy}
          onClick={() => test.mutate({ id: status.id, input: { config: status.config } }, { onSuccess: setResult })}>
          Test
        </Button>
        {status.source === "zendesk" && status.running &&
          <Button type="button" disabled={busy} onClick={() => connect.mutate(status.id)}>
            {status.connected ? "Reconnect" : "Connect"}
          </Button>}
        {confirmRemove
          ? <><Button type="button" variant="primary" disabled={busy}
              onClick={() => remove.mutate(status.id, { onSuccess: () => setConfirmRemove(false) })}>Remove connection</Button>
            <Button type="button" variant="ghost" onClick={() => setConfirmRemove(false)}>Keep</Button></>
          : <Button type="button" disabled={busy} onClick={() => setConfirmRemove(true)}>Remove…</Button>}
      </div>
      {result && <p className="quiet" role="status">
        {result.ok ? <Stamp hue="green">Verified</Stamp> : <Stamp hue="red">Failed</Stamp>} {result.detail ?? ""}
      </p>}
      {remove.isError && !busy && <p className="account-error" role="alert">{remove.error instanceof Error ? remove.error.message : "The connection was not removed."}</p>}
      {connect.isError && !busy && <p className="account-error" role="alert">{connect.error instanceof Error ? connect.error.message : "Authorization could not start."}</p>}
    </td>
  </tr>;
}

function SourceCatalog({ list, onPick }: { list: ConnectionStatus[]; onPick: (source: TicketSource) => void }) {
  const atCap = list.length >= 8;
  return <Panel title="Connect a source">
    <div className="panel-body">
      {atCap && <p className="quiet">The connection limit is reached. Remove a connection to connect another source.</p>}
      <div className="connector-grid">
        {SOURCES.map(source => {
          const live = list.filter(connection => connection.source === source && connection.running);
          return <button key={source} type="button" className="connector-plate" disabled={atCap}
            onClick={() => onPick(source)}>
            <span className="connector-plate-head">
              <ConnectorIcon source={source} connected={live.length > 0} />
              <strong>{sourceName(source)}</strong>
            </span>
            <p>{sourceDescription(source)}</p>
            {live.length > 0 && <span className="quiet annotation">{live.length === 1 ? "1 connection" : `${live.length} connections`}</span>}
          </button>;
        })}
      </div>
    </div>
  </Panel>;
}

export function ConnectionsPage() {
  const session = useSession();
  const connections = useConnections();
  const [adding, setAdding] = useState<TicketSource | null>(null);
  const [editing, setEditing] = useState<ConnectionStatus | null>(null);

  if (!roleAtLeast(session.data?.role, "admin")) {
    return <>
      <PageTitle title="Connections" description="Managing ticket connections requires the admin role." />
      <Panel title="Ticket connections"><div className="panel-body"><p>Ask an administrator to connect a ticket or issue source.</p></div></Panel>
    </>;
  }
  if (connections.isError) return <ErrorPlate title="Connections could not load" error={connections.error} action={<Button onClick={() => void connections.refetch()}>Try again</Button>} />;
  if (connections.isLoading) return <Loading label="Loading connections…" />;

  const list = connections.data?.connections ?? [];
  return <>
    <PageTitle title="Connections"
      description="Ticket and issue sources this workspace watches. Zendesk connects through its authorization page; other sources authorize with a token." />
    <div className="section-stack">
      {(adding || editing) && <Panel
        title={editing ? `Edit ${sourceName(editing.source)} connection` : `Connect ${sourceName(adding!)}`}
        aside={editing ? <span className="quiet annotation mono">{editing.account}</span>
          : <span className="connector-plate-head"><ConnectorIcon source={adding!} /> {sourceDescription(adding!)}</span>}>
        <div className="panel-body">
          <ConnectionForm key={editing?.id ?? adding} source={editing?.source ?? adding!} status={editing ?? undefined}
            onDone={() => { setAdding(null); setEditing(null); }} />
        </div>
      </Panel>}
      <Panel title="Connections">
        <div className="panel-body">
          {list.length === 0
            ? <p>No connections yet. Pick a source below to get started.</p>
            : <table className="account-table">
              <thead><tr><th>Source</th><th>Account</th><th>Credential</th><th>Status</th><th>Actions</th></tr></thead>
              <tbody>
                {list.map(status => <ConnectionRow key={status.id} status={status} onEdit={() => { setAdding(null); setEditing(status); }} />)}
              </tbody>
            </table>}
        </div>
      </Panel>
      <SourceCatalog list={list} onPick={source => { setEditing(null); setAdding(source); }} />
    </div>
  </>;
}
