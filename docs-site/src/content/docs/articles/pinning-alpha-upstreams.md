---
title: Pinning alpha upstreams
description: How TriageLab integrates KAgent rc3 and Substrate 1.0 without inheriting their churn.
---

TriageLab sits on two young upstreams: KAgent `v1.0.0-rc3` and its matching
Agent Substrate `v1.0.0` packaging. Both are moving — the current 1.x alpha
schemas differ meaningfully from older public examples — and an investigation
tool whose substrate shifts underneath it is worse than no tool. This is the
discipline we use to depend on alpha software without inheriting its churn.

## Pin to commits, not versions alone

The [upstream compatibility record](/TriageLab/reference/upstream/) lists each
dependency's version *and* the exact commit identity it was reviewed against.
A tag can be moved; a commit hash cannot. When we say "KAgent rc3," we mean
`3548b7aaf4d91ec86b0565df2bd8a9950e9a276d`, and every interface claim in the
record was read at that commit — not copied from a README that may predate a
breaking change.

The record also distinguishes things that are easy to confuse. There are two
Substrate repositories: the core project and KAgent's packaging fork of it.
KAgent's Go module requires one and replaces it with the other. Installing
both installers duplicates infrastructure. The only way to catch that class
of problem is to read the module graph and the installers yourself, at the
pinned commits.

## Read the contract before the example

The biggest schema trap in this stack: a standalone **Sandbox is an
owner-scoped service object** in KAgent's API — a database-backed resource
mapped to a Substrate Actor — while **SandboxTemplate** is the Kubernetes
CRD. Older examples blur this distinction, and code written against the wrong
one appears to work until lifecycle operations start failing. Similar traps:
`api.kagent.dev/v1alpha3` harness references replacing 0.x inline shapes, and
structured output validating through the harness rather than ad-hoc parsing.

The rule we follow: research the pinned upstream source before changing an
integration, and record which interfaces are used and which constraints were
discovered there — like process start having no idempotency receipt, or MCP
output pages being capped at 1 MiB. Those constraints shape client code far
more than the happy-path examples do.

## Upgrade as a procedure, not a bump

When a new release lands, the upgrade is a rehearsed sequence: review the CRD
schemas, protobuf configuration, and MCP input/output schemas at the new
commit; update the CLI, chart, controller, guest, and runtime images
*together*; resolve and record new image digests from their registries;
validate rendered manifests; then run the real demo on a fresh disposable
cluster and record exactly which versions and checks ran. Compatibility is
never inferred from unchanged tool names — names survive semantic changes
routinely in alphas.

## What we don't do

We don't track `main`. We don't vendor behavior into local approximations —
if upstream provides a security mechanism (isolation, egress policy, token
verification), we use it rather than reimplementing a weaker local version.
And we don't pretend the alpha boundary doesn't exist: KAgent's shipped
authorizer is not a production tenant policy, so TriageLab's development
setup stays on a dedicated cluster and says so plainly.

Alpha upstreams are a bargain — you get capabilities that would take a year
to build — but only if you hold their contracts in writing. The compatibility
record is that writing, and every integration change updates it in the same
commit.
