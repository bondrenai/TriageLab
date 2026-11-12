import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { ArrowRight, KeyRound } from "lucide-react";
import { useSession, useSignIn } from "../hooks/use-auth";
import { BrandMark } from "../components/brand-mark";
import { Button } from "../components/ui";

export function LoginPage() {
  const session = useSession();
  const signIn = useSignIn();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(params.get("signin") === "failed" ? "Single sign-in did not complete. Try again." : null);

  useEffect(() => {
    if (session.data?.authenticated) navigate("/", { replace: true });
  }, [session.data?.authenticated, navigate]);

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    setError(null);
    signIn.mutate({ username: username.trim(), password }, {
      onSuccess: () => navigate("/", { replace: true }),
      onError: err => setError(err instanceof Error ? err.message : "Sign in failed."),
    });
  };

  return <div className="login-shell">
    <main className="login-plate plate">
      <div className="login-brand"><span className="brand-mark"><BrandMark /></span><span className="login-title">TriageLab</span></div>
      <p className="login-copy">Sign in to the investigation workspace.</p>
      {error && <div className="login-error" role="alert">{error}</div>}
      <form onSubmit={submit} className="login-form">
        <label className="field-label" htmlFor="login-username">Username</label>
        <input
          id="login-username" className="field" autoComplete="username" autoCapitalize="none"
          spellCheck={false} required value={username} onChange={e => setUsername(e.target.value)}
          autoFocus
        />
        <label className="field-label" htmlFor="login-password">Password</label>
        <input
          id="login-password" className="field" type="password" autoComplete="current-password"
          required value={password} onChange={e => setPassword(e.target.value)}
        />
        <Button type="submit" variant="primary" disabled={signIn.isPending}>
          <KeyRound size={15} aria-hidden="true" />{signIn.isPending ? "Signing in…" : "Sign in"}
        </Button>
      </form>
      {session.data?.oidc_enabled && (
        <>
          <div className="login-divider"><span>or</span></div>
          <a className="button button-secondary login-sso" href="/api/auth/oidc/start">
            Continue with single sign-on<ArrowRight size={15} aria-hidden="true" />
          </a>
        </>
      )}
      <div className="login-footer"><span>by </span><span className="wordmark">BONDREN</span></div>
    </main>
  </div>;
}
