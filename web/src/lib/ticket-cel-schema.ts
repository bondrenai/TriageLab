import type { TicketSource } from "./types/ticket";

// Mirrors the normalized attributes in internal/ticketflow/cel.go. Connector
// custom-field IDs are dynamic map keys, not invented schema properties.
export function ticketCelSchema(source: TicketSource) {
  return {
    properties: {
      ticket: {
        type: "object",
        description: "Normalized attributes of the ticket being evaluated.",
        properties: {
          source: { type: "string", description: "Connector: zendesk, github, linear, bondren, or jira." },
          id: { type: "integer", description: "Numeric ticket ID used by the connector." },
          requester_id: { type: "integer", description: "Numeric requester ID, or 0 when unavailable." },
          subject: { type: "string", description: "Ticket subject or issue title." },
          status: { type: "string", description: "Normalized ticket status, such as open or pending." },
          tags: { type: "array", description: 'Ticket tags or labels. Example: "bug" in ticket.tags.' },
          updated_at: { type: "string", description: "Source update timestamp as a string. Use timestamp(ticket.updated_at) for date comparisons." },
          ...(source === "zendesk" ? {
            priority: { type: "string", description: "Zendesk priority: low, normal, high, urgent, or empty." },
            custom_fields: { type: "object", description: 'Zendesk fields keyed by string IDs. Guard missing fields: "123" in ticket.custom_fields && ticket.custom_fields["123"] == "value".' },
          } : {}),
        },
      },
    },
  };
}
