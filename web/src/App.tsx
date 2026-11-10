import { useEffect } from "react";
import { Link, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { ArrowUpRight, FlaskConical, FolderOpen, Bot, Github, Inbox, LogOut, Users } from "lucide-react";
import { AgentsPage } from "./pages/AgentsPage";
import { InvestigationsPage } from "./pages/InvestigationsPage";
import { NewInvestigationPage } from "./pages/NewInvestigationPage";
import { TicketsPage } from "./pages/TicketsPage";
import { ReportPage } from "./pages/ReportPage";
import { LoginPage } from "./pages/LoginPage";
import { UsersPage } from "./pages/UsersPage";
import { PageTitle, ButtonLink, Stamp } from "./components/ui";
import { useSession, useSignOut } from "./hooks/use-auth";
import { roleAtLeast } from "./lib/auth";
import { useQueryClient } from "@tanstack/react-query";

function IdentityFooter({ username, role }: { username?: string; role?: string }) {
  const signOut = useSignOut();
  return <div className="identity-block">
    <div className="identity-row">
      <span className="mono identity-name">{username ?? "signed in"}</span>
      {role && <Stamp hue={role === "admin" ? "blue" : "gray"}>{role}</Stamp>}
    </div>
    <button className="sign-out" onClick={() => void signOut.mutate()} disabled={signOut.isPending}>
      <LogOut size={14} aria-hidden="true" />{signOut.isPending ? "Signing out…" : "Sign out"}
    </button>
  </div>;
}

export function App() {
  const location = useLocation();
  const session = useSession();
  const queryClient = useQueryClient();
  useEffect(() => {
    document.getElementById("main-content")?.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }, [location.pathname]);

  // A 401 from any API call (expired session, restart) returns to sign-in.
  useEffect(() => {
    const onUnauthorized = () => void queryClient.invalidateQueries({ queryKey: ["session"] });
    window.addEventListener("triagelab:unauthorized", onUnauthorized);
    return () => window.removeEventListener("triagelab:unauthorized", onUnauthorized);
  }, [queryClient]);

  if (session.isLoading) {
    return <div className="login-shell"><main className="login-plate plate" aria-busy="true"><p className="login-copy">Loading workspace…</p></main></div>;
  }
  if (!session.data?.authenticated) {
    return <LoginPage />;
  }

  const isAdmin = roleAtLeast(session.data.role, "admin");

  return <div className="app-shell">
    <a className="skip-link" href="#main-content">Skip to content</a>
    <aside className="sidebar ink-frame">
      <Link className="brand" to="/" aria-label="TriageLab home"><span className="brand-mark"><FlaskConical size={23} strokeWidth={1.5} /></span><span>TriageLab</span></Link>
      <div className="workspace-label engraved">Workbench</div>
      <nav className="main-nav" aria-label="Main navigation">
        <NavLink to="/" end><FolderOpen size={17} aria-hidden="true" />Investigations</NavLink>
        <NavLink to="/agents"><Bot size={17} aria-hidden="true" />Agents</NavLink>
        <NavLink to="/tickets"><Inbox size={17} aria-hidden="true" />Ticket cases</NavLink>
        {isAdmin && <NavLink to="/accounts"><Users size={17} aria-hidden="true" />Accounts</NavLink>}
      </nav>
      <div className="sidebar-footer">
        <IdentityFooter username={session.data.username} role={session.data.role} />
        <a className="source-link" href="https://github.com/bondrenai/TriageLab" target="_blank" rel="noreferrer"><Github size={16} aria-hidden="true" />Source & documentation<ArrowUpRight size={13} aria-hidden="true" /></a>
        <div className="company-signature"><span>by</span><span className="wordmark">BONDREN</span></div>
        <p>A free, open-source investigation tool.</p>
      </div>
    </aside>
    <div className="workspace">
      <div className="workspace-bar"><span>Technical issue reproduction</span><span className="workspace-mode"><span className="status-dot" />{session.data.role === "viewer" ? "Reviewing as viewer" : "Local workspace"}</span></div>
      <main id="main-content" tabIndex={-1}>
        <Routes>
          <Route path="/" element={<InvestigationsPage />} />
          <Route path="/login" element={<LoginPage />} />
          <Route path="/new" element={<NewInvestigationPage />} />
          <Route path="/agents/*" element={<AgentsPage />} />
          <Route path="/tickets" element={<TicketsPage />} />
          <Route path="/accounts" element={<UsersPage />} />
          <Route path="/example" element={<ReportPage example />} />
          <Route path="/runs/:id" element={<ReportPage />} />
          <Route path="*" element={<><PageTitle title="Page not found" description="This workspace page does not exist." /><ButtonLink to="/">Back to investigations</ButtonLink></>} />
        </Routes>
      </main>
      <footer className="workspace-footer"><span>TriageLab<span className="mobile-signature"> by <span className="wordmark">BONDREN</span></span></span><span>From a reported issue to inspectable evidence.</span></footer>
    </div>
  </div>;
}
