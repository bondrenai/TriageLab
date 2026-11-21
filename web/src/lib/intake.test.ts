import assert from "node:assert/strict";
import test from "node:test";
import { filterRunHistory, isIgnoredFolderPath, prepareFolderSelection } from "./intake.ts";
import type { RunSummary } from "./types/investigation.ts";

test("prepareFolderSelection strips shared root folder and skips ignored build directories", () => {
  const result = prepareFolderSelection(
    [
      { name: "sync.py", webkitRelativePath: "repro/sync.py", size: 120 },
      { name: "orders.csv", webkitRelativePath: "repro/data/orders.csv", size: 400 },
      { name: "HEAD", webkitRelativePath: "repro/.git/HEAD", size: 23 },
      { name: "pkg.js", webkitRelativePath: "repro/node_modules/pkg/pkg.js", size: 800 },
      { name: "cache.pyc", webkitRelativePath: "repro/src/__pycache__/cache.pyc", size: 90 },
    ],
    true,
  );
  assert.equal(result.skippedIgnored, 3);
  assert.deepEqual(
    result.accepted.map(entry => entry.path),
    ["sync.py", "data/orders.csv"],
  );
});

test("isIgnoredFolderPath only checks directory segments", () => {
  assert.equal(isIgnoredFolderPath("dist/bundle.js"), true);
  assert.equal(isIgnoredFolderPath("sub/.venv/bin/python"), true);
  assert.equal(isIgnoredFolderPath("build.py"), false);
  assert.equal(isIgnoredFolderPath("src/dist.json"), false);
});

test("filterRunHistory filters by query text and outcome/attention chips", () => {
  const runs: RunSummary[] = [
    {
      id: "11111111-1111-4111-8111-111111111111",
      description: "Nightly order sync drops 11 orders",
      status: "completed",
      outcome: "reproduced",
      started_at: "2026-11-20T10:00:00Z",
      experiment_count: 4,
      active: false,
      round: 1,
      resource_state: "deleted",
    },
    {
      id: "22222222-2222-4222-8222-222222222222",
      description: "Invoice export mismatch",
      status: "completed",
      outcome: "not_reproduced",
      started_at: "2026-11-20T11:00:00Z",
      experiment_count: 2,
      active: false,
      round: 1,
      resource_state: "deleted",
    },
    {
      id: "33333333-3333-4333-8333-333333333333",
      description: "Timeout in API gateway",
      status: "paused",
      outcome: "inconclusive",
      started_at: "2026-11-20T12:00:00Z",
      experiment_count: 1,
      active: false,
      round: 1,
      resource_state: "retained",
      attention: { reason: "Waiting for additional information", action: "Add information and resume" },
    },
  ];
  assert.equal(filterRunHistory(runs, "", "reproduced").length, 1);
  assert.equal(filterRunHistory(runs, "", "not_reproduced").length, 1);
  assert.equal(filterRunHistory(runs, "", "attention").length, 1);
  assert.equal(filterRunHistory(runs, "orders", "all").length, 1);
  assert.equal(filterRunHistory(runs, "22222222", "all").length, 1);
  assert.equal(filterRunHistory(runs, "orders", "not_reproduced").length, 0);
});
