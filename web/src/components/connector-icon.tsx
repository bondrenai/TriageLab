import githubMark from "../assets/connectors/github.svg?url";
import jiraMark from "../assets/connectors/jira.svg?url";
import linearMark from "../assets/connectors/linear.svg?url";
import zendeskMark from "../assets/connectors/zendesk.svg?url";
import type { TicketSource } from "../lib/types/ticket";

// Third-party brand glyphs render as ink masks: the mask paints by shape, so
// every connector reads in the same ink over the drafting-paper surface. The
// Bondren mark is first-party and two-ink (graphite plates with the
// ultramarine accent, per the brand guide's vellum form), so it renders
// inline instead of masked.
const connectorIcons: Partial<Record<TicketSource, string>> = {
  github: githubMark,
  jira: jiraMark,
  linear: linearMark,
  zendesk: zendeskMark,
};

function BondrenMark() {
  return <svg viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <rect x="2" y="3" width="17" height="10" fill="#151A21" />
    <rect x="22" y="3" width="8" height="10" fill="#151A21" />
    <rect x="2" y="17" width="8" height="10" fill="#151A21" />
    <rect x="13" y="17" width="17" height="10" fill="#1F3FD8" />
  </svg>;
}

export function ConnectorIcon({ source, connected = false }: { source: TicketSource; connected?: boolean }) {
  const icon = connectorIcons[source];
  return <span className={connected ? "connector-frame connected" : "connector-frame"} aria-hidden={icon ? undefined : "true"}>
    {icon
      ? <span className="connector-glyph" role="img" aria-label={`${source} mark`} style={{
          WebkitMaskImage: `url("${icon}")`, maskImage: `url("${icon}")`,
          WebkitMaskPosition: "center", maskPosition: "center",
          WebkitMaskRepeat: "no-repeat", maskRepeat: "no-repeat",
          WebkitMaskSize: "contain", maskSize: "contain",
        }} />
      : <span className="connector-glyph vector" role="img" aria-label="Bondren mark"><BondrenMark /></span>}
  </span>;
}
