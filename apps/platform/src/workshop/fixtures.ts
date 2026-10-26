import { delay } from "msw";
import type { AnyHandler } from "msw";
import { http, HttpResponse } from "msw/http";
import type { Schema, Session } from "../api/client";

export const guest: Session = {
  csrfToken: "workshop-csrf",
  mfaPending: false,
  recentAuth: false,
  workspaces: [],
};
export const member: Session = {
  ...guest,
  user: {
    id: "00000000-0000-4000-8000-000000000001",
    email: "alex@example.test",
    name: "Alex",
    mfaEnabled: true,
    recoveryCodesRemaining: 10,
    recoveryCodesSaved: true,
    emailVerified: true,
  },
};
export const pendingMFA: Session = { ...member, mfaPending: true };

export function scenario(session: Session, ...actions: AnyHandler[]) {
  return {
    workshopSession: session,
    msw: {
      handlers: {
        session: sessionHandlers(session),
        action: actions,
      },
    },
  };
}

export function sessionHandlers(session = guest, hosted = false) {
  return [
    http.get("/api/v1/session", () => HttpResponse.json(session)),
    http.get("/api/v1/hosting", () =>
      HttpResponse.json({
        enabled: hosted,
        billingEnabled: false,
        support: "",
      }),
    ),
    http.post(
      "/api/v1/auth/logout",
      () => new HttpResponse(null, { status: 204 }),
    ),
  ];
}

export function reject(
  path: string,
  code = "invalid_credentials",
  status = 401,
) {
  return http.post(`/api/v1/auth/${path}`, () =>
    HttpResponse.json(
      {
        code,
        message: "Workshop response.",
        requestId: "workshop-request",
      } satisfies Schema["Error"],
      { status },
    ),
  );
}

export function accept(path: string) {
  return http.post(`/api/v1/auth/${path}`, () =>
    path === "invitations/accept"
      ? HttpResponse.json(member)
      : new HttpResponse(null, { status: 204 }),
  );
}

export function submitting(path: string) {
  return http.post(`/api/v1/auth/${path}`, async () => {
    await delay("infinite");
    return new HttpResponse(null, { status: 204 });
  });
}

export const hostedWorkspaceId = "00000000-0000-4000-8000-000000000002";
export const hostedMember: Session = {
  ...member,
  recentAuth: true,
  workspaces: [
    {
      id: hostedWorkspaceId,
      name: "My workspace",
      role: "owner",
      requireMfa: false,
    },
  ],
};
export const hostedSubscription: Schema["Subscription"] = {
  hosted: true,
  billingEnabled: true,
  managementActive: false,
  status: "pending",
  serverLimit: 2,
  support: "Contact your installation operator.",
};
export function hostedScenario(...args: Parameters<typeof scenario>) {
  const configured = scenario(...args);
  configured.msw.handlers.session = sessionHandlers(args[0], true);
  return configured;
}
