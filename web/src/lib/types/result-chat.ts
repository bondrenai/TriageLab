export interface ResultChatCitation {
  round: number;
  process_id: string;
}

export interface ResultChatMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  created_at: string;
  citations?: ResultChatCitation[];
}

export interface ResultChat {
  messages: ResultChatMessage[];
  status: "idle" | "answering" | "error" | "interrupted";
  error?: string;
  can_ask: boolean;
  unavailable_reason?: string;
  max_message_bytes: number;
}
