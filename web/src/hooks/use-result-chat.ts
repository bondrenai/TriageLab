import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIError } from "../lib/api-client";
import { askResultChat, getResultChat } from "../lib/result-chat";
import { queryKeys } from "../lib/query-keys";
import type { ResultChat } from "../lib/types/result-chat";

export function isChatAccessDenied(error: unknown) {
  return error instanceof APIError && (error.status === 401 || error.status === 403);
}

export function useResultChat(id: string) {
  const client = useQueryClient();
  return useQuery({
    queryKey: queryKeys.resultChat(id),
    queryFn: async () => {
      try {
        return await getResultChat(id);
      } catch (error) {
        if (isChatAccessDenied(error)) {
          client.setQueryData<ResultChat>(queryKeys.resultChat(id), {
            messages: [], status: "idle", can_ask: false, max_message_bytes: 4096,
          });
        }
        throw error;
      }
    },
    enabled: Boolean(id),
    retry: false,
    refetchInterval: query => isChatAccessDenied(query.state.error) ? false : query.state.data?.status === "answering" ? 1500 : 10_000,
  });
}

export function useAskResultChat(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (message: string) => askResultChat(id, message),
    retry: false,
    onSuccess: chat => client.setQueryData(queryKeys.resultChat(id), chat),
    onError: () => client.invalidateQueries({ queryKey: queryKeys.resultChat(id), exact: true }),
  });
}
