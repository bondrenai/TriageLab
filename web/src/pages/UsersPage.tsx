import { useState } from "react";
import { Check, Plus, UserCog, X } from "lucide-react";
import {
  useAccounts, useCreateAccount, useRemoveAccount, useSession, useSetAccountPassword, useUpdateAccount,
} from "../hooks/use-auth";
import { roleAtLeast, type Account, type Role } from "../lib/auth";
import { Button, ErrorPlate, Loading, Panel, PageTitle, Stamp } from "../components/ui";

const ROLES: Role[] = ["viewer", "user", "admin"];

const roleDescription = (role: Role) =>
  role === "admin" ? "Full control, including agents, procedures, and accounts"
    : role === "user" ? "Runs and steers investigations"
      : "Read-only access to reports and cases";

function RoleSelect({ value, onChange, disabled }: { value: Role; onChange: (role: Role) => void; disabled?: boolean }) {
  return <select className="field field-compact" value={value} onChange={e => onChange(e.target.value as Role)} disabled={disabled} aria-label="Role">
    {ROLES.map(role => <option key={role} value={role}>{role}</option>)}
  </select>;
}

function AccountRow({ account, self }: { account: Account; self: string }) {
  const update = useUpdateAccount();
  const remove = useRemoveAccount();
  const resetPassword = useSetAccountPassword();
  const [renaming, setRenaming] = useState(false);
  const [secret, setSecret] = useState("");
  const [confirmRemove, setConfirmRemove] = useState(false);
  const isSelf = account.username === self;
  const busy = update.isPending || remove.isPending || resetPassword.isPending;
  const error = update.error ?? remove.error ?? resetPassword.error;

  return <tr className={account.disabled ? "account-disabled" : undefined}>
    <td className="mono">{account.username}{isSelf && <span className="quiet"> (you)</span>}</td>
    <td>
      <RoleSelect
        value={account.role}
        disabled={isSelf || busy}
        onChange={role => update.mutate({ username: account.username, role })}
      />
    </td>
    <td>
      {account.disabled
        ? <Stamp hue="red">Disabled</Stamp>
        : account.has_password ? <Stamp hue="green">Active</Stamp> : <Stamp>No password</Stamp>}
    </td>
    <td className="account-actions">
      {renaming
        ? <form className="inline-form" onSubmit={event => {
            event.preventDefault();
            resetPassword.mutate({ username: account.username, password: secret }, { onSuccess: () => { setRenaming(false); setSecret(""); } });
          }}>
          <input className="field" type="password" placeholder="New password" autoComplete="new-password"
            minLength={8} required value={secret} onChange={e => setSecret(e.target.value)} aria-label={`New password for ${account.username}`} />
          <Button type="submit" disabled={resetPassword.isPending || secret.length < 8}><Check size={15} aria-hidden="true" />Save</Button>
          <Button type="button" variant="ghost" onClick={() => { setRenaming(false); setSecret(""); }}><X size={15} aria-hidden="true" />Cancel</Button>
        </form>
        : <div className="button-row">
          <Button type="button" onClick={() => setRenaming(true)} disabled={busy}>Set password</Button>
          <Button type="button" disabled={isSelf || busy}
            onClick={() => update.mutate({ username: account.username, disabled: !account.disabled })}>
            {account.disabled ? "Re-enable" : "Disable"}
          </Button>
          {confirmRemove
            ? <><Button type="button" variant="primary" disabled={isSelf || busy}
                onClick={() => remove.mutate(account.username, { onSuccess: () => setConfirmRemove(false) })}>Remove account</Button>
              <Button type="button" variant="ghost" onClick={() => setConfirmRemove(false)}>Keep</Button></>
            : <Button type="button" disabled={isSelf || busy} onClick={() => setConfirmRemove(true)}>Remove…</Button>}
        </div>}
      {error && !busy && <p className="account-error" role="alert">{error instanceof Error ? error.message : "The change was rejected."}</p>}
    </td>
  </tr>;
}

export function UsersPage() {
  const session = useSession();
  const accounts = useAccounts();
  const create = useCreateAccount();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const self = session.data?.username ?? "";

  if (!roleAtLeast(session.data?.role, "admin")) {
    return <>
      <PageTitle title="Accounts" description="Managing sign-in accounts requires the admin role." />
      <Panel title="Account management"><div className="panel-body"><p>Ask an administrator to change roles or accounts.</p></div></Panel>
    </>;
  }
  if (accounts.isError) return <ErrorPlate title="Accounts could not load" error={accounts.error} action={<Button onClick={() => void accounts.refetch()}>Try again</Button>} />;
  if (accounts.isLoading) return <Loading label="Loading accounts…" />;

  const users = accounts.data?.users ?? [];
  return <>
    <PageTitle title="Accounts" description="Local sign-in accounts. Single sign-on accounts are managed by the identity provider." />
    <div className="section-stack">
      <Panel title="Add an account">
        <div className="panel-body">
          <form className="account-create" onSubmit={event => {
            event.preventDefault();
            create.mutate({ username: username.trim(), password, role }, {
              onSuccess: () => { setUsername(""); setPassword(""); },
            });
          }}>
            <div>
              <label className="field-label" htmlFor="new-username">Username</label>
              <input id="new-username" className="field mono" autoComplete="off" spellCheck={false} required
                placeholder="pat.dev" value={username} onChange={e => setUsername(e.target.value)} />
            </div>
            <div>
              <label className="field-label" htmlFor="new-password">Initial password</label>
              <input id="new-password" className="field" type="password" autoComplete="new-password" required
                minLength={8} value={password} onChange={e => setPassword(e.target.value)} />
            </div>
            <div>
              <label className="field-label">Role
                <div className="role-picker">
                  <RoleSelect value={role} onChange={setRole} />
                  <span className="quiet">{roleDescription(role)}</span>
                </div>
              </label>
            </div>
            <Button type="submit" variant="primary" disabled={create.isPending || username.length < 1 || password.length < 8}>
              <Plus size={16} aria-hidden="true" />{create.isPending ? "Adding…" : "Add account"}
            </Button>
          </form>
          {create.isError && <p className="account-error" role="alert">{create.error instanceof Error ? create.error.message : "The account was not created."}</p>}
        </div>
      </Panel>
      <Panel title="Existing accounts" aside={<span className="quiet annotation"><UserCog size={14} aria-hidden="true" /> {users.length} account{users.length === 1 ? "" : "s"}</span>}>
        <div className="panel-body">
          <table className="account-table">
            <thead><tr><th>Username</th><th>Role</th><th>Status</th><th>Actions</th></tr></thead>
            <tbody>
              {users.map(account => <AccountRow key={account.username} account={account} self={self} />)}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  </>;
}
