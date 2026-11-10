export interface AgentModel {
  provider: string;
  name: string;
  endpoint?: string;
  api_format?: string;
  secret_name?: string;
  secret_key?: string;
}

export interface ModelDefinition {
  name: string;
  model: AgentModel;
}

export interface MCPServerDefinition {
  name: string;
  url: string;
  protocol: string;
  secret_name?: string;
  secret_key?: string;
  manual_tools?: string[];
}

export interface AgentDefinition {
  name: string;
  description: string;
  system_prompt: string;
  model_ref?: string;
  runtime?: { image: string; command: string[]; args: string[] };
  mcp_servers?: { name: string; tools: string[] }[];
}

export interface SandboxEnvVar {
  name: string;
  value: string;
}

export interface SandboxTemplateDefinition {
  name: string;
  workload_image: string;
  env?: SandboxEnvVar[];
  worker_pool: string;
  snapshot_location: string;
  egress_policy_refs?: string[];
}

export interface EgressDestinationDefinition {
  origin: string;
  header?: string;
  prefix?: string;
  secret_name?: string;
  secret_key?: string;
}

export interface EgressPolicyDefinition {
  name: string;
  destinations: EgressDestinationDefinition[];
}

export interface ManifestDefinitions {
  agents: AgentDefinition;
  models: ModelDefinition;
  "mcp-servers": MCPServerDefinition;
  sandboxes: SandboxTemplateDefinition;
  "egress-policies": EgressPolicyDefinition;
}

export type DeploymentResource = keyof ManifestDefinitions;

export interface SavedDefinition<K extends keyof ManifestDefinitions> {
  definition: ManifestDefinitions[K];
  revision: string;
}

export interface kagentManifest {
  kind: string;
  metadata?: { name: string; namespace: string };
  items?: Omit<kagentManifest, "items">[];
}

export interface DiscoveredTool {
  name: string;
  description: string;
  selection_error?: string;
}

export interface Deployment {
  name: string;
  state: "ready" | "pending" | "attention" | "deleting";
  reason?: string;
  tools: DiscoveredTool[] | null;
  managed?: boolean;
  manual_tools?: string[];
}

export interface DeploymentStatus {
  enabled: boolean;
  resources: Deployment[];
}
