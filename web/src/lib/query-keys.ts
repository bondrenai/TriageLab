export const queryKeys = {
  settings: ["settings"] as const,
  runs: ["runs"] as const,
  run: (id: string) => ["runs", id] as const,
};
