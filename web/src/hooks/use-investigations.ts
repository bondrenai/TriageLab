import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cancelRun, cleanupRun, closeRun, createRun, getRun, getSettings, listRuns, pauseRun, postTicketNote, resumeRun, steerRun, type ResumeInput } from "../lib/investigations";
import type { InvestigationReport } from "../lib/types";
import { queryKeys } from "../lib/query-keys";
import { APIError } from "../lib/api-client";

export const useSettings = () => useQuery({ queryKey: queryKeys.settings, queryFn: getSettings, staleTime: 60_000 });
export function useRuns() {
  const client = useQueryClient();
  return useQuery({
    queryKey: queryKeys.runs,
    queryFn: async () => {
      try {
        return await listRuns();
      } catch (error) {
        if (error instanceof APIError && (error.status === 401 || error.status === 403)) {
          // Transient failures retain confirmed rows. Revoked access must remove
          // those rows from the shared cache before another view can reuse them.
          client.setQueryData(queryKeys.runs, { runs: [] });
        }
        throw error;
      }
    },
    retry: (failureCount, error) => !(error instanceof APIError && (error.status === 401 || error.status === 403)) && failureCount < 2,
    refetchInterval: query => query.state.error instanceof APIError && (query.state.error.status === 401 || query.state.error.status === 403)
      ? false : query.state.data?.runs.some(run => run.active) ? 2_000 : 10_000,
  });
}
export const useRun = (id: string) => useQuery({ queryKey: queryKeys.run(id), queryFn: () => getRun(id), enabled: Boolean(id), refetchInterval: query => query.state.data?.active || query.state.data?.cleanup_running ? 2_000 : false });
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

export function useSteerRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (input: ResumeInput) => steerRun(id, input), onSettled: () => refreshRun(client, id) });
}

export function useResumeRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (input: ResumeInput) => resumeRun(id, input), onSettled: () => refreshRun(client, id) });
}

export function useCloseRun(id: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (input: { round: number; settings: string }) => closeRun(id, input), onSettled: () => refreshRun(client, id) });
}
export function useCleanupRun(id: string) {
  const client = useQueryClient();
  const refresh = () => Promise.all([client.invalidateQueries({ queryKey: queryKeys.run(id) }), client.invalidateQueries({ queryKey: queryKeys.runs })]);
  return useMutation({ mutationFn: () => cleanupRun(id), onSuccess: refresh, onError: refresh });
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
