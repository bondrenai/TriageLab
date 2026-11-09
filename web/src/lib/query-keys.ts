export const queryKeys = {
  tickets: ["tickets"] as const,
  settings: ["settings"] as const,
  runs: ["runs"] as const,
  run: (id: string) => ["runs", id] as const,
};
