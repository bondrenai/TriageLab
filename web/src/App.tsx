import { useEffect } from "react";
import { Link, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { ArrowUpRight, FlaskConical, FolderOpen, Bot, Github, Inbox } from "lucide-react";
import { AgentsPage } from "./pages/AgentsPage";
import { InvestigationsPage } from "./pages/InvestigationsPage";
import { NewInvestigationPage } from "./pages/NewInvestigationPage";
import { TicketsPage } from "./pages/TicketsPage";
import { ReportPage } from "./pages/ReportPage";
import { PageTitle, ButtonLink } from "./components/ui";

export function App() {
  const location = useLocation();
  useEffect(() => {
    document.getElementById("main-content")?.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }, [location.pathname]);

  return <div className="app-shell">
    <a className="skip-link" href="#main-content">Skip to content</a>
    <aside className="sidebar ink-frame">
      <Link className="brand" to="/" aria-label="TriageLab home"><span className="brand-mark"><FlaskConical size={23} strokeWidth={1.5} /></span><span>TriageLab</span></Link>
      <div className="workspace-label engraved">Workbench</div>
      <nav className="main-nav" aria-label="Main navigation">
        <NavLink to="/" end><FolderOpen size={17} aria-hidden="true" />Investigations</NavLink>
        <NavLink to="/agents"><Bot size={17} aria-hidden="true" />Agents</NavLink>
        <NavLink to="/tickets"><Inbox size={17} aria-hidden="true" />Ticket cases</NavLink>
      </nav>
      <div className="sidebar-footer">
        <a className="source-link" href="https://github.com/bondrenai/TriageLab" target="_blank" rel="noreferrer"><Github size={16} aria-hidden="true" />Source & documentation<ArrowUpRight size={13} aria-hidden="true" /></a>
        <div className="company-signature"><span>by</span><span className="wordmark">BONDREN</span></div>
        <p>A free, open-source investigation tool.</p>
      </div>
    </aside>
    <div className="workspace">
      <div className="workspace-bar"><span>Technical issue reproduction</span><span className="workspace-mode"><span className="status-dot" />Local workspace</span></div>
      <main id="main-content" tabIndex={-1}>
        <Routes>
          <Route path="/" element={<InvestigationsPage />} />
          <Route path="/new" element={<NewInvestigationPage />} />
          <Route path="/agents/*" element={<AgentsPage />} />
          <Route path="/tickets" element={<TicketsPage />} />
          <Route path="/example" element={<ReportPage example />} />
          <Route path="/runs/:id" element={<ReportPage />} />
          <Route path="*" element={<><PageTitle title="Page not found" description="This workspace page does not exist." /><ButtonLink to="/">Back to investigations</ButtonLink></>} />
        </Routes>
      </main>
      <footer className="workspace-footer"><span>TriageLab<span className="mobile-signature"> by <span className="wordmark">BONDREN</span></span></span><span>From a reported issue to inspectable evidence.</span></footer>
    </div>
  </div>;
}
