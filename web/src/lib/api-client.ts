export class APIError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...options,
    headers: { "X-TriageLab-Request": "1", ...options.headers },
  });
  if (!response.ok) {
    let message = `Request failed (${response.status}).`;
    try {
      const body = await response.json();
      if (typeof body.error === "string") message = body.error;
    } catch { /* A proxy may return an HTML error instead of API JSON. */ }
    // A rejected session ends the workspace view; the app shell listens for
    // this and returns to the sign-in page. Only the sign-in attempt itself
    // expects a 401 without an existing session.
    if (response.status === 401 && path !== "/api/auth/login") {
      window.dispatchEvent(new CustomEvent("triagelab:unauthorized"));
    }
    throw new APIError(message, response.status);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}
