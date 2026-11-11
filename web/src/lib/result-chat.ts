import { request } from "./api-client";
import type { ResultChat } from "./types/result-chat";

export const getResultChat = (id: string) => request<ResultChat>(`/api/runs/${encodeURIComponent(id)}/chat`);
export const askResultChat = (id: string, message: string) => request<ResultChat>(`/api/runs/${encodeURIComponent(id)}/chat`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ message }),
});
