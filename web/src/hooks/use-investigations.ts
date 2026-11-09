import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cancelRun, createRun, getRun, getSettings, listRuns, postTicketNote } from "../lib/investigations";
import type { InvestigationReport } from "../lib/types";
import { queryKeys } from "../lib/query-keys";

export const useSettings = () => useQuery({ queryKey: queryKeys.settings, queryFn: getSettings, staleTime: 60_000 });
export const useRuns = () => useQuery({ queryKey: queryKeys.runs, queryFn: listRuns, refetchInterval: query => query.state.data?.runs.some(run => run.active) ? 2_000 : 10_000 });
export const useRun = (id: string) => useQuery({ queryKey: queryKeys.run(id), queryFn: () => getRun(id), enabled: Boolean(id), refetchInterval: query => query.state.data?.active ? 2_000 : false });
export function useCreateRun() {
  const client = useQueryClient();
  return useMutation({ mutationFn: createRun, onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.runs }) });
}
export function useCancelRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: () => cancelRun(id), onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.runs }) });
}

export function usePostTicketNote(id: string) {
  const client = useQueryClient();
  const queryKey = queryKeys.run(id);
  return useMutation({
    mutationFn: (plan: string) => postTicketNote(id, plan),
    onSuccess: note => client.setQueryData<InvestigationReport>(queryKey, report => report && { ...report, ticket_note: note }),
    onError: () => client.invalidateQueries({ queryKey }),
  });
}
