import { Fragment, type HTMLAttributes, type ReactNode } from "react";
import { parseBlocks, parseInline, type Block, type Inline } from "../lib/markdown";

// Untrusted text rendered through the safe subset in lib/markdown.ts. Every node
// is a React element or string, so the text never becomes HTML; links stay text
// with their URL visible, and image syntax stays literal.

export function MarkdownBlocks({ text, className, ...rest }: { text: string; className?: string } & Omit<HTMLAttributes<HTMLDivElement>, "children">) {
  return <div {...rest} className={className ? `markdown ${className}` : "markdown"}>{parseBlocks(text).map(renderBlock)}</div>;
}

export function MarkdownInline({ text }: { text: string }) {
  return <>{renderInline(parseInline(text))}</>;
}

function renderBlock(block: Block, index: number): ReactNode {
  switch (block.kind) {
    case "paragraph": return <p key={index}>{renderInline(block.content)}</p>;
    case "heading": return <p key={index} className={`md-heading md-heading-${Math.min(block.level, 3)}`}>{renderInline(block.content)}</p>;
    case "code": return <pre key={index} className="md-code"><code>{block.text}</code></pre>;
    case "quote": return <blockquote key={index} className="md-quote">{renderInline(block.content)}</blockquote>;
    case "rule": return <hr key={index} className="md-rule" />;
    case "list": {
      const items = block.items.map((item, itemIndex) => <li key={itemIndex}>{renderInline(item)}</li>);
      return block.ordered
        ? <ol key={index} className="md-list numbered" start={block.start}>{items}</ol>
        : <ul key={index} className="md-list">{items}</ul>;
    }
  }
}

function renderInline(nodes: Inline[]): ReactNode {
  return nodes.map((node, index) => {
    switch (node.kind) {
      case "text": return <Fragment key={index}>{node.text}</Fragment>;
      case "break": return <br key={index} />;
      case "code": return <code key={index} className="md-inline-code">{node.text}</code>;
      case "strong": return <strong key={index}>{renderInline(node.children)}</strong>;
      case "em": return <em key={index}>{renderInline(node.children)}</em>;
      case "link": return <span key={index} className="md-link">{renderInline(node.children)} <span className="md-link-url">({node.url})</span></span>;
    }
  });
}
