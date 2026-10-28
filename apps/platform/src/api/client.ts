import createClient from "openapi-fetch";
import type { components, paths } from "./generated/schema";
export type Schema = components["schemas"];
export type Session = Schema["Session"];
export const client = createClient<paths>({
  baseUrl: "",
  credentials: "same-origin",
});
let csrf = "";
export function setSession(session: Session) {
  csrf = session.csrfToken;
  return session;
}
client.use({
  onRequest({ request }) {
    if (!["GET", "HEAD"].includes(request.method)) {
      request.headers.set("X-CSRF-Token", csrf);
    }
    return request;
  },
});
export class ApiError extends Error {
  constructor(
    public readonly detail: Schema["Error"],
    public readonly status: number,
  ) {
    super(detail.message);
  }
}
export async function result<T>(
  request: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const r = await request;
  if (r.error || !r.response.ok) {
    const error = r.error as Schema["Error"] | undefined;
    throw new ApiError(
      error ?? {
        code: "network_error",
        message: "The request could not be completed.",
        requestId: r.response.headers.get("X-Request-ID") ?? "",
      },
      r.response.status,
    );
  }
  return r.data as T;
}
export async function loadSession({ signal }: { signal?: AbortSignal } = {}) {
  const session = await result(client.GET("/api/v1/session", { signal }));
  signal?.throwIfAborted();
  return setSession(session);
}
