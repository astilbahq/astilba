import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { client, result, setSession } from "../../api/client";
import { sessionOptions } from "../../api/session";
import { Link } from "../../components/link";
import * as s from "../../styles";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import * as auth from "./auth.styles";
import { authValidation } from "./validation";

export function Invitation({ token }: { token?: string }) {
  const action = useAction();
  const session = useQuery(sessionOptions).data;
  const [done, setDone] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const returnTo = token
    ? `/invitation?${new URLSearchParams({ token })}`
    : undefined;

  useEffect(() => {
    if (done) heading.current?.focus();
  }, [done]);

  return (
    <div className={auth.flow}>
      <div className={auth.headingGroup}>
        <h1 ref={heading} tabIndex={-1}>
          {done ? "Workspace joined" : "Join a workspace"}
        </h1>
        {!done && token && (
          <p className={s.muted}>
            {session?.user
              ? `Signed in as ${session.user.email}.`
              : "If you already have an account, sign in as the invited email first."}
          </p>
        )}
      </div>
      <section className={s.stack}>
        {!token ? (
          <p className={s.muted}>
            Open the invitation link in your email to join a workspace.
          </p>
        ) : done ? (
          <p className={s.muted} role="status">
            You can now open your projects.
          </p>
        ) : (
          <>
            <Form
              action={action}
              validation={
                session?.user
                  ? authValidation.acceptInvitation
                  : authValidation.invitation
              }
              onSubmit={(data) =>
                action.run(async () => {
                  setSession(
                    await result(
                      client.POST("/api/v1/auth/invitations/accept", {
                        body: {
                          token,
                          name: text(data, "name") || undefined,
                          password: text(data, "password") || undefined,
                        },
                      }),
                    ),
                  );
                  setDone(true);
                })
              }
            >
              {!session?.user && (
                <>
                  <Field
                    name="name"
                    label="Your name"
                    autoComplete="name"
                    placeholder="Enter your name"
                    variant="quiet"
                  />
                  <Field
                    name="password"
                    label="Choose a password"
                    type="password"
                    autoComplete="new-password"
                    placeholder="Create a password"
                    variant="quiet"
                  />
                </>
              )}
              <BusyButton busy={action.isPending}>Accept invitation</BusyButton>
            </Form>
          </>
        )}
        <div className={auth.secondaryActions}>
          {done ? (
            <Link to="/" className={auth.secondaryLink}>
              Open projects
            </Link>
          ) : session?.user ? (
            <Link to="/" className={auth.secondaryLink}>
              Back to projects
            </Link>
          ) : (
            <Link
              to="/login"
              search={{ returnTo }}
              className={auth.secondaryLink}
            >
              Back to sign in
            </Link>
          )}
        </div>
      </section>
    </div>
  );
}
