import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { queryKeys } from "../lib/query-keys";
import { readTicketWorkspace, takeTicketAction } from "../lib/tickets";

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
