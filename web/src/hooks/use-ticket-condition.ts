import { useEffect, useState } from "react";
import { validateTicketCondition, type ConditionDiagnostic } from "../lib/tickets";

export function useTicketCondition(connection: string, expression: string) {
  const [result, setResult] = useState<{ expression: string; diagnostics: ConditionDiagnostic[] }>();
  useEffect(() => {
    if (!expression || !connection) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      void validateTicketCondition(connection, expression, controller.signal).then(
        diagnostics => {
          if (!controller.signal.aborted) setResult({ expression, diagnostics });
        },
        () => {
          if (!controller.signal.aborted) setResult({
            expression, diagnostics: [{ message: "Validation is unavailable. Try again or save to validate the condition." }],
          });
        },
      );
    }, 350);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [connection, expression]);
  return result?.expression === expression ? result.diagnostics : undefined;
}
