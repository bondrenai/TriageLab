import type { TicketSource } from "./types/ticket";

export const conditionFields = {
  tags: { label: "Tag or label", operators: ["has", "lacks"] },
  status: { label: "Status", operators: ["is", "is not"] },
  priority: { label: "Priority", operators: ["is", "is not"] },
  subject: { label: "Title", operators: ["contains", "starts with", "is"] },
  id: { label: "Ticket ID", operators: ["is", "is not"] },
  requester_id: { label: "Requester ID", operators: ["is", "is not"] },
  updated_at: { label: "Updated at", operators: ["is", "starts with"] },
} as const;

type ConditionField = keyof typeof conditionFields;

export interface TicketCondition {
  field: ConditionField;
  operator: string;
  value: string;
}

export type ConditionJoin = "all" | "any";

export function builderFields(source: TicketSource): ConditionField[] {
  return (Object.keys(conditionFields) as ConditionField[])
    .filter(field => field !== "priority" || source === "zendesk");
}

export function renderCondition({ field, operator, value }: TicketCondition): string {
  const literal = field === "id" || field === "requester_id" ? value : JSON.stringify(value);
  switch (operator) {
    case "has": return `${literal} in ticket.tags`;
    case "lacks": return `!(${literal} in ticket.tags)`;
    case "contains": return `ticket.${field}.contains(${literal})`;
    case "starts with": return `ticket.${field}.startsWith(${literal})`;
    case "is not": return `ticket.${field} != ${literal}`;
    default: return `ticket.${field} == ${literal}`;
  }
}

export function renderConditions(rules: TicketCondition[], join: ConditionJoin): string {
  return rules.map(renderCondition).join(join === "all" ? " &&\n" : " ||\n");
}

// Only convert expressions that round-trip through the builder. Other CEL
// stays in the editor as written.
export function readConditions(expression: string, source: TicketSource) {
  const join: ConditionJoin = expression.includes(" ||\n") ? "any" : "all";
  const rules: TicketCondition[] = [];
  if (!expression) return { rules, join };
  for (const line of expression.split(join === "all" ? " &&\n" : " ||\n")) {
    const literal = line.match(/"(?:[^"\\]|\\.)*"|\b[0-9]+$/)?.[0];
    if (!literal) return null;
    let value: string;
    try {
      value = literal.startsWith('"') ? JSON.parse(literal) as string : literal;
    } catch {
      return null;
    }
    const rule = builderFields(source).flatMap(field =>
      conditionFields[field].operators.map(operator => ({ field, operator, value })))
      .find(candidate => renderCondition(candidate) === line);
    if (!rule) return null;
    rules.push(rule);
  }
  return { rules, join };
}
