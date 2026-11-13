import { test } from "node:test";
import assert from "node:assert/strict";
import {
  buildWatchActivity,
  formatDurationSeconds,
  formatSandboxPath,
  resolveExpandedExperimentKeys,
  taskStateLabel,
} from "./watch.ts";
import type { Experiment, ToolCall } from "./types/investigation.ts";

test("formatSandboxPath simplifies workspace paths", () => {
  assert.equal(formatSandboxPath("/data/workspace/rowferry.py"), "rowferry.py");
  assert.equal(formatSandboxPath("/data/workspace"), ".");
  assert.equal(formatSandboxPath("/etc/hosts"), "/etc/hosts");
  assert.equal(formatSandboxPath(undefined), "unknown path");
});

test("buildWatchActivity maps file ops, commands, and collapses status polls without decoding base64", () => {
  const experiments: Experiment[] = [
    {
      call_id: "c3",
      process_id: "proc-1",
      command: ["python3", "rowferry.py", "--replay", "orders.csv"],
      cwd: "/data/workspace",
      state: "PROCESS_STATE_RUNNING",
      outputs_complete: false,
      stdout: "page 1",
    },
  ];
  const toolCalls: ToolCall[] = [
    {
      id: "c1",
      name: "read_sandbox_file",
      args: { sandbox_id: "sb", path: "/data/workspace/sync.json" },
      response: { output: { data_base64: "U0VDUkVU", size_bytes: 239 } },
    },
    {
      id: "c2",
      name: "write_sandbox_file",
      args: { sandbox_id: "sb", path: "/data/workspace/check.py", data_base64: "cHJpbnQoMSk=" },
      response: { output: { bytes_written: 8 } },
    },
    {
      id: "c3",
      name: "start_sandbox_process",
      args: { sandbox_id: "sb", command: ["python3", "rowferry.py"], cwd: "/data/workspace" },
      response: { output: { process_id: "proc-1" } },
    },
    {
      id: "c4",
      name: "get_sandbox_process",
      args: { sandbox_id: "sb", process_id: "proc-1" },
      response: { output: { process_id: "proc-1", state: "PROCESS_STATE_RUNNING" } },
    },
    {
      id: "c5",
      name: "read_sandbox_outputs",
      args: { sandbox_id: "sb", process_id: "proc-1" },
      response: { output: { stdout_base64: "cGFnZSAx", stderr_base64: "" } },
    },
    {
      id: "c6",
      name: "lookup_docs",
      args: { query: "cursor pagination" },
      pending: true,
    },
  ];

  const items = buildWatchActivity(toolCalls, experiments);
  assert.equal(items.length, 5);
  assert.equal(items[0].kind, "read_file");
  assert.equal(items[0].detail, "sync.json");
  assert.equal(items[0].meta, "239 B");
  assert.ok(!JSON.stringify(items).includes("U0VDUkVU"));
  assert.ok(!JSON.stringify(items).includes("cHJpbnQoMSk="));

  assert.equal(items[1].kind, "write_file");
  assert.equal(items[1].detail, "check.py");
  assert.equal(items[1].meta, "8 B");

  assert.equal(items[2].kind, "command");
  assert.equal(items[2].label, "Experiment 01");
  assert.equal(items[2].statusLabel, "Running");
  assert.equal(items[2].processId, "proc-1");

  assert.equal(items[3].kind, "poll_group");
  assert.equal(items[3].detail, "2 polls · proc-1");

  assert.equal(items[4].kind, "tool");
  assert.equal(items[4].label, "lookup_docs");
  assert.equal(items[4].detail, "query: cursor pagination");
  assert.equal(items[4].statusLabel, "Pending");
});

test("resolveExpandedExperimentKeys auto-expands active and latest experiments while honoring manual toggles", () => {
  const experiments: Experiment[] = [
    { process_id: "p1", command: ["ls"], state: "PROCESS_STATE_EXITED", exit_code: 0 },
    { process_id: "p2", command: ["sleep", "5"], state: "PROCESS_STATE_RUNNING" },
    { process_id: "p3", command: ["python3", "test.py"], state: "PROCESS_STATE_EXITED", exit_code: 1 },
  ];

  const initial = resolveExpandedExperimentKeys(1, experiments, true, {});
  assert.deepEqual([...initial].sort(), ["1:p2", "1:p3"]);

  const withToggles = resolveExpandedExperimentKeys(1, experiments, true, {
    "1:p1": true,
    "1:p3": false,
  });
  assert.deepEqual([...withToggles].sort(), ["1:p1", "1:p2"]);

  const inactive = resolveExpandedExperimentKeys(1, experiments, false, { "1:p1": true });
  assert.deepEqual([...inactive], ["1:p1"]);
});

test("duration and task state labels format cleanly", () => {
  assert.equal(formatDurationSeconds(42), "42s");
  assert.equal(formatDurationSeconds(125), "2m 5s");
  assert.equal(taskStateLabel("TASK_STATE_WORKING", "running", "running"), "Agent working");
  assert.equal(taskStateLabel("TASK_STATE_WORKING", "running", "creating"), "Provisioning sandbox");
});
