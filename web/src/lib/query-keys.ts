import type { DeploymentResource } from "./types/agent";

export const queryKeys = {
  tickets: ["tickets"] as const,
  settings: ["settings"] as const,
  runs: ["runs"] as const,
  run: (id: string) => ["runs", id] as const,
  deploymentStatus: (resource: DeploymentResource) => ["deployment-status", resource] as const,
};
