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
function isErrorBody(body: unknown): body is Schema["Error"] {
  const error = body as Partial<Schema["Error"]> | null | undefined;
  return (
    typeof error?.code === "string" &&
    typeof error.message === "string" &&
    error.message !== ""
  );
}
// A reverse proxy or a router default answers in another format, with no message.
function unreadableError(response: Response): Schema["Error"] {
  const requestId = response.headers.get("X-Request-ID") ?? "";
  return response.status === 429
    ? {
        code: "rate_limited",
        message: "Too many requests. Try again later.",
        requestId,
      }
    : {
        code: "network_error",
        message: "The request could not be completed.",
        requestId,
      };
}
export async function result<T>(
  request: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const r = await request;
  if (r.error || !r.response.ok) {
    throw new ApiError(
      isErrorBody(r.error) ? r.error : unreadableError(r.response),
      r.response.status,
    );
  }
  return r.data as T;
}
export async function loadSession({ signal }: { signal?: AbortSignal } = {}) {
  const session = await result(client.GET("/api/v1/session", { signal }));
  signal?.throwIfAborted();
  return session;
}

// signOut ends this browser's session and confirms it has ended. A logout can race another
// tab that renews the session, such as by confirming identity or changing the password. The
// server ends a renewal that comes after the logout read the session, but one that came first
// has already replaced the token the logout sent, so that logout is refused or ends nothing.
// Reading the session again presents the browser's current cookie, so one still signed in is
// logged out once more.
export async function signOut({ signal }: { signal?: AbortSignal } = {}) {
  let refused: unknown;
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      await result(client.POST("/api/v1/auth/logout", { signal }));
      refused = undefined;
    } catch (error) {
      // The token this tab sent was replaced, so its CSRF token no longer matches.
      if (!(error instanceof ApiError) || error.status !== 403) throw error;
      refused = error;
    }
    let session: Session;
    try {
      session = setSession(await loadSession({ signal }));
    } catch (error) {
      // The page that follows a sign-out reads the session itself, so an accepted logout
      // need not wait for this check.
      if (refused === undefined) return;
      throw error;
    }
    if (!session.user && !session.mfaPending) return;
  }
  throw refused ?? new Error("The session is still signed in.");
}
