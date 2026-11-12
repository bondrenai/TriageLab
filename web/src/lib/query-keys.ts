import type { DeploymentResource } from "./types/agent";

export const queryKeys = {
  session: ["session"] as const,
  accounts: ["accounts"] as const,
  apiClients: ["api-clients"] as const,
  tickets: ["tickets"] as const,
  connections: ["connections"] as const,
  settings: ["settings"] as const,
  runs: ["runs"] as const,
  run: (id: string) => ["runs", id] as const,
  resultChat: (id: string) => ["runs", id, "chat"] as const,
  deploymentStatus: (resource: DeploymentResource) => ["deployment-status", resource] as const,
};
