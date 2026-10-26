import { skipToken, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, client, result } from "../../api/client";
import { refreshRejectedSession } from "../../api/session";

const endpoints = {
  reset: "/api/v1/auth/reset/validate",
  invitation: "/api/v1/auth/invitations/validate",
  signup: "/api/v1/hosting/signup/validate",
  email: "/api/v1/auth/email/validate",
} as const;
type LinkKind = keyof typeof endpoints;
type LinkState = "checking" | "invalid" | "unavailable" | "ready";

export function useAuthLinkCheck(
  kind: LinkKind,
  token?: string,
  accountId?: string,
) {
  const queries = useQueryClient();
  const check = useQuery({
    queryKey: ["auth-link", kind, token, accountId],
    queryFn: token
      ? async ({ signal }) => {
          try {
            await result(
              client.POST(endpoints[kind], { body: { token }, signal }),
            );
          } catch (error) {
            if (kind === "email") await refreshRejectedSession(error, queries);
            throw error;
          }
          return true;
        }
      : skipToken,
    gcTime: 0,
    staleTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const state: LinkState = check.isPending
    ? "checking"
    : check.error
      ? check.error instanceof ApiError &&
        ["invalid_credentials", "invalid_input"].includes(
          check.error.detail.code,
        )
        ? "invalid"
        : "unavailable"
      : "ready";
  return {
    state,
    error: check.error,
    isFetching: check.isFetching,
    refetch: check.refetch,
  };
}
