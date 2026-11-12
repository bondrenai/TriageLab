import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { queryKeys } from "../lib/query-keys";
import { beginZendeskOAuth, previewProcedure, readTicketWorkspace, saveProcedure, takeTicketAction } from "../lib/tickets";
import type { ProcedureVersion, TicketProcedure } from "../lib/types/ticket";

export function useTicketWorkspace() {
  return useQuery({ queryKey: queryKeys.tickets, queryFn: readTicketWorkspace, refetchInterval: 10000 });
}

export function useTicketAction() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: takeTicketAction,
    onSettled: (_result, _error, action) => {
      if (action.action !== "preview") {
        void client.invalidateQueries({ queryKey: queryKeys.tickets });
      }
    },
  });
}

export function useZendeskOAuth() {
  return useMutation({
    mutationFn: beginZendeskOAuth,
    onSuccess: ({ url }) => window.location.assign(url),
  });
}

export function useSaveProcedure() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ connection, version }: { connection: string; version: ProcedureVersion }) =>
      saveProcedure(connection, version),
    onSettled: () => { void client.invalidateQueries({ queryKey: queryKeys.tickets }); },
  });
}

export function usePreviewProcedure() {
  return useMutation({
    mutationFn: ({ connection, id, procedure }: { connection: string; id: string; procedure: TicketProcedure }) =>
      previewProcedure(connection, id, procedure),
  });
}
