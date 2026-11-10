import { useState } from "react";
import { Check, Copy, KeyRound, Plus, X } from "lucide-react";
import {
  useClients, useCreateClient, useRemoveClient, useRotateClientSecret, useSession, useUpdateClient,
} from "../hooks/use-auth";
import { roleAtLeast, type APIClient, type Scope } from "../lib/auth";
import { Button, ErrorPlate, Loading, Panel, PageTitle, Stamp } from "../components/ui";

const SCOPE_DESCRIPTIONS: Record<Scope, string> = {
  "investigations:run": "Launch investigations, and read and steer the ones this client starts",
  "investigations:read": "Read every investigation in the workspace, including other people's",
};

const VALID_ID = /^[a-z0-9]([a-z0-9._-]{0,30}[a-z0-9])?$/;

function ScopeCheckboxes({ scopes, onToggle, disabledScope }: {
  scopes: Scope[];
  onToggle: (scope: Scope, granted: boolean) => void;
  disabledScope?: (scope: Scope) => boolean;
}) {
  const all = Object.keys(SCOPE_DESCRIPTIONS) as Scope[];
  return <div className="scope-options">
    {all.map(scope => <label key={scope} className="scope-option">
      <input type="checkbox" checked={scopes.includes(scope)} disabled={disabledScope?.(scope) ?? false}
        onChange={e => onToggle(scope, e.target.checked)} />
      <span><span className="mono">{scope}</span>
        <span className="scope-description">{SCOPE_DESCRIPTIONS[scope]}</span></span>
    </label>)}
  </div>;
}

/** The one-time secret after creation or rotation. Kept in page state only. */
function SecretReveal({ id, secret, onDone }: { id: string; secret: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  return <Panel title={`Client secret for ${id}`} className="secret-plate">
    <div className="panel-body">
      <p className="secret-warning" role="alert">Copy the secret now. It is not stored and cannot be shown again.</p>
      <div className="secret-row">
        <input className="field mono secret-value" type="text" readOnly value={secret}
          aria-label={`Client secret for ${id}`} onFocus={e => e.currentTarget.select()} />
        <Button type="button" onClick={() => {
          navigator.clipboard?.writeText(secret).then(() => setCopied(true)).catch(() => setCopied(false));
        }}>
          {copied ? <Check size={15} aria-hidden="true" /> : <Copy size={15} aria-hidden="true" />}
          {copied ? "Copied" : "Copy"}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}><X size={15} aria-hidden="true" />Done</Button>
      </div>
      <p className="quiet annotation">Use this client ID and secret over HTTP Basic authentication at the token endpoint.</p>
    </div>
  </Panel>;
}

function ClientRow({ client, onReveal }: { client: APIClient; onReveal: (id: string, secret: string) => void }) {
  const update = useUpdateClient();
  const remove = useRemoveClient();
  const rotate = useRotateClientSecret();
  const [confirmRemove, setConfirmRemove] = useState(false);
  const busy = update.isPending || remove.isPending || rotate.isPending;
  const error = update.error ?? remove.error ?? rotate.error;

  return <tr className={client.disabled ? "account-disabled" : undefined}>
    <td className="mono">{client.id}
      {client.created_by && <span className="quiet"> by {client.created_by}</span>}
    </td>
    <td>
      <ScopeCheckboxes scopes={client.scopes} disabledScope={scope => busy || client.disabled ||
        (client.scopes.length === 1 && client.scopes[0] === scope)}
        onToggle={(scope, granted) => update.mutate({
          id: client.id,
          scopes: granted ? [...client.scopes, scope] : client.scopes.filter(name => name !== scope),
        })} />
    </td>
    <td>{client.disabled ? <Stamp hue="red">Disabled</Stamp> : <Stamp hue="green">Active</Stamp>}</td>
    <td className="account-actions">
      <div className="button-row">
        <Button type="button" disabled={busy}
          onClick={() => rotate.mutate(client.id, { onSuccess: result => onReveal(result.client_id, result.client_secret) })}>
          Rotate secret
        </Button>
        <Button type="button" disabled={busy}
          onClick={() => update.mutate({ id: client.id, disabled: !client.disabled })}>
          {client.disabled ? "Re-enable" : "Disable"}
        </Button>
        {confirmRemove
          ? <><Button type="button" variant="primary" disabled={busy}
              onClick={() => remove.mutate(client.id, { onSuccess: () => setConfirmRemove(false) })}>Remove client</Button>
            <Button type="button" variant="ghost" onClick={() => setConfirmRemove(false)}>Keep</Button></>
          : <Button type="button" disabled={busy} onClick={() => setConfirmRemove(true)}>Remove…</Button>}
      </div>
      {error && !busy && <p className="account-error" role="alert">{error instanceof Error ? error.message : "The change was rejected."}</p>}
    </td>
  </tr>;
}

export function ApiClientsPage() {
  const session = useSession();
  const clients = useClients();
  const create = useCreateClient();
  const [id, setId] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(["investigations:run"]);
  const [reveal, setReveal] = useState<{ id: string; secret: string } | null>(null);
  const validId = VALID_ID.test(id);

  if (!roleAtLeast(session.data?.role, "admin")) {
    return <>
      <PageTitle title="API clients" description="Managing API clients requires the admin role." />
      <Panel title="API clients"><div className="panel-body"><p>Ask an administrator to create or change API clients.</p></div></Panel>
    </>;
  }
  if (clients.isError) return <ErrorPlate title="API clients could not load" error={clients.error} action={<Button onClick={() => void clients.refetch()}>Try again</Button>} />;
  if (clients.isLoading) return <Loading label="Loading API clients…" />;

  const listing = clients.data?.clients ?? [];
  const tokenURL = `${window.location.origin}/oauth/token`;
  return <>
    <PageTitle title="API clients"
      description="OAuth clients let your own applications launch investigations and read results over the API, with a secret instead of a browser session." />
    <div className="section-stack">
      {reveal && <SecretReveal id={reveal.id} secret={reveal.secret} onDone={() => setReveal(null)} />}
      <Panel title="Add an API client">
        <div className="panel-body">
          <form className="account-create" onSubmit={event => {
            event.preventDefault();
            create.mutate({ id: id.trim(), scopes }, {
              onSuccess: result => {
                setReveal({ id: result.client_id, secret: result.client_secret });
                setId("");
                setScopes(["investigations:run"]);
              },
            });
          }}>
            <div>
              <label className="field-label" htmlFor="new-client-id">Client ID</label>
              <input id="new-client-id" className="field mono" autoComplete="off" spellCheck={false} required
                placeholder="ci-nightly-triage" value={id} onChange={e => setId(e.target.value)}
                aria-describedby="new-client-id-help" />
              <p className="field-help" id="new-client-id-help">Lowercase letters, digits, and inner "-", ".", "_".</p>
            </div>
            <div className="account-create-wide">
              <span className="field-label">Scopes</span>
              <ScopeCheckboxes scopes={scopes} disabledScope={() => create.isPending}
                onToggle={(scope, granted) => setScopes(current =>
                  granted ? [...new Set([...current, scope])] : current.filter(name => name !== scope))} />
            </div>
            <Button type="submit" variant="primary" disabled={create.isPending || !validId || scopes.length === 0}>
              <Plus size={16} aria-hidden="true" />{create.isPending ? "Creating…" : "Create client"}
            </Button>
          </form>
          {create.isError && <p className="account-error" role="alert">{create.error instanceof Error ? create.error.message : "The client was not created."}</p>}
        </div>
      </Panel>
      <Panel title="Existing clients" aside={<span className="quiet annotation"><KeyRound size={14} aria-hidden="true" /> {listing.length} client{listing.length === 1 ? "" : "s"}</span>}>
        <div className="panel-body">
          {listing.length === 0
            ? <p className="quiet">No API clients yet. Create one to connect an external application.</p>
            : <table className="account-table">
              <thead><tr><th>Client ID</th><th>Scopes</th><th>Status</th><th>Actions</th></tr></thead>
              <tbody>
                {listing.map(client => <ClientRow key={client.id} client={client} onReveal={(id, secret) => setReveal({ id, secret })} />)}
              </tbody>
            </table>}
        </div>
      </Panel>
      <Panel title="How an application connects">
        <div className="panel-body">
          <p>Exchange the client credentials for a short-lived access token, then send it as a Bearer token.</p>
          <pre className="code-note mono"><code>{`curl -u "$CLIENT_ID:$CLIENT_SECRET" \\
  -d grant_type=client_credentials \\
  ${tokenURL}

curl -H "Authorization: Bearer $ACCESS_TOKEN" \\
  -F description="A reported issue" \\
  ${window.location.origin}/api/runs`}</code></pre>
          <ul className="connect-notes">
            <li>Tokens live one hour and end on restart or revocation; request a new one when a call answers 401.</li>
            <li>The workspace runs one investigation at a time; launching during an active run answers 409.</li>
            <li>The API is reachable the same way this page is: over the loopback port-forward, never cross-origin.</li>
          </ul>
        </div>
      </Panel>
    </div>
  </>;
}
