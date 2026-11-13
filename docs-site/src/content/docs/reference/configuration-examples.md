---
title: "Configuration examples"
description: "Every committed example configuration, embedded verbatim from the repository root."
---

This page is generated from the CLI source by `triagelab docs`; run `make docs-generate` after changing commands, flags, or configuration. Do not edit it by hand.

Copy an example to a private file, fill in your values, and pass it to the matching flag. Credentials are always named by environment variables (`token_env`, `client_secret_env`, `headers_env`), never written in these files.

## Investigation configuration

`triagelab.example.json` — The core configuration passed to `--config`; defaults shown in the [configuration](/TriageLab/reference/configuration/) table.

```json
{
  "endpoint": "http://127.0.0.1:8083/mcp",
  "namespace": "kagent",
  "agent": "triagelab-investigator",
  "template": "triagelab-python",
  "user_id": "triagelab@localhost",
  "token_env": "TRIAGELAB_TOKEN",
  "investigation_timeout": 600,
  "process_timeout": 60,
  "sandbox_ttl": 900,
  "max_experiments": 8,
  "poll_interval": 2,
  "request_timeout": 20,
  "max_output_bytes": 1048576,
  "run_namespaces": ["triagelab-runs"]
}
```

## Sign-in configuration

`auth.example.json` — Optional file passed to `serve --auth` configuring one OpenID Connect provider; see [sign-in and roles](/TriageLab/guides/sign-in/).

```json
{
  "oidc": {
    "issuer": "https://sso.example.com/realms/triagelab",
    "client_id": "triagelab",
    "client_secret_env": "TRIAGELAB_OIDC_CLIENT_SECRET",
    "scopes": ["openid", "profile", "email"],
    "username_claim": "preferred_username",
    "groups_claim": "groups",
    "role_map": {
      "triagelab-admins": "admin",
      "triagelab-users": "user"
    },
    "default_role": "viewer"
  }
}
```

## Results chat

`triagelab.chat.example.json` — Optional file passed to `serve --chat` selecting a tool-free completion endpoint; see [results chat and MCP](/TriageLab/guides/chat-mcp/).

```json
{
  "endpoint": "http://127.0.0.1:3000/v1/chat/completions",
  "model": "results-chat",
  "api_key_env": "TRIAGELAB_CHAT_API_KEY",
  "timeout_seconds": 60,
  "max_tokens": 2048
}
```

## HTTP MCP access

`triagelab.mcp.example.json` — Optional file passed to `serve --mcp` defining scoped machine credentials; see [results chat and MCP](/TriageLab/guides/chat-mcp/).

```json
{
  "principals": [
    {
      "name": "gateway",
      "role": "user",
      "token_env": "TRIAGELAB_MCP_TOKEN"
    }
  ],
  "allowed_origins": []
}
```

## Zendesk ticket source

`tickets.example.json` — Passed to `serve --tickets` for the Zendesk case source; see [Zendesk tickets](/TriageLab/guides/tickets-zendesk/).

```json
{
  "subdomain": "your-zendesk-subdomain",
  "client_id": "triagelab-local",
  "redirect_uri": "http://127.0.0.1:8080/oauth/zendesk/callback",
  "poll_seconds": 60,
  "internal_notes": false,
  "policy": {
    "tag": "triagelab_python_timeout",
    "priorities": [],
    "auto_questions": false,
    "max_questions": 2
  }
}
```

## GitHub issue source

`github.example.json` — Passed to `serve --tickets` for one GitHub.com repository; see [GitHub issues](/TriageLab/guides/tickets-github/).

```json
{
  "source": "github",
  "repository": "your-owner/your-repository",
  "token_env": "TRIAGELAB_GITHUB_TOKEN",
  "poll_seconds": 60,
  "investigation_comments": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

## GitLab issue source

`gitlab.example.json` — Passed to `serve --tickets` for one GitLab.com or self-managed project; see [GitLab issues](/TriageLab/guides/tickets-gitlab/).

```json
{
  "source": "gitlab",
  "repository": "your-group/your-project",
  "origin": "https://gitlab.example.com",
  "token_env": "TRIAGELAB_GITLAB_TOKEN",
  "poll_seconds": 60,
  "investigation_comments": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

## Linear issue source

`linear.example.json` — Passed to `serve --tickets` for one Linear team; see [Linear issues](/TriageLab/guides/tickets-linear/).

```json
{
  "source": "linear",
  "workspace": "your-workspace",
  "team": "ENG",
  "token_env": "TRIAGELAB_LINEAR_TOKEN",
  "poll_seconds": 60,
  "investigation_comments": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

## Bondren issue source

`bondren.example.json` — Passed to `serve --tickets` for one Bondren workspace origin; see [Bondren issues](/TriageLab/guides/tickets-bondren/).

```json
{
  "source": "bondren",
  "origin": "https://app.bondren.example",
  "token_env": "TRIAGELAB_BONDREN_TOKEN",
  "poll_seconds": 60,
  "internal_notes": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

## Jira issue source

`jira.example.json` — Passed to `serve --tickets` for one Jira Cloud site; see [Jira issues](/TriageLab/guides/tickets-jira/).

```json
{
  "source": "jira",
  "origin": "https://your-site.atlassian.net",
  "email": "triagelab@your-company.example",
  "token_env": "TRIAGELAB_JIRA_TOKEN",
  "poll_seconds": 60,
  "internal_notes": true,
  "policy": {
    "tag": "triagelab_python_timeout",
    "auto_questions": true,
    "max_questions": 2
  }
}
```

## Telemetry configuration

`triagelab.telemetry.example.json` — Optional file passed to `--telemetry` for OTLP/HTTP export; see [telemetry](/TriageLab/guides/telemetry/).

```json
{
  "endpoint": "http://127.0.0.1:4318",
  "headers_env": "TRIAGELAB_OTLP_HEADERS",
  "sample_ratio": 1.0,
  "export_interval_seconds": 30
}
```

