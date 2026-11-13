// A small, safe Markdown subset for untrusted text: imported case descriptions
// and model output. It returns a tree that components render as React elements,
// so nothing is ever interpreted as HTML. By design it has no images, no live
// links and no raw HTML: image syntax and HTML stay literal text, and a link
// keeps its URL visible as text without becoming an anchor. Exact-text
// previews of what TriageLab will post elsewhere do not use it.
//
// Plain TypeScript with no JSX, so `node --test` can run its tests directly.

export type Inline =
  | { kind: "text"; text: string }
  | { kind: "code"; text: string }
  | { kind: "strong"; children: Inline[] }
  | { kind: "em"; children: Inline[] }
  | { kind: "link"; children: Inline[]; url: string }
  | { kind: "break" };

export type Block =
  | { kind: "paragraph"; content: Inline[] }
  | { kind: "heading"; level: number; content: Inline[] }
  | { kind: "code"; text: string }
  | { kind: "list"; ordered: boolean; start: number; items: Inline[][] }
  | { kind: "quote"; content: Inline[] }
  | { kind: "rule" };

// Inline parsing is quadratic on hostile input, so a longer line stays plain text.
export const MAX_INLINE_LINE = 4000;

const fenceOpen = /^ {0,3}(`{3,}|~{3,})[^`]*$/;
const heading = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;
const rule = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const quote = /^ {0,3}>[ ]?(.*)$/;
const bullet = /^ {0,3}[-+*][ \t]+(.*)$/;
const ordered = /^ {0,3}(\d{1,9})[.)][ \t]+(.*)$/;
const escapable = "\\`*_[]()#+-.!>~|{}";
const whitespace = /\s/;

function startsBlock(line: string): boolean {
  return fenceOpen.test(line) || heading.test(line) || rule.test(line) || quote.test(line) || bullet.test(line) || ordered.test(line);
}

/** Parse untrusted text into blocks. Single newlines inside a block are kept as line breaks. */
export function parseBlocks(source: string): Block[] {
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") { i++; continue; }

    const fence = fenceOpen.exec(line);
    if (fence) {
      const marker = fence[1];
      const body: string[] = [];
      i++;
      while (i < lines.length && !new RegExp(`^ {0,3}${marker[0] === "`" ? "`" : "~"}{${marker.length},}[ \\t]*$`).test(lines[i])) body.push(lines[i++]);
      i++; // the closing fence, or past the end when it was never closed
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }
    if (rule.test(line)) { blocks.push({ kind: "rule" }); i++; continue; }
    const head = heading.exec(line);
    if (head) {
      blocks.push({ kind: "heading", level: head[1].length, content: parseInline(head[2] ?? "") });
      i++;
      continue;
    }
    if (quote.test(line)) {
      const body: string[] = [];
      while (i < lines.length && quote.test(lines[i])) body.push(quote.exec(lines[i++])![1]);
      blocks.push({ kind: "quote", content: parseInline(body.join("\n")) });
      continue;
    }
    const firstBullet = bullet.exec(line), firstOrdered = firstBullet ? null : ordered.exec(line);
    if (firstBullet || firstOrdered) {
      const isOrdered = !!firstOrdered;
      const marker = isOrdered ? ordered : bullet;
      const items: string[][] = [];
      while (i < lines.length) {
        const current = lines[i];
        const item = marker.exec(current);
        if (item && !rule.test(current)) { items.push([isOrdered ? item[2] : item[1]]); i++; continue; }
        if (current.trim() === "") {
          // A blank line continues the list only if another item of the same kind follows.
          let next = i + 1;
          while (next < lines.length && lines[next].trim() === "") next++;
          if (next < lines.length && marker.test(lines[next]) && !rule.test(lines[next])) { i = next; continue; }
          break;
        }
        if (startsBlock(current)) break;
        items[items.length - 1].push(current.trim()); // continuation of the current item
        i++;
      }
      blocks.push({
        kind: "list", ordered: isOrdered, start: isOrdered ? Number(firstOrdered![1]) : 1,
        items: items.map(parts => parseInline(parts.join("\n"))),
      });
      continue;
    }
    const body: string[] = [];
    while (i < lines.length && lines[i].trim() !== "" && (body.length === 0 || !startsBlock(lines[i]))) body.push(lines[i++]);
    blocks.push({ kind: "paragraph", content: parseInline(body.join("\n")) });
  }
  return blocks;
}

/** Parse inline syntax. Newlines become line breaks; nothing crosses a line. */
export function parseInline(source: string): Inline[] {
  const out: Inline[] = [];
  source.replace(/\r\n?/g, "\n").split("\n").forEach((line, index) => {
    if (index > 0) out.push({ kind: "break" });
    if (line.length > MAX_INLINE_LINE) push(out, line);
    else out.push(...parseLine(line));
  });
  return out;
}

function push(out: Inline[], text: string) {
  if (!text) return;
  const last = out[out.length - 1];
  if (last && last.kind === "text") last.text += text;
  else out.push({ kind: "text", text });
}

function parseLine(line: string): Inline[] {
  const out: Inline[] = [];
  let i = 0;
  while (i < line.length) {
    const ch = line[i];
    if (ch === "\\" && i + 1 < line.length && escapable.includes(line[i + 1])) { push(out, line[i + 1]); i += 2; continue; }
    if (ch === "`") {
      let run = 1;
      while (line[i + run] === "`") run++;
      const close = findRun(line, "`", run, i + run);
      if (close < 0) { push(out, "`".repeat(run)); i += run; continue; }
      let text = line.slice(i + run, close);
      if (text.length > 1 && text.startsWith(" ") && text.endsWith(" ") && text.trim()) text = text.slice(1, -1);
      out.push({ kind: "code", text });
      i = close + run;
      continue;
    }
    // Only asterisks emphasise: underscores stay literal so identifiers such as
    // __init__.py and snake_case read as written in technical text.
    if (ch === "*") {
      const strong = line[i + 1] === ch;
      const width = strong ? 2 : 1;
      const close = findEmphasisClose(line, ch, width, i);
      if (close > i + width) {
        const children = parseLine(line.slice(i + width, close));
        out.push(strong ? { kind: "strong", children } : { kind: "em", children });
        i = close + width;
        continue;
      }
      push(out, ch.repeat(width));
      i += width;
      continue;
    }
    if (ch === "[" && line[i - 1] !== "!") {
      const link = /^\[([^\]\n]{1,500})\]\(([^()\s]{1,2000})\)/.exec(line.slice(i));
      if (link) {
        out.push({ kind: "link", children: parseLine(link[1]), url: link[2] });
        i += link[0].length;
        continue;
      }
    }
    push(out, ch);
    i++;
  }
  return out;
}

// The index of the next run of exactly `width` copies of `ch` at or after `from`.
function findRun(line: string, ch: string, width: number, from: number): number {
  for (let i = line.indexOf(ch, from); i >= 0; i = line.indexOf(ch, i + 1)) {
    let run = 1;
    while (line[i + run] === ch) run++;
    if (run === width && line[i - 1] !== ch) return i;
    i += run - 1;
  }
  return -1;
}

// Emphasis opens before non-space text and closes after non-space text.
function findEmphasisClose(line: string, ch: string, width: number, open: number): number {
  const after = line[open + width];
  if (after === undefined || whitespace.test(after) || after === ch) return -1;
  const marker = ch.repeat(width);
  for (let i = line.indexOf(marker, open + width); i >= 0; i = line.indexOf(marker, i + 1)) {
    if (line[i + width] === ch || (width === 1 && line[i - 1] === ch)) continue;
    if (whitespace.test(line[i - 1])) continue;
    return i;
  }
  return -1;
}
