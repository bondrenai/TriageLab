---
title: API clients (OAuth)
description: Let your own application launch investigations and read results with an OAuth client instead of a browser session.
---

Sign-in covers people. API clients cover programs: an admin registers an
OAuth client, and your application exchanges its secret for a short-lived
access token that works on the same investigation API the browser uses —
nothing else.

## Create a client

On the workspace's **API clients** page (admin role), choose a client ID and
at least one scope. The page shows the generated client secret once; it is
not stored anywhere retrievable afterwards. Clients live beside the sign-in
accounts in `<output>/.auth/clients.json` (move it with `serve --clients`),
and only a SHA-256 of each secret is kept on disk.

Two scopes exist, and they are not symmetric:

| Scope | Grants |
| --- | --- |
| `investigations:run` | Launch investigations, and read, pause, resume, cancel, and close **the ones this client started** |
| `investigations:read` | Read **every** investigation in the workspace, including ticket investigations other people started |

A client with only `investigations:run` sees nothing but its own runs. Grant
`investigations:read` only when the application should see the whole
workspace's history — reports can contain customer data from tickets. Every
scope or secret change ends that client's outstanding tokens immediately.

## Get a token

The client credentials grant (RFC 6749 section 4.4) at `POST /oauth/token`,
with the client ID and secret over HTTP Basic authentication:

```sh
curl -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials \
  -d 'scope=investigations:run' \
  http://127.0.0.1:8080/oauth/token
```

```json
{
  "access_token": "tlat_…",
  "token_type": "Bearer",
  "expires_in": 3600,
  "scope": "investigations:run"
}
```

Omit `scope` to receive every granted scope; request a subset to down-scope a
token. Send the token as `Authorization: Bearer …` on API calls. Tokens are
memory-only like browser sessions: they last one hour and end on restart or
revocation, so request a new one whenever a call answers 401. There are no
refresh tokens. A client can end its own token early at `POST /oauth/revoke`
with `token=<access token>`.

## Call the API

Launch an investigation like the browser does — a multipart description with
optional files:

```sh
curl -H "Authorization: Bearer $TOKEN" \
  -F description="Payments service returns 500 for EU cards since 14:20 UTC" \
  http://127.0.0.1:8080/api/runs
```

The reply is `202 {"id":"<run-id>"}`. The workspace runs **one investigation
at a time**; launching during an active run answers `409`, so retry later.
Then poll the run until `status` is final and `active` is false:

```sh
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/runs/$RUN_ID
curl -H "Authorization: Bearer $TOKEN" -OJ \
  http://127.0.0.1:8080/api/runs/$RUN_ID/report.json
```

The run detail is the report: evidence in `experiments` and `tool_calls`, the
model's `analysis`, `outcome`, and `lifecycle`. `started_by` records
`client:<id>`. A client with `investigations:run` steers its own runs with
`POST /api/runs/$RUN_ID/{pause,resume,cancel,close}`; resuming needs the
`X-TriageLab-Resume-Round` and `X-TriageLab-Resume-Settings` headers from the
run's `lifecycle` view, exactly like the browser. Steering a paused run that
asks for more information is how an integration answers the investigator.

In Go, `golang.org/x/oauth2/clientcredentials` handles the token lifecycle:

```go
tokenSource := clientcredentials.Config{
    ClientID:     "ci-nightly-triage",
    ClientSecret: secret,
    TokenURL:     baseURL + "/oauth/token",
    Scopes:       []string{"investigations:run"},
}.TokenSource(ctx)
client := oauth2.NewClient(ctx, tokenSource)
```

## Rules and boundaries

- The API is reachable exactly like the workspace UI: over the loopback
  listener, through your port forward or SSH tunnel. There is no CORS, so a
  browser app on another origin cannot call it; integrations are server-side
  or CLI programs.
- A Bearer token authenticates only the routes above. Settings, tickets,
  agents, and account management answer 403; they need a signed-in human.
- API clients never touch ticket approval: resuming a ticket investigation
  stays bound to a signed-in review.
- Only the client credentials grant exists. There is no authorization-code
  flow, no refresh tokens, no token introspection, and no discovery document
  — additions will follow concrete integrator needs.
- Errors follow OAuth 2.0 (`invalid_client`, `invalid_scope`, …) on the token
  endpoints and RFC 6750 challenges (`WWW-Authenticate: Bearer …`) on API
  routes.
