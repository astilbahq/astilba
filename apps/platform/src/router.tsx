import { useQuery } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  Link,
  Outlet,
} from "@tanstack/react-router";
import { useEffect, useState } from "react";
import {
  client,
  loadSession,
  result,
  type Session,
  setSession,
} from "./api/client";
import { Alerts } from "./features/alerts";
import { hostingOptions, Signup } from "./hosting";
import { Account, Members } from "./security";
import { Servers } from "./servers";
import * as s from "./styles";
import { ThemeContext } from "./theme";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
import { Dashboard } from "./workspace";
export const sessionOptions = { queryKey: ["session"], queryFn: loadSession };
function Root() {
  const [theme, setTheme] = useState(
    () =>
      localStorage.getItem("astilba-theme") ??
      (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"),
  );
  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem("astilba-theme", theme);
  }, [theme]);
  const session = useQuery(sessionOptions);
  const action = useAction();
  return (
    <ThemeContext value={theme === "dark" ? "dark" : "light"}>
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <div className={s.shell}>
        <aside className={s.side} aria-label="Workspace navigation">
          <Link to="/" className={s.brand}>
            ✳ astilba
          </Link>
          <div>
            <small>YOUR INFRASTRUCTURE</small>
            <nav aria-label="Main" className={s.nav}>
              <Link to="/">Projects</Link>
              <Link to="/servers">Servers</Link>
              <Link to="/alerts">Alerts</Link>
              <Link to="/members">Workspace</Link>
              <Link to="/account">Account security</Link>
            </nav>
          </div>
          <div className={s.stack}>
            <small>Open source. On your terms.</small>
            <button
              type="button"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              Use {theme === "dark" ? "light" : "dark"} theme
            </button>
            <a href="/source/NOTICE.txt">Licence & source</a>
            {session.data?.user && (
              <>
                <span className={s.muted}>{session.data.user.email}</span>
                <button
                  type="button"
                  disabled={action.isPending}
                  onClick={() =>
                    action.run(async () => {
                      await result(client.POST("/api/v1/auth/logout"));
                      location.assign("/");
                    })
                  }
                >
                  Sign out
                </button>
              </>
            )}
          </div>
        </aside>
        <main id="main" className={s.main}>
          <ErrorNotice error={session.error} />
          {session.isPending ? (
            <p role="status">Loading your workspace…</p>
          ) : session.data ? (
            <Outlet />
          ) : (
            <button type="button" onClick={() => void session.refetch()}>
              Retry connection
            </button>
          )}
        </main>
      </div>
    </ThemeContext>
  );
}
export function Authenticated({ children }: { children: React.ReactNode }) {
  const session = useQuery(sessionOptions);
  return session.data?.user ? children : <SignIn session={session.data} />;
}
function SignIn({ session }: { session?: Session }) {
  const hosting = useQuery(hostingOptions).data;
  const action = useAction();
  return (
    <div className={s.stack}>
      <div>
        <p className={s.muted}>WELCOME BACK</p>
        <h1>
          {session?.mfaPending
            ? "Verify it’s you"
            : "Your servers. Your workspace."}
        </h1>
        <p className={s.muted}>Sign in to manage your infrastructure.</p>
      </div>
      <section className={s.panel}>
        <ErrorNotice error={action.error} />
        {session?.mfaPending ? (
          <Form
            action={action}
            onSubmit={(d) =>
              action.run(async () =>
                setSession(
                  await result(
                    client.POST("/api/v1/auth/mfa", {
                      body: { code: text(d, "code") },
                    }),
                  ),
                ),
              )
            }
          >
            <Field
              name="code"
              label="Authenticator or recovery code"
              autoComplete="one-time-code"
            />
            <BusyButton busy={action.isPending}>Verify</BusyButton>
          </Form>
        ) : (
          <Form
            action={action}
            onSubmit={(d) =>
              action.run(async () =>
                setSession(
                  await result(
                    client.POST("/api/v1/auth/login", {
                      body: {
                        email: text(d, "email"),
                        password: text(d, "password"),
                      },
                    }),
                  ),
                ),
              )
            }
          >
            <Field
              name="email"
              label="Email"
              type="email"
              autoComplete="username"
            />
            <Field
              name="password"
              label="Password"
              type="password"
              autoComplete="current-password"
            />
            <BusyButton busy={action.isPending}>Sign in</BusyButton>
          </Form>
        )}
        <div className={s.row}>
          <Link to="/reset">Reset password</Link>
          <Link to="/invitation">Accept invitation</Link>
          {hosting?.enabled && <Link to="/signup">Join the hosted beta</Link>}
        </div>
        <small>
          Accounts are created by invitation. To set up a new installation,
          bootstrap its first owner from the server CLI.
        </small>
      </section>
    </div>
  );
}
function Reset() {
  const action = useAction();
  const [sent, setSent] = useState(false);
  const [complete, setComplete] = useState(false);
  return (
    <div className={s.stack}>
      <h1>Reset your password</h1>
      <ErrorNotice error={action.error} />
      <section className={s.panel}>
        <Form
          action={action}
          onSubmit={(d) =>
            action.run(async () => {
              await result(
                client.POST("/api/v1/auth/reset/request", {
                  body: { email: text(d, "email") },
                }),
              );
              setSent(true);
            })
          }
        >
          <Field
            name="email"
            label="Account email"
            type="email"
            autoComplete="username"
          />
          <BusyButton busy={action.isPending}>Send reset code</BusyButton>
        </Form>
        {sent && (
          <p role="status">
            If this account exists and email is configured, a reset code will
            arrive shortly.
          </p>
        )}
        <Form
          action={action}
          onSubmit={(d) =>
            action.run(async () => {
              await result(
                client.POST("/api/v1/auth/reset/complete", {
                  body: {
                    token: text(d, "token"),
                    password: text(d, "password"),
                  },
                }),
              );
              setComplete(true);
            })
          }
        >
          <Field
            name="token"
            label="Reset code from email"
            autoComplete="one-time-code"
          />
          <Field
            name="password"
            label="New password (15–128 characters)"
            type="password"
            autoComplete="new-password"
            minLength={15}
          />
          <BusyButton busy={action.isPending}>Set password</BusyButton>
        </Form>
        {complete && (
          <p role="status">
            Password changed. <Link to="/">Sign in</Link>
          </p>
        )}
      </section>
    </div>
  );
}
function Invitation() {
  const action = useAction();
  const session = useQuery(sessionOptions);
  const [done, setDone] = useState(false);
  return (
    <div className={s.stack}>
      <h1>Join a workspace</h1>
      <p className={s.muted}>
        If you already have an account, sign in as the invited email first.
      </p>
      <section className={s.panel}>
        <ErrorNotice error={action.error} />
        <Form
          action={action}
          onSubmit={(d) =>
            action.run(async () => {
              setSession(
                await result(
                  client.POST("/api/v1/auth/invitations/accept", {
                    body: {
                      token: text(d, "token"),
                      name: text(d, "name") || undefined,
                      password: text(d, "password") || undefined,
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
            label="Invitation code from email"
            autoComplete="one-time-code"
          />
          {!session.data?.user && (
            <>
              <Field name="name" label="Your name" autoComplete="name" />
              <Field
                name="password"
                label="Choose a password (15–128 characters)"
                type="password"
                autoComplete="new-password"
                minLength={15}
              />
            </>
          )}
          <BusyButton busy={action.isPending}>Accept invitation</BusyButton>
        </Form>
        {done && (
          <p role="status">
            Workspace joined. <Link to="/">Open projects</Link>
          </p>
        )}
      </section>
    </div>
  );
}
const root = createRootRoute({
  component: Root,
  notFoundComponent: () => (
    <p>
      Page not found. <Link to="/">Return to projects</Link>
    </p>
  ),
});
export const dashboardRoute = createRoute({
  getParentRoute: () => root,
  path: "/",
  validateSearch: (
    search: Record<string, unknown>,
  ): { workspace?: string; project?: string; environment?: string } => ({
    workspace:
      typeof search.workspace === "string" ? search.workspace : undefined,
    project: typeof search.project === "string" ? search.project : undefined,
    environment:
      typeof search.environment === "string" ? search.environment : undefined,
  }),
  component: () => (
    <Authenticated>
      <Dashboard />
    </Authenticated>
  ),
});
const servers = createRoute({
  getParentRoute: () => root,
  path: "/servers",
  component: () => (
    <Authenticated>
      <Servers />
    </Authenticated>
  ),
});
const alerts = createRoute({
  getParentRoute: () => root,
  path: "/alerts",
  component: () => (
    <Authenticated>
      <Alerts />
    </Authenticated>
  ),
});
const account = createRoute({
  getParentRoute: () => root,
  path: "/account",
  component: () => (
    <Authenticated>
      <Account />
    </Authenticated>
  ),
});
const members = createRoute({
  getParentRoute: () => root,
  path: "/members",
  component: () => (
    <Authenticated>
      <Members />
    </Authenticated>
  ),
});
const reset = createRoute({
  getParentRoute: () => root,
  path: "/reset",
  component: Reset,
});
const invitation = createRoute({
  getParentRoute: () => root,
  path: "/invitation",
  component: Invitation,
});
const signup = createRoute({
  getParentRoute: () => root,
  path: "/signup",
  component: Signup,
});
export const router = createRouter({
  routeTree: root.addChildren([
    dashboardRoute,
    servers,
    alerts,
    account,
    members,
    reset,
    invitation,
    signup,
  ]),
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
