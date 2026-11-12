// The TriageLab mark: a flask under inspection inside drafting corner marks.
// Ink follows currentColor and the evidence dot follows the ultramarine token,
// so the mark adapts to the paper surface and the midnight navigation frame.
// Static copies live in web/public/favicon.svg, docs-site/public/favicon.svg,
// and docs-site/src/assets/triagelab-mark-{light,dark}.svg.
export function BrandMark() {
  return <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" aria-hidden="true" focusable="false">
    <path d="M4 13V4H13M35 4H44V13M44 35V44H35M13 44H4V35" strokeWidth="2.5" strokeLinecap="square" />
    <path d="M17.5 10.5H30.5M20.5 10.5V19L11.5 37.5H36.5L27.5 19V10.5M15.5 29.5H32.5" strokeWidth="2.75" strokeLinecap="round" strokeLinejoin="round" />
    <circle cx="24" cy="33.5" r="2.4" fill="var(--color-blue-600)" stroke="none" />
  </svg>;
}
