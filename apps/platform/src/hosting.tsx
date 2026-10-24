import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { client, result, setSession } from "./api/client";
import { sessionOptions } from "./api/session";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";

export const hostingOptions = {
  queryKey: ["hosting"],
  queryFn: () => result(client.GET("/api/v1/hosting")),
};
export function Signup() {
  const hosting = useQuery(hostingOptions);
  const session = useQuery(sessionOptions).data;
  const action = useAction();
  const [done, setDone] = useState(false);
  if (!hosting.data?.enabled)
    return <p>Hosted signup is unavailable on this installation.</p>;
  return (
    <div className={s.stack}>
      <h1>Welcome to Astilba</h1>
      <p>
        Use your beta invitation to create a workspace. Then enable two-factor
        authentication in account security and choose your workspace
        subscription.
      </p>
      <ErrorNotice error={action.error} />
      {done ? (
        <p role="status">
          Your workspace is ready.{" "}
          <Link to="/account">Set up account security</Link>, then{" "}
          <Link to="/members">open workspace billing</Link>.
        </p>
      ) : (
        <section className={s.panel}>
          <Form
            action={action}
            label="Hosted signup"
            onSubmit={(d) =>
              action.run(async () => {
                setSession(
                  await result(
                    client.POST("/api/v1/hosting/signup", {
                      body: {
                        token: text(d, "token"),
                        workspace: text(d, "workspace"),
                        name: session?.user ? undefined : text(d, "name"),
                        password: session?.user
                          ? undefined
                          : text(d, "password"),
                      },
                    }),
                  ),
                );
                setDone(true);
              })
            }
          >
            <Field
              name="token"
              label="Beta invitation code"
              autoComplete="off"
            />
            <Field name="workspace" label="Workspace name" />
            {!session?.user && (
              <>
                <Field name="name" label="Your name" autoComplete="name" />
                <Field
                  name="password"
                  label="Password"
                  type="password"
                  autoComplete="new-password"
                  minLength={15}
                />
              </>
            )}
            {session?.user && (
              <p>
                Accepting as {session.user.email}. Two-factor authentication is
                required.
              </p>
            )}
            <BusyButton busy={action.isPending}>
              Create invited workspace
            </BusyButton>
          </Form>
        </section>
      )}
    </div>
  );
}
export function EmailVerification() {
  const user = useQuery(sessionOptions).data?.user;
  const action = useAction();
  const [sent, setSent] = useState(false);
  if (user?.emailVerified) return <p>Email verified.</p>;
  return (
    <section className={s.panel}>
      <h2>Verify your email</h2>
      <p>Email verification is required for hosted workspace changes.</p>
      <ErrorNotice error={action.error} />
      <button
        type="button"
        disabled={action.isPending}
        onClick={() =>
          action.run(async () => {
            await result(client.POST("/api/v1/auth/email/request"));
            setSent(true);
          })
        }
      >
        Send verification code
      </button>
      {sent && (
        <p role="status">Check your email for a code valid for one hour.</p>
      )}
      <Form
        action={action}
        label="Verify email"
        onSubmit={(d) =>
          action.run(async () => {
            setSession(
              await result(
                client.POST("/api/v1/auth/email/verify", {
                  body: { token: text(d, "token") },
                }),
              ),
            );
          })
        }
      >
        <Field
          name="token"
          label="Email verification code"
          autoComplete="one-time-code"
        />
        <BusyButton busy={action.isPending}>Verify email</BusyButton>
      </Form>
    </section>
  );
}
export function WorkspaceSubscription({
  workspaceId,
  owner,
}: {
  workspaceId: string;
  owner: boolean;
}) {
  const action = useAction();
  const status = useQuery({
    queryKey: ["subscription", workspaceId],
    enabled: !!workspaceId,
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/subscription", {
          params: { path: { workspaceId } },
        }),
      ),
  });
  if (!status.data?.hosted) return <ErrorNotice error={status.error} />;
  const data = status.data;
  return (
    <section className={s.panel}>
      <h2>Workspace subscription</h2>
      <ErrorNotice error={action.error ?? status.error} />
      <p>
        Status: {data.status}. Server allowance: {data.serverLimit}. No
        per-application, database or member charges.
      </p>
      {data.managementUntil && (
        <p>
          Management access until{" "}
          {new Date(data.managementUntil).toLocaleString()}.
        </p>
      )}
      {data.recoveryUntil && (
        <p>
          Export, recovery and transfer remain available until{" "}
          {new Date(data.recoveryUntil).toLocaleString()}. Running applications
          and installed schedules remain on your servers.
        </p>
      )}
      <p>
        {data.support ||
          "Contact the beta operator for asynchronous support hours."}
      </p>
      {owner && data.billingEnabled && (
        <div className={s.row}>
          <button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/subscription/checkout",
                    { params: { path: { workspaceId } } },
                  ),
                );
                location.assign(v.url);
              })
            }
          >
            Start subscription
          </button>
          <button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/subscription/portal",
                    { params: { path: { workspaceId } } },
                  ),
                );
                location.assign(v.url);
              })
            }
          >
            Manage billing
          </button>
        </div>
      )}
      {owner && (
        <p>
          Billing changes require verified email, two-factor authentication and
          recent identity confirmation in{" "}
          <Link to="/account">account security</Link>.
        </p>
      )}
    </section>
  );
}
