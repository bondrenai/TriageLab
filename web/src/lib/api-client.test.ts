import { test } from "node:test";
import assert from "node:assert/strict";
import { APIError, request } from "./api-client.ts";

test("request dispatches triagelab:unauthorized on 401 except for /api/auth/login", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const events: string[] = [];

  try {
    globalThis.window = {
      dispatchEvent(event: Event) {
        events.push(event.type);
        return true;
      },
    } as unknown as Window & typeof globalThis;

    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ error: "Sign in to use the workspace" }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      })) as typeof fetch;

    await assert.rejects(() => request("/api/auth/login", { method: "POST" }), APIError);
    assert.deepEqual(events, []);

    await assert.rejects(() => request("/api/auth/admin/users"), APIError);
    assert.deepEqual(events, ["triagelab:unauthorized"]);

    await assert.rejects(() => request("/api/runs"), APIError);
    assert.deepEqual(events, ["triagelab:unauthorized", "triagelab:unauthorized"]);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.window = originalWindow;
  }
});
