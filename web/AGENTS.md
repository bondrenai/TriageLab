# TriageLab frontend

Use React 19, TypeScript, Vite, Tailwind 4, and TanStack Query. Keep dependencies
small. API transport belongs in `lib/api-client.ts`, domain operations in
`lib/investigations.ts`, shared types in `lib/types/`, and query lifecycle in
`hooks/`. Components do not fetch directly. Keep query keys in one module.

The UI follows Bondren's ink-forward drafting system. `src/styles.css` is the
token source: cool drawing paper, graphite ink, a midnight navigation frame,
and ultramarine interaction. Use IBM Plex Sans for UI and titles, IBM Plex Mono
for identifiers, commands and data, and Marcellus only for the BONDREN wordmark.
Color through the named gray/red/amber/green/blue scales. Functional type is at
least 11px. Plates are tone and shadow; controls have borders. Title blocks
close with a two-pixel cut rule. Status stamps are square-cornered impressions,
not identity pills. One ink-filled primary action per page.

Preserve loading, failure, empty, active and interrupted states separately.
Model analysis is a hypothesis; collected process output is evidence. The saved
example is explicitly labelled and never enters live history. Render imported
content and model output as text, or through `components/markdown.tsx`, which
builds React elements from the safe subset parsed in `lib/markdown.ts`: never
HTML, images or live links. Command output and exact-text previews of what
TriageLab will post stay literal. Downloads refer to server-saved reports.

Support mobile widths, visible focus, labelled inputs, reduced motion and native
keyboard interactions. All fonts and assets are served locally. No analytics or
third-party requests are needed for the UI.

Honor the user's remote-compute requirement: edit locally, install dependencies
and run preview/build/check commands on the temporary VM. `make dev-ui` starts
the Vite preview; `make build-ui` prepares assets for the Go server. Do not run
builds or tests on the resource-constrained local machine.
