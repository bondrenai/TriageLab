import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createAccount, getSession, listAccounts, removeAccount, setAccountPassword, signIn, signOut, updateAccount,
  type Role,
} from "../lib/auth";
import { queryKeys } from "../lib/query-keys";

/** Session bootstrap. Unknown until the first response arrives. */
export const useSession = () =>
  useQuery({ queryKey: queryKeys.session, queryFn: getSession, staleTime: 60_000, retry: false });

export function useSignIn() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { username: string; password: string }) => signIn(input.username, input.password),
    onSuccess: session => client.setQueryData(queryKeys.session, session),
  });
}

export function useSignOut() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: signOut,
    onSettled: () => {
      client.setQueryData(queryKeys.session, { authenticated: false, oidc_enabled: false });
      client.clear();
    },
  });
}

const refreshAccounts = (client: ReturnType<typeof useQueryClient>) => () =>
  client.invalidateQueries({ queryKey: queryKeys.accounts });

export const useAccounts = () => useQuery({ queryKey: queryKeys.accounts, queryFn: listAccounts });

export function useCreateAccount() {
  const client = useQueryClient();
  return useMutation({ mutationFn: createAccount, onSuccess: refreshAccounts(client) });
}

export function useUpdateAccount() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { username: string; role?: Role; disabled?: boolean }) => updateAccount(input.username, input),
    onSuccess: refreshAccounts(client),
  });
}

export function useSetAccountPassword() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { username: string; password: string }) => setAccountPassword(input.username, input.password),
    onSuccess: refreshAccounts(client),
  });
}

export function useRemoveAccount() {
  const client = useQueryClient();
  return useMutation({ mutationFn: (username: string) => removeAccount(username), onSuccess: refreshAccounts(client) });
}
