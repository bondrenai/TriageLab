# Agentgateway CEL editor

`src/lib/cel-monaco.ts` and `src/components/ticket-cel-monaco.tsx` adapt
Agentgateway v1.7.0's `ui/src/celMonaco.ts` and
`ui/src/components/MiniMonacoEditor.tsx`, respectively:
https://github.com/agentgateway/agentgateway/tree/v1.7.0/ui/src

These files are licensed under Apache-2.0. The full license is distributed in
`public/licenses/agentgateway.txt` and the built UI's `/licenses/agentgateway.txt`.
Upstream has no root NOTICE file at this version. Modified files identify the
TriageLab changes. The gateway-specific CEL extensions and generated schema are
replaced with TriageLab's ticket schema and standard CEL suggestions.

Monaco Editor, its React wrapper, the loader, and state-local are MIT licensed. Their license texts are
distributed alongside the Agentgateway license in `public/licenses/`, together
with Monaco’s third-party notices and DOMPurify’s Apache-2.0 license.
