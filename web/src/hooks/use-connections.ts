import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { queryKeys } from "../lib/query-keys";
import {
  beginZendeskConnect, createConnection, deleteConnection, listConnections, testConnection, testSavedConnection,
  updateConnection,
} from "../lib/connections";
import type { ConnectionInput } from "../lib/types/connection";

const refreshConnections = (client: ReturnType<typeof useQueryClient>) => () => {
  void client.invalidateQueries({ queryKey: queryKeys.connections });
  void client.invalidateQueries({ queryKey: queryKeys.tickets });
};

export function useConnections() {
  return useQuery({ queryKey: queryKeys.connections, queryFn: listConnections });
}

export function useCreateConnection() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: createConnection,
    onSettled: refreshConnections(client),
  });
}

export function useUpdateConnection() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: ConnectionInput }) => updateConnection(id, input),
    onSettled: refreshConnections(client),
  });
}

export function useRemoveConnection() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: deleteConnection,
    onSettled: refreshConnections(client),
  });
}

// A test against a saved connection keeps its stored credential when the form
// does not supply a replacement token.
export function useTestConnection(saved: boolean) {
  return useMutation({
    mutationFn: ({ id, input }: { id?: string; input: ConnectionInput }) =>
      saved && id ? testSavedConnection(id, input) : testConnection(input),
  });
}

export function useZendeskConnect() {
  return useMutation({ mutationFn: beginZendeskConnect, onSuccess: ({ url }) => window.location.assign(url) });
}
