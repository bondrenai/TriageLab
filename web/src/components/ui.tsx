import { useId, type ReactNode, type ButtonHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { AlertCircle } from "lucide-react";
import { Link } from "react-router-dom";

export function Button({ variant = "secondary", className = "", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "ghost" }) {
  return <button className={`button button-${variant} ${className}`} {...props} />;
}

export function ButtonLink({ to, children, variant = "secondary" }: { to: string; children: ReactNode; variant?: "primary" | "secondary" }) {
  return <Link className={`button button-${variant}`} to={to}>{children}</Link>;
}

export function PageTitle({ title, description, action, back, entity = false }: { title: string; description?: ReactNode; action?: ReactNode; back?: string; entity?: boolean }) {
  return <header className="page-title rule-cut">
    {back && <Link className="back-link" to="/">{back}</Link>}
    <div className="title-line"><div><h1 className={entity ? "entity-title" : "sheet-title"}>{title}</h1>{description && <div className="page-description">{description}</div>}</div>{action && <div className="title-action">{action}</div>}</div>
  </header>;
}

export function Panel({ title, children, className = "", aside }: { title: string; children: ReactNode; className?: string; aside?: ReactNode }) {
  const id = useId();
  return <section className={`plate panel ${className}`} aria-labelledby={id}><header className="panel-header"><h2 id={id}>{title}</h2>{aside}</header>{children}</section>;
}

export type StampHue = "gray" | "blue" | "green" | "amber" | "red";
export function Stamp({ children, hue = "gray" }: { children: ReactNode; hue?: StampHue }) {
  return <span className={`stamp stamp-${hue}`}>{children}</span>;
}

export function StatusStamp({ status, outcome, active }: { status: string; outcome?: string; active?: boolean }) {
  if (status === "paused") return <Stamp hue="amber">Paused</Stamp>;
  if (status === "pausing" || status === "resuming" || status === "closing") return <Stamp hue="blue">{status === "pausing" ? "Pausing" : status === "resuming" ? "Resuming" : "Closing"}</Stamp>;
  if (status === "closed") return <Stamp>Closed</Stamp>;
  if (active) return <Stamp hue="blue">Investigating</Stamp>;
  if (status === "completed") return <Stamp hue={outcome === "reproduced" ? "green" : "amber"}>{outcome === "reproduced" ? "Reproduced" : outcome === "not_reproduced" ? "Not reproduced" : "Inconclusive"}</Stamp>;
  if (status === "running" || status === "queued") return <Stamp hue="amber">Interrupted</Stamp>;
  return <Stamp hue={status === "error" ? "red" : "amber"}>{status.replaceAll("_", " ") || "Unknown"}</Stamp>;
}

export function ErrorPlate({ title = "Something went wrong", error, action }: { title?: string; error: unknown; action?: ReactNode }) {
  return <div className="error-plate" role="alert"><AlertCircle size={18} aria-hidden="true" /><div><strong>{title}</strong><p>{error instanceof Error ? error.message : String(error)}</p>{action}</div></div>;
}

export function Loading({ label = "Loading investigation…" }: { label?: string }) {
  return <div className="plate loading" role="status" aria-label={label}><span className="sr-only">{label}</span><div className="skeleton" /><div className="skeleton" /><div className="skeleton" /></div>;
}

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className="field textarea" {...props} />;
}
