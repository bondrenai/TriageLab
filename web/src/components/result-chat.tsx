import { useEffect, useRef, useState, type FormEvent } from "react";
import { isChatAccessDenied, useAskResultChat, useResultChat } from "../hooks/use-result-chat";
import { APIError } from "../lib/api-client";
import { fileSize, formatDate } from "../lib/investigations";
import type { InvestigationReport } from "../lib/types";
import type { ResultChat, ResultChatMessage } from "../lib/types/result-chat";
import { EvidenceReferences, type EvidenceSelection } from "./report-evidence";
import { Button, ErrorPlate, Loading, Panel, Textarea } from "./ui";

function AnswerSources({ message, report, onReview, onOpenEvidence }: {
  message: ResultChatMessage;
  report: InvestigationReport;
  onReview: (selection: EvidenceSelection) => void;
  onOpenEvidence: (id: string, round: number) => void;
}) {
  const citations = message.citations ?? [];
  const rounds = [...new Set(citations.map(citation => citation.round))];
  if (!rounds.length) return null;
  return <div className="result-chat-sources">{rounds.map(round => {
    const source = round === (report.round ?? 1) ? report : report.rounds?.find((item, index) => (item.round ?? index + 1) === round);
    if (!source) return <p key={round} className="evidence-absence">The cited evidence from round {round} is unavailable in this report.</p>;
    return <EvidenceReferences key={round} context={{ title: "Answer from the investigation conversation", statement: message.content, ids: citations.filter(citation => citation.round === round).map(citation => citation.process_id) }} report={source} onReview={onReview} onOpenEvidence={id => onOpenEvidence(id, round)} label={`Evidence from round ${round}`} />;
  })}</div>;
}

export function InvestigationChat({ report, canAsk, onReviewEvidence, onOpenEvidence }: {
  report: InvestigationReport;
  canAsk: boolean;
  onReviewEvidence: (selection: EvidenceSelection) => void;
  onOpenEvidence: (id: string, round: number) => void;
}) {
  const query = useResultChat(report.run_id);
  const ask = useAskResultChat(report.run_id);
  const [draft, setDraft] = useState("");
  const [inputError, setInputError] = useState("");
  const [submissionUncertain, setSubmissionUncertain] = useState(false);
  const attempted = useRef<{ message: string; previousUserID?: string } | null>(null);
  const transcript = useRef<HTMLOListElement>(null);
  const followLatest = useRef(true);
  const inFlight = useRef(false);
  const state = query.data;
  const denied = isChatAccessDenied(query.error);
  const completed = !report.active && ["completed", "closed", "error", "interrupted", "timed_out"].includes(report.status);
  const answering = state?.status === "answering";
  const maxBytes = state?.max_message_bytes ?? 4096;
  const messageBytes = new TextEncoder().encode(draft.trim()).length;
  const validationError = inputError || (messageBytes > maxBytes ? `Keep the question under ${fileSize(maxBytes)}.` : "");
  const canSubmit = completed && canAsk && state?.can_ask && !answering && !ask.isPending && !query.isError && !submissionUncertain;

  useEffect(() => {
    if (transcript.current && followLatest.current) transcript.current.scrollTop = transcript.current.scrollHeight;
  }, [state?.messages.length]);

  function resolveSubmission(saved: ResultChat) {
    const attempt = attempted.current;
    const previousIndex = saved.messages.findIndex(message => message.id === attempt?.previousUserID);
    const subsequent = saved.messages.slice(previousIndex + 1);
    if (attempt && subsequent.some(message => message.role === "user" && message.content === attempt.message)) {
      setDraft("");
      attempted.current = null;
    }
  }

  async function refreshConversation() {
    const refreshed = await query.refetch();
    if (refreshed.isSuccess) {
      resolveSubmission(refreshed.data);
      setSubmissionUncertain(false);
      ask.reset();
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSubmit || inFlight.current) return;
    const message = draft.trim();
    if (!message) { setInputError("Write a question about the saved investigation."); return; }
    if (messageBytes > maxBytes) { setInputError(`Keep the question under ${fileSize(maxBytes)}.`); return; }
    setInputError("");
    followLatest.current = true;
    attempted.current = { message, previousUserID: state?.messages.filter(item => item.role === "user").at(-1)?.id };
    inFlight.current = true;
    try {
      await ask.mutateAsync(message);
      setDraft("");
      attempted.current = null;
    } catch (error) {
      if (!(error instanceof APIError) || error.status >= 500) setSubmissionUncertain(true);
    } finally {
      inFlight.current = false;
    }
  }

  const unavailable = !completed
    ? "Questions become available when this investigation has finished. Paused investigations must be resumed or closed first."
    : !canAsk
      ? "You can read this conversation. Asking a question requires the user role."
      : state?.unavailable_reason;

  return <Panel title="Ask about this investigation" aside={<span className="quiet text-meta">Saved conversation</span>}>
    <div className="panel-body result-chat">
      <p className="result-chat-scope">Ask about the saved results, including earlier rounds. Answers are agent interpretations; this conversation does not run new experiments or change the report.</p>
      {query.isPending ? <Loading label="Loading investigation conversation…" /> : denied ? <ErrorPlate title="Conversation unavailable" error="You no longer have access to this investigation conversation." /> : <>
        {query.isError && <ErrorPlate title={state ? "Conversation could not refresh" : "Conversation could not load"} error={query.error} action={<Button disabled={query.isFetching} onClick={() => void refreshConversation()}>Refresh conversation</Button>} />}
        {state && <>
          {state.messages.length > 0 ? <ol ref={transcript} className="result-chat-transcript" tabIndex={0} aria-label="Saved investigation conversation" aria-live="polite" aria-relevant="additions" onScroll={event => { const element = event.currentTarget; followLatest.current = element.scrollHeight - element.scrollTop - element.clientHeight < 40; }}>{state.messages.map(message => <li key={message.id} className={`result-chat-message result-chat-${message.role}`}>
            <div className="result-chat-message-heading"><h3>{message.role === "user" ? "Question" : "Agent answer"}</h3><time dateTime={message.created_at}>{formatDate(message.created_at)}</time></div>
            <p className="result-chat-content">{message.content}</p>
            {message.role === "assistant" && <AnswerSources message={message} report={report} onReview={onReviewEvidence} onOpenEvidence={onOpenEvidence} />}
          </li>)}</ol> : <p className="result-chat-empty">No questions yet. Ask why an experiment matters, compare hypotheses, or explore what remains unproven.</p>}
          {answering && <p className="result-chat-status" role="status">Reviewing the saved investigation. The answer will appear here.</p>}
          {state.status === "error" && <ErrorPlate title="The answer could not complete" error={state.error || "The saved question remains available. You can ask another question when the service is ready."} />}
          {state.status === "interrupted" && <div className="notice result-chat-interrupted" role="status"><strong>The answer was interrupted.</strong><p>{state.error || "The question is saved. No answer was recorded, and the request was not replayed."}</p></div>}
          {unavailable && <p className="result-chat-unavailable" role="status">{unavailable}</p>}
          {ask.isError && !submissionUncertain && <ErrorPlate title="Question could not be submitted" error={ask.error} />}
          {submissionUncertain && <div className="notice result-chat-interrupted" role="status"><strong>Question submission is unconfirmed.</strong><p>Refresh the saved conversation and check whether the question was accepted before sending again.</p><Button disabled={query.isFetching} onClick={() => void refreshConversation()}>Refresh saved conversation</Button></div>}
          {completed && canAsk && (state.can_ask || answering) && <form className="result-chat-form" onSubmit={submit}>
            <label className="field-label" htmlFor="result-chat-question">Question about the results</label>
            <Textarea id="result-chat-question" value={draft} onChange={event => { setDraft(event.target.value); setInputError(""); }} rows={3} required disabled={ask.isPending || answering || submissionUncertain || query.isError} aria-describedby={`result-chat-help${validationError ? " result-chat-error" : ""}`} aria-invalid={Boolean(validationError)} placeholder="Which experiment most strongly supports the leading hypothesis?" />
            <div className="result-chat-compose-footer"><p className="field-help" id="result-chat-help">Up to {fileSize(maxBytes)}. Questions use your configured model; provider charges may apply.</p><Button type="submit" disabled={!canSubmit || !draft.trim() || messageBytes > maxBytes}>{ask.isPending ? "Sending question…" : "Ask question"}</Button></div>
            {validationError && <p className="field-error" id="result-chat-error" role="alert">{validationError}</p>}
          </form>}
        </>}
      </>}
    </div>
  </Panel>;
}
