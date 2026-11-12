// Adapted from agentgateway v1.7.0 ui/src/components/MiniMonacoEditor.tsx
// (Apache-2.0). Modified by TriageLab: local loading, ticket completions,
// server markers, model disposal, accessibility, and workspace styling.
// See web/THIRD_PARTY_NOTICES.md and public/licenses/agentgateway.txt.
import { useEffect, useRef, type RefObject } from "react";
import Editor, { loader, type OnMount } from "@monaco-editor/react";
import * as monaco from "monaco-editor/editor";
import "monaco-editor/features/codeEditor/register";
import "monaco-editor/features/codicon/register";
import "monaco-editor/features/tokenization/register";
// The 0.57 feature entry registers inline suggestions only. Include the
// suggestion widget/controller for property completion and Ctrl+Space.
import "monaco-editor/features/suggest/register";
import "monaco-editor/editor/contrib/suggest/browser/suggestController";
import "monaco-editor/features/snippet/register";
import "monaco-editor/features/hover/register";
import "monaco-editor/features/gotoError/register";
import "monaco-editor/features/bracketMatching/register";
import "monaco-editor/features/toggleTabFocusMode/register";
import "monaco-editor/features/clipboard/register";
import "monaco-editor/features/wordOperations/register";
import "monaco-editor/features/linesOperations/register";
import "monaco-editor/features/contextmenu/register";
import "monaco-editor/features/find/register";
import EditorWorker from "monaco-editor/editor/editor.worker?worker";
import { celEditorOptions, celLanguage, configureCelMonaco, setTicketModelSource } from "../lib/cel-monaco";
import type { ConditionDiagnostic } from "../lib/tickets";
import type { TicketSource } from "../lib/types/ticket";

// Use Vite's same-origin worker and bundled Monaco, never the loader's CDN.
self.MonacoEnvironment = { getWorker: () => new EditorWorker() };
loader.config({ monaco });
configureCelMonaco(monaco);
monaco.editor.defineTheme("triagelab", {
  base: "vs", inherit: true, rules: [],
  colors: { "editor.background": "#f8fafc", "editor.focusedStackFrameHighlightBackground": "#dbeafe" },
});

export interface TicketCELHandle { jumpTo: (line: number, column: number) => void }

// CEL reports Unicode code points; Monaco positions count UTF-16 code units.
function modelPosition(model: monaco.editor.ITextModel, line: number, column: number) {
  const lineNumber = Math.max(1, Math.min(line, model.getLineCount()));
  const content = model.getLineContent(lineNumber);
  return new monaco.Position(lineNumber, Array.from(content).slice(0, Math.max(0, column - 1)).join("").length + 1);
}

export default function TicketCELMonaco({ source, value, label, diagnostics, onChange, editorRef }: {
  source: TicketSource;
  value: string;
  label: string;
  diagnostics: ConditionDiagnostic[] | undefined;
  onChange: (value: string) => void;
  editorRef: RefObject<TicketCELHandle | null>;
}) {
  const editor = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const latest = useRef({ source, diagnostics });
  latest.current = { source, diagnostics };

  function updateMarkers() {
    const model = editor.current?.getModel();
    if (!model) return;
    setTicketModelSource(model, latest.current.source);
    monaco.editor.setModelMarkers(model, "triagelab-cel", (latest.current.diagnostics ?? []).map(diagnostic => {
      const start = modelPosition(model, diagnostic.line ?? 1, diagnostic.column ?? 1);
      const end = modelPosition(model, diagnostic.line ?? 1, (diagnostic.column ?? 1) + 1);
      return {
        severity: monaco.MarkerSeverity.Error, message: diagnostic.message,
        startLineNumber: start.lineNumber, startColumn: start.column,
        endLineNumber: end.lineNumber, endColumn: end.column,
      };
    }));
  }

  useEffect(updateMarkers, [source, value, diagnostics]);
  useEffect(() => () => { editorRef.current = null; editor.current = null; }, [editorRef]);

  const onMount: OnMount = instance => {
    editor.current = instance;
    editorRef.current = {
      jumpTo(line, column) {
        const model = instance.getModel();
        if (!model) return;
        const position = modelPosition(model, line, column);
        instance.setPosition(position);
        instance.revealPositionInCenterIfOutsideViewport(position);
        instance.focus();
      },
    };
    updateMarkers();
  };

  return <div className="policy-monaco">
    <Editor language={celLanguage} theme="triagelab" value={value} onMount={onMount}
      onChange={next => onChange(next ?? "")} loading={<p className="field-help">Loading CEL editor…</p>}
      options={{
        ...celEditorOptions, ariaLabel: `${label}: CEL expression`,
        automaticLayout: true, fontFamily: "IBM Plex Mono, monospace", fontSize: 13,
        lineNumbers: "on", lineNumbersMinChars: 2, glyphMargin: false, folding: false,
        padding: { top: 10, bottom: 10 }, lineDecorationsWidth: 8, tabSize: 2,
        wordWrap: "on", fixedOverflowWidgets: true, tabFocusMode: true,
        scrollbar: { alwaysConsumeMouseWheel: false },
      }} />
  </div>;
}
