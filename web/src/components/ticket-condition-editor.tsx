import { lazy, Suspense, useId, useRef, useState } from "react";
import { useTicketCondition } from "../hooks/use-ticket-condition";
import { builderFields, conditionFields, readConditions, renderConditions, type TicketCondition } from "../lib/ticket-condition";
import type { TicketSource } from "../lib/types/ticket";
import { Field } from "./resource-editor";
import { Button } from "./ui";

import type { TicketCELHandle } from "./ticket-cel-monaco";

const TicketCELMonaco = lazy(() => import("./ticket-cel-monaco"));

const initialCondition: TicketCondition = { field: "tags", operator: "has", value: "" };
const tokenPattern = /(\/\/[^\n]*)|("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|(\b(?:true|false|null|in)\b)|\b(\d+(?:\.\d+)?)\b|(\bticket\b)/g;

function HighlightedCEL({ value }: { value: string }) {
  let end = 0;
  const tokens = Array.from(value.matchAll(tokenPattern), match => {
    const before = value.slice(end, match.index);
    end = match.index + match[0].length;
    const style = match[1] ? "comment" : match[2] ? "string" : match[3] || match[4] ? "literal" : "variable";
    return <span key={match.index}>{before}<span className={`cel-${style}`}>{match[0]}</span></span>;
  });
  return <>{tokens}{value.slice(end)}{"\n"}</>;
}

export function TicketConditionEditor({ connection, source, label, emptyHint, hint, value, onChange }: {
  connection: string;
  label: string;
  emptyHint: string;
  hint?: string;
  source: TicketSource;
  value: string;
  onChange: (value: string) => void;
}) {
  const parsed = readConditions(value, source);
  const [mode, setMode] = useState<"builder" | "cel">(() => parsed ? "builder" : "cel");
  const [draft, setDraft] = useState(initialCondition);
  const [editing, setEditing] = useState<number>();
  const [emptyJoin, setEmptyJoin] = useState<"all" | "any">("all");
  const diagnostics = useTicketCondition(connection, value);
  const raw = useRef<TicketCELHandle>(null);
  const lintID = useId();
  const rules = parsed?.rules ?? [];
  const join = parsed && rules.length > 1 ? parsed.join : emptyJoin;
  const numeric = draft.field === "id" || draft.field === "requester_id";
  const validDraft = numeric ? /^[1-9][0-9]{0,18}$/.test(draft.value) && BigInt(draft.value) <= 9223372036854775807n : Boolean(draft.value.trim());
  const next = [...rules];
  next[editing ?? next.length] = draft;
  const candidate = renderConditions(next, join);
  const canAdd = validDraft && new TextEncoder().encode(candidate).length <= 4096;

  function applyCondition() {
    onChange(candidate);
    setDraft(initialCondition);
    setEditing(undefined);
  }


  return <fieldset className="agent-fields agent-inset policy-editor">
    <legend className="field-label">{label}</legend>
    {hint && <p className="field-help">{hint}</p>}
    <div className="button-row">
      <Button type="button" aria-pressed={mode === "builder"} disabled={!parsed}
        onClick={() => setMode("builder")}>Condition builder</Button>
      <Button type="button" aria-pressed={mode === "cel"}
        onClick={() => setMode("cel")}>Raw CEL</Button>
    </div>
    {mode === "builder" && parsed ? <>
      <Field label="Match">
        <select className="field" value={join} onChange={event => {
          const next = event.target.value === "any" ? "any" : "all";
          setEmptyJoin(next);
          onChange(renderConditions(rules, next));
        }}>
          <option value="all">All conditions</option>
          <option value="any">Any condition</option>
        </select>
      </Field>
      {rules.length > 0 && <ul className="policy-conditions">
        {rules.map((rule, index) => <li key={index}>
          <span>{conditionFields[rule.field].label} {rule.operator} <code>{rule.value}</code></span>
          <div className="button-row">
            <Button type="button" aria-label={`Edit condition ${index + 1}`}
              onClick={() => { setDraft(rule); setEditing(index); }}>Edit</Button>
            <Button type="button" aria-label={`Remove condition ${index + 1}`} onClick={() => {
              onChange(renderConditions(rules.filter((_, i) => index !== i), join));
              setEditing(undefined);
              setDraft(initialCondition);
            }}>Remove</Button>
          </div>
        </li>)}
      </ul>}
      <div className="policy-condition-inputs">
        <Field label="Ticket property">
          <select className="field" value={draft.field} onChange={event => {
            const field = event.target.value as TicketCondition["field"];
            setDraft({ field, operator: conditionFields[field].operators[0], value: "" });
          }}>
            {builderFields(source).map(field => <option key={field} value={field}>{conditionFields[field].label}</option>)}
          </select>
        </Field>
        <Field label="Operator">
          <select className="field" value={draft.operator}
            onChange={event => setDraft({ ...draft, operator: event.target.value })}>
            {conditionFields[draft.field].operators.map(operator => <option key={operator}>{operator}</option>)}
          </select>
        </Field>
        <Field label="Value">
          <input className="field" maxLength={512} inputMode={numeric ? "numeric" : "text"}
            value={draft.value} onChange={event => setDraft({ ...draft, value: event.target.value })}
            onKeyDown={event => { if (event.key === "Enter") { event.preventDefault(); if (canAdd) applyCondition(); } }} />
        </Field>
        <Button type="button" disabled={!canAdd} onClick={applyCondition}>
          {editing === undefined ? "Add condition" : "Update condition"}
        </Button>
      </div>
      {value && <details><summary>Generated CEL</summary><pre className="policy-cel-preview"><HighlightedCEL value={value} /></pre></details>}
    </> : <>
      {!parsed && <p className="field-help">This expression uses CEL beyond the condition builder.</p>}
      <details>
        <summary>Ticket properties</summary>
        <p className="field-help"><code>ticket.source</code>, <code>ticket.tags</code>, <code>ticket.status</code>,
          <code> ticket.subject</code>, <code>ticket.id</code>, <code>ticket.requester_id</code>, <code>ticket.updated_at</code>
          {source === "zendesk" && <>, <code>ticket.priority</code>, <code>ticket.custom_fields</code></>}.
        </p>
      </details>
      <p className="field-help">Type <code>ticket.</code> for properties, or press Ctrl+Space for suggestions. Tab moves to the next control.</p>
      <Suspense fallback={<p className="field-help">Loading CEL editor…</p>}>
        <TicketCELMonaco source={source} label={label} value={value} onChange={onChange}
          diagnostics={diagnostics} editorRef={raw} />
      </Suspense>
    </>}
    <div id={lintID} aria-live="polite" className="field-help">
      {!value ? emptyHint
        : !diagnostics ? "Checking CEL…"
        : diagnostics.length === 0 ? "Valid CEL. Preview a ticket to check whether it matches."
        : <ul className="policy-diagnostics">{diagnostics.map(({ message, line, column }, index) =>
          <li key={index}>{line && line > 0 && mode === "cel"
            ? <button type="button" onClick={() => raw.current?.jumpTo(line, column ?? 1)}>Line {line}, column {column ?? 1}</button>
            : null} {message}</li>)}</ul>}
    </div>
  </fieldset>;
}
