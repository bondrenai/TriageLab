import { Button, Panel, Stamp, type StampHue } from "./ui";
import type { useDeploymentStatus } from "../hooks/use-agents";
import type { Deployment, ManifestDefinitions } from "../lib/types/agent";

const stateLabels: Record<Deployment["state"], [string, StampHue]> = {
  ready: ["Ready", "green"],
  pending: ["Reconciling", "blue"],
  attention: ["Needs attention", "amber"],
  deleting: ["Deleting", "gray"],
};

function reasonLabel(reason: string) {
  if (reason === "ActorTemplatePending") return "Preparing the agent runtime";
  if (reason === "ActorTemplateRetrying") return "Retrying agent runtime preparation";
  if (reason === "DiscoveryDisabled") return "Automatic tool discovery is disabled for this server";
  return reason.replace(/([a-z0-9])([A-Z])/g, "$1 $2");
}

export function DeploymentPanel({ status, resource, previewed, onEdit, editing }: {
  status: ReturnType<typeof useDeploymentStatus>;
  resource: keyof ManifestDefinitions;
  previewed: string[];
  onEdit?: (name: string) => void;
  editing?: boolean;
}) {
  const deployed = status.data?.resources ?? [];
  const missing = previewed.filter(name => !deployed.some(item => item.name === name));

  return <Panel title="Deployment status" aside={status.data?.enabled || status.isError ?
    <Button type="button" variant="ghost" disabled={status.isFetching} onClick={() => void status.refetch()}>Refresh</Button> : undefined}>
    <div className="panel-body agent-fields" aria-busy={status.isPending}>
      {status.isPending ? <p className="field-help">Checking KAgent…</p>
        : status.isError ? <p className="field-help" role="status">{status.error.message}</p>
        : !status.data?.enabled ? <p className="field-help">Live status is off. Configure read-only cluster access to see deployments{resource === "mcp-servers" ? " and discovered tools" : ""}.</p>
        : deployed.length === 0 && missing.length === 0 ? <p className="field-help">No deployments in this namespace.</p>
        : <ul className="agent-deployments">
          {deployed.map(item => {
            const [label, hue] = stateLabels[item.state];
            const accepted = item.state === "ready" && (resource === "models" || resource === "mcp-servers") && item.tools === null;
            return <li key={item.name}>
              <div className="agent-deployment-title"><code>{item.name}</code><Stamp hue={hue}>{accepted ? "Accepted" : label}</Stamp></div>
              {item.managed && item.state !== "deleting" && onEdit && <Button type="button" variant="ghost" disabled={editing}
                aria-label={`Edit ${item.name}`} onClick={() => onEdit(item.name)}>Edit</Button>}
              {item.reason && <p className="field-help">{reasonLabel(item.reason)}</p>}
              {resource === "agents" && item.managed && Boolean(item.manual_tools?.length) &&
                <p className="field-help">{item.manual_tools?.length} attached MCP {item.manual_tools?.length === 1 ? "tool" : "tools"} beyond the sandbox set</p>}
              {item.tools && (item.tools.length ? <details className="agent-discovered-tools">
                <summary>{item.tools.length} discovered {item.tools.length === 1 ? "tool" : "tools"}</summary>
                <ul>{item.tools.map(tool => <li key={tool.name}><code>{tool.name}</code>
                  {tool.description && <p className="field-help">{tool.description}</p>}
                </li>)}</ul>
              </details> : <p className="field-help">No tools discovered.</p>)}
            </li>;
          })}
          {missing.map(name => <li key={name} className="agent-deployment-title"><code>{name}</code><Stamp>Not deployed</Stamp></li>)}
        </ul>}
    </div>
  </Panel>;
}
