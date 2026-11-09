import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getDeploymentStatus, readDefinition, renderManifest, saveDefinition } from "../lib/agents";
import { queryKeys } from "../lib/query-keys";
import type { DeploymentResource, ManifestDefinitions, SavedDefinition } from "../lib/types/agent";

export const useManifest = <K extends keyof ManifestDefinitions>(resource: K) =>
  useMutation({ mutationFn: (definition: ManifestDefinitions[K]) => renderManifest(resource, definition) });

export function useDeploymentStatus(resource: DeploymentResource, active: boolean) {
  return useQuery({
    queryKey: queryKeys.deploymentStatus(resource),
    queryFn: ({ signal }) => getDeploymentStatus(resource, signal),
    enabled: active,
    retry: false,
    refetchInterval: query => active && query.state.data?.enabled !== false ? 15_000 : false,
  });
}

export function useDefinitionEditor<K extends keyof ManifestDefinitions>(resource: K) {
  const [initial, setInitial] = useState<SavedDefinition<K>>();
  const [key, setKey] = useState(0);
  const client = useQueryClient();
  const save = useMutation({
    mutationFn: (definition: ManifestDefinitions[K]) => saveDefinition(resource, definition, initial?.revision),
    onSuccess: saved => {
      setInitial(saved);
      void client.invalidateQueries({ queryKey: queryKeys.deploymentStatus(resource) });
    },
  });
  const load = useMutation({
    mutationFn: (name: string) => readDefinition(resource, name),
    onSuccess: saved => {
      setInitial(saved);
      save.reset();
      setKey(key => key + 1);
    },
  });
  function createNew() {
    setInitial(undefined);
    save.reset();
    load.reset();
    setKey(key => key + 1);
  }
  return { initial: initial?.definition, key, save, load, createNew };
}

export type DefinitionEditor<K extends keyof ManifestDefinitions> = ReturnType<typeof useDefinitionEditor<K>>;
