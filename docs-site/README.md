# TriageLab documentation site

Astro + Starlight site for TriageLab's public documentation: start guides,
workflow guides, technical reference, articles, and a blog.

## Design

The site looks like the tool it documents. `src/styles/tokens.css` holds the
palette: the workspace's cool paper, graphite ink, and ultramarine accent
(`web/src/styles.css`), calibrated lighter for long reading, with the
workspace's midnight ramp as dark mode. `src/styles/global.css` lays those
tokens over Starlight and defines the shared `tl-*` pieces.

The site's own voice sits on top of that shared system:

- **Type.** IBM Plex Serif for display (page titles, section heads), set in
  sentence case with italic emphasis. Plex Sans for body, Plex Mono for
  labels, numerals, and code. Fonts are self-hosted.
- **Marks from the logo.** A label opens with the evidence dot
  (`.tl-eyebrow`); a figure under examination sits inside the four corner
  brackets (`.tl-brackets`).
- **Solid and dashed.** Solid means observed or settled; dashed means a
  hypothesis or not yet. The hero's recorded run, the fit cards, and the
  stamps (`.tl-stamp`) all follow it.
- **Rules.** Title blocks, section heads, and table heads close with a 2px
  cut rule. The primary button is ink; ultramarine marks links and focus and
  is never a fill.

`src/components/Hero.astro` and `PageTitle.astro` override Starlight's
components. The hero's recorded run (`EvidenceRecord.astro`) quotes
`start/example.md`, and the closing command (`LandingClose.astro`) quotes
step 5 of `start/quickstart.md`; change them together.

## Keep the technical bits current

Three reference pages are **generated**, never hand-edited:

| Page | Generated from |
| --- | --- |
| `src/content/docs/reference/cli.md` | the real flag registrations in `cmd/triagelab` |
| `src/content/docs/configuration.md` | the `investigation.Config` struct via reflection |
| `src/content/docs/configuration-examples.md` | the committed `*.example.json` files at the repository root |

`triagelab docs --out docs-site/src/content/docs/reference` (or
`make docs-generate`) rewrites them. A golden-file test in
`cmd/triagelab/docs_test.go` runs under plain `make check` (`go test ./...`)
and fails when the committed pages drift from the source, so changing a flag
or config field without regenerating breaks CI. `make docs-check` runs the
same check from the CLI.

Internal links are validated at build time by `starlight-links-validator`.
Because the site publishes under the `/TriageLab/` base path (GitHub Pages
project site), every internal link in content must be written **with the
`/TriageLab` prefix** (`/TriageLab/reference/cli/`); the build fails
otherwise. External links are not validated.

## Commands

```sh
make install-docs   # npm ci
make dev-docs       # dev server on 127.0.0.1:4332
make build-docs     # production build (includes link validation)
make docs-generate  # regenerate the reference pages from the Go source
make docs-check     # verify committed generated pages match the source
```

Run `make docs-generate` from the repository root so the generator can read
the example configuration files.

## Publishing

Pushes to `main` trigger `.github/workflows/docs.yml`, which builds this
directory and deploys `dist/` to GitHub Pages at
<https://bondrenai.github.io/TriageLab/>. One-time repository setup:
Settings → Pages → Source → **GitHub Actions**. The Astro `site`/`base`
values in `astro.config.mjs` must stay in sync with the deployment URL.

Blog posts live in `src/content/docs/blog/` (any Markdown file with `title`
and `date` frontmatter; `author`, `tags`, and `excerpt` are optional).
Articles are ordinary docs pages under `src/content/docs/articles/`.
