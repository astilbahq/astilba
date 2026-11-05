import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { ApiError, loadSession, type Session, setSession } from "./client";

let pendingRenewals = 0;

// A read overlapping a renewal can carry the token it just replaced and report signed out. Acting
// on that would abort the renewal before the browser stores its new cookie. useAction refetches
// when the request settles, so a skipped reply is replaced straight away.
export async function holdSignedOutReads<T>(renewal: () => Promise<T>) {
  pendingRenewals++;
  try {
    return await renewal();
  } finally {
    pendingRenewals--;
  }
}

export const sessionOptions = queryOptions({
  queryKey: ["session"],
  queryFn: async ({ client, queryKey, signal }) => {
    const session = await loadSession({ signal });
    const current = client.getQueryData<Session>(queryKey);
    const signedOut = !session.user && !session.mfaPending;
    return pendingRenewals > 0 && current?.user && signedOut
      ? current
      : setSession(session);
  },
});

export async function refreshRejectedSession(
  error: unknown,
  queries: QueryClient,
) {
  if (error instanceof ApiError && [401, 403].includes(error.status)) {
    await queries.invalidateQueries({ queryKey: sessionOptions.queryKey });
  }
}
