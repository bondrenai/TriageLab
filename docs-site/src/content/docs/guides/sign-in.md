---
title: Sign-in and roles
description: Local accounts and one optional OIDC provider, with viewer/user/admin roles enforced server-side.
---

The workspace fails closed without a usable login method — an open workspace
never starts. Sessions are memory-only cookies, so everyone signs in again
after a restart. Two methods can be configured, and they may coexist.

## Local accounts

Local accounts live in `<output>/.auth/users.json` beside the reports, hashed
with PBKDF2-SHA256. Bootstrap and manage them from the CLI:

```sh
echo 'initial-password' | bin/triagelab user add --username admin --role admin --password-stdin
bin/triagelab user list
```

The first account defaults to `admin`; later accounts default to
least-privilege `viewer`. Admins can also manage accounts on the workspace's
Accounts page. Manage accounts from the CLI while the server is stopped — a
running server holds its own copy and would overwrite CLI changes on its next
save. The full `user` command surface is in the [CLI reference](/TriageLab/reference/cli/).

## Single sign-on

One OpenID Connect provider can be configured through an optional file passed
to `serve --auth auth.json`:

```json
{
  "oidc": {
    "issuer": "https://sso.example.com/realms/triagelab",
    "client_id": "triagelab",
    "client_secret_env": "TRIAGELAB_OIDC_CLIENT_SECRET",
    "scopes": ["openid", "profile", "email"],
    "username_claim": "preferred_username",
    "groups_claim": "groups",
    "role_map": { "triagelab-admins": "admin", "triagelab-users": "user" },
    "default_role": "viewer"
  }
}
```

The client secret is read from an environment variable, never the file. Roles
come from a direct role claim or by mapping a groups claim through `role_map`;
accounts that match no mapping receive `default_role` (least-privilege
`viewer` unless configured otherwise). The redirect URI defaults to
`http://127.0.0.1:8080/api/auth/oidc/callback` — register that exact value
with the provider.

## Roles

Three roles are enforced server-side on every API route:

| Role | Can | Cannot |
| --- | --- | --- |
| viewer | Read reports, cases, deployment status | Change anything |
| user | Everything a viewer can, plus launching and steering investigations and ticket actions | Define agents, sandbox templates, egress policies, procedures, or accounts |
| admin | Everything, including saving KAgent definitions, ticket procedures, and account management | |

Each report records `started_by`. Sign-in attempts are rate-limited per
account. There is no per-user action audit log yet, and roles do not isolate
accounts from each other's reports on the shared volume — this remains a
single-user workspace reached over a tunnel.
