import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cancelRun, closeRun, createRun, getRun, getSettings, listRuns, pauseRun, postTicketNote, resumeRun, type ResumeInput } from "../lib/investigations";
import type { InvestigationReport } from "../lib/types";
import { queryKeys } from "../lib/query-keys";

export const useSettings = () => useQuery({ queryKey: queryKeys.settings, queryFn: getSettings, staleTime: 60_000 });
export const useRuns = () => useQuery({ queryKey: queryKeys.runs, queryFn: listRuns, refetchInterval: query => query.state.data?.runs.some(run => run.active) ? 2_000 : 10_000 });
export const useRun = (id: string) => useQuery({ queryKey: queryKeys.run(id), queryFn: () => getRun(id), enabled: Boolean(id), refetchInterval: query => query.state.data?.active ? 2_000 : 10_000 });
export function useCreateRun() {
  const client = useQueryClient();
  return useMutation({ mutationFn: createRun, onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.runs }) });
}
export function useCancelRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: () => cancelRun(id), onSettled: () => refreshRun(client, id) });
}

function refreshRun(client: ReturnType<typeof useQueryClient>, id: string) {
  return Promise.all([client.invalidateQueries({ queryKey: queryKeys.runs }), client.invalidateQueries({ queryKey: queryKeys.run(id) })]);
}

export function usePauseRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: () => pauseRun(id), onSettled: () => refreshRun(client, id) });
}

export function useResumeRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (input: ResumeInput) => resumeRun(id, input), onSettled: () => refreshRun(client, id) });
}

export function useCloseRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (input: { round: number; settings: string }) => closeRun(id, input), onSettled: () => refreshRun(client, id) });
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
