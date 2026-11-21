import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createAccount, createClient, getSession, listAccounts, listClients, removeAccount, removeClient,
  rotateClientSecret, setAccountPassword, signIn, signOut, updateAccount, updateClient,
  type Role, type Scope,
} from "../lib/auth";
import { queryKeys } from "../lib/query-keys";

/** Session bootstrap. Unknown until the first response arrives. */
export const useSession = () =>
  useQuery({ queryKey: queryKeys.session, queryFn: getSession, staleTime: 60_000, retry: false });

export function useSignIn() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { username: string; password: string }) => signIn(input.username, input.password),
    onSuccess: session => {
      client.removeQueries({ predicate: query => query.queryKey[0] !== queryKeys.session[0] });
      client.setQueryData(queryKeys.session, session);
    },
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

export const useAccounts = (enabled: boolean) =>
  useQuery({ queryKey: queryKeys.accounts, queryFn: listAccounts, enabled });

export function useCreateAccount() {
  const client = useQueryClient();
  return useMutation({ mutationFn: createAccount, onSuccess: refreshAccounts(client) });
}

export function useUpdateAccount() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ username, ...patch }: { username: string; role?: Role; disabled?: boolean }) =>
      updateAccount(username, patch),
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

const refreshClients = (client: ReturnType<typeof useQueryClient>) => () =>
  client.invalidateQueries({ queryKey: queryKeys.apiClients });

export const useClients = (enabled: boolean) =>
  useQuery({ queryKey: queryKeys.apiClients, queryFn: listClients, enabled });

export function useCreateClient() {
  const client = useQueryClient();
  return useMutation({ mutationFn: createClient, onSuccess: refreshClients(client) });
}

export function useUpdateClient() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...patch }: { id: string; scopes?: Scope[]; disabled?: boolean }) =>
      updateClient(id, patch),
    onSuccess: refreshClients(client),
  });
}

export function useRotateClientSecret() {
  const client = useQueryClient();
  return useMutation({ mutationFn: rotateClientSecret, onSuccess: refreshClients(client) });
}

export function useRemoveClient() {
  const client = useQueryClient();
  return useMutation({ mutationFn: (id: string) => removeClient(id), onSuccess: refreshClients(client) });
}
