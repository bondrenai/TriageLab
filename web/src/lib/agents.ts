import { request } from "./api-client";
import type { DeploymentResource, DeploymentStatus, kagentManifest, ManifestDefinitions, SavedDefinition } from "./types/agent";

export function parseToolNames(value: string) {
  return value.split(/[,\s]+/).filter(Boolean);
}

export function renderManifest<K extends keyof ManifestDefinitions>(resource: K, definition: ManifestDefinitions[K]) {
  return request<kagentManifest>(`/api/${resource}/manifest`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(definition),
  });
}

export function getDeploymentStatus(resource: DeploymentResource, signal: AbortSignal) {
  return request<DeploymentStatus>(`/api/${resource}/status`, { signal });
}

export function readDefinition<K extends keyof ManifestDefinitions>(resource: K, name: string) {
  return request<SavedDefinition<K>>(`/api/${resource}/definition?${new URLSearchParams({ name })}`);
}

export function saveDefinition<K extends keyof ManifestDefinitions>(resource: K, definition: ManifestDefinitions[K], revision?: string) {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (revision) headers["If-Match"] = JSON.stringify(revision);
  return request<SavedDefinition<K>>(`/api/${resource}/definition`, { method: "POST", headers, body: JSON.stringify(definition) });
}
