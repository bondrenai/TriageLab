---
title: Pause and resume
description: Keep an investigation's workspace and conversation while waiting for information.
---

Investigations can wait for weeks without keeping active compute running.
TriageLab uses kagent's existing sandbox suspension and restoration, retaining
the same sandbox and investigator session until you resume or close the work.

## Pause

When the investigator returns blocking questions in `missing_information`,
TriageLab automatically pauses. You can also choose **Pause** on an active
report or use the CLI:

```sh
triagelab pause --config triagelab.json --report reports/<case-id>/report.json
```

Pause settles the current turn, stops outstanding experiments, collects their
evidence, and confirms sandbox suspension. Wait for **Paused** before shutting
down TriageLab. **Stop** and Ctrl-C still cancel and clean up the investigation.

## Resume with new information

Open a paused report, supply the additional information and files, review the
saved settings, and choose **Resume**. Ticket investigations require this
signed-in review; resuming does not send any helpdesk message.

Manual investigations also support:

```sh
triagelab resume --config triagelab.json --report reports/<case-id>/report.json \
  --description 'New information from the customer' --file ./new-evidence.txt
```

The saved agent, tools, and limits apply to a fresh bounded round in the same
conversation. New files go under `/data/workspace/rounds/<round>/`; existing
workspace files remain. Earlier reports stay available in the round selector
and under `rounds/` in the run directory. Old process handles are historical,
so current claims require newly collected evidence.

After a restart, use the same upstream endpoint and identity with a current
credential. TriageLab does not automatically resume paid work. If restoration
was interrupted before invocation, an explicit resume with the same additional
information can reconcile it. If a model invocation's outcome is uncertain,
TriageLab requires closing that investigation instead of risking a duplicate.

## Close and retain the reports

Choose **Close** when the investigation no longer needs its retained resources:

```sh
triagelab close --config triagelab.json --report reports/<case-id>/report.json
```

Close deletes the session and sandbox and keeps the reports. If cleanup fails,
retry Close after connectivity recovers. A completed investigation without
blocking questions cleans up automatically.

## Deployment and storage

Every controller must run the pinned
[bondrenai/kagent retention patch](https://github.com/bondrenai/kagent/commit/048e1016d50b209dba01065d0d10dfd80b78fb41).
The dedicated deployment must set `config.sessions.idleTtl: 0s`; the development
installer configures both. Released 1.0.0 controllers do not provide this retention
guarantee, and TriageLab rejects a sandbox without the retention flag.

Keep the entire private `reports/<case-id>/` directory, including `lifecycle.json`,
plus kagent's database and Substrate snapshot storage. Downloaded reports alone
cannot resume work. Credentials are not stored in the lifecycle record.

There is no automatic expiry while safely paused. Close unwanted investigations
to release snapshot storage and control-plane records. The first version allows
up to 32 rounds and 64 MiB of report history. Durability covers workspace files,
not process memory, running commands, or root filesystem edits outside durable
directories. Snapshot retention is not an independent backup.
