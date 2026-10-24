import { useQuery } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  Link,
  Navigate,
  Outlet,
  useLocation,
  useRouter,
} from "@tanstack/react-router";
import { type ReactNode, useContext, useEffect, useState } from "react";
import { client, result, setSession } from "./api/client";
import { sessionOptions } from "./api/session";
import logo from "./assets/astilba-lockup-horizontal-light.svg";
import { Alerts } from "./features/alerts";
import { hostingOptions, Signup } from "./hosting";
import { Account, Members } from "./security";
import { Servers } from "./servers";
import * as s from "./styles";
import { ThemeContext } from "./theme";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
import { Dashboard, type WorkspaceSelection } from "./workspace";

function AuthFrame({ children }: { children: ReactNode }) {
  return (
    <main id="main" className={s.authPage}>
      <div className={s.authContent}>
        <img
          src={logo}
          alt="Astilba"
          width="180"
          height="37"
          className={s.authLogo}
        />
        {children}
      </div>
    </main>
  );
}
function Root() {
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    const saved = localStorage.getItem("astilba-theme");
    return saved === "light" || saved === "dark"
      ? saved
      : matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
  });
  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem("astilba-theme", theme);
  }, [theme]);
  const session = useQuery(sessionOptions);
  return (
    <ThemeContext
      value={{
        theme,
        toggleTheme: () => setTheme(theme === "dark" ? "light" : "dark"),
      }}
    >
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      {session.isPending ? (
        <AuthFrame>
          <p role="status">Loading…</p>
        </AuthFrame>
      ) : session.error && !session.data ? (
        <AuthFrame>
          <ErrorNotice error={session.error} />
          <button
            type="button"
            disabled={session.isFetching}
            onClick={() => void session.refetch()}
          >
            Retry connection
          </button>
        </AuthFrame>
      ) : (
        <Outlet />
      )}
    </ThemeContext>
  );
}
function ApplicationLayout() {
  const sessionQuery = useQuery(sessionOptions);
  const session = sessionQuery.data;
  const { returnTo } = application.useRouteContext();
  const { theme, toggleTheme } = useContext(ThemeContext);
  const action = useAction();
  if (!session?.user || session.mfaPending) {
    return <Navigate to="/login" search={{ returnTo }} replace />;
  }
  return (
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
          <button type="button" onClick={toggleTheme}>
            Use {theme === "dark" ? "light" : "dark"} theme
          </button>
          <a href="/source/NOTICE.txt">Licence & source</a>
          <span className={s.muted}>{session.user.email}</span>
          <ErrorNotice error={action.error} />
          <button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                await result(client.POST("/api/v1/auth/logout"));
                window.location.assign("/login");
              })
            }
          >
            Sign out
          </button>
        </div>
      </aside>
      <main id="main" className={s.main}>
        <ErrorNotice error={sessionQuery.error} />
        <Outlet />
      </main>
    </div>
  );
}
function AuthLayout() {
  const location = useLocation();
  return (
    <AuthFrame>
      <Outlet />
      {location.pathname !== "/login" && (
        <Link to="/login">Back to sign in</Link>
      )}
    </AuthFrame>
  );
}
function loginDestination(value: unknown): string {
  if (typeof value !== "string" || !value.startsWith("/")) return "/";
  try {
    const url = new URL(value, window.location.origin);
    // Only return to application pages, never auth loops, API endpoints or other origins.
    if (
      url.origin !== window.location.origin ||
      !["/", "/servers", "/alerts", "/members", "/account"].includes(
        url.pathname.replace(/\/$/, "") || "/",
      )
    )
      return "/";
    return url.pathname + url.search + url.hash;
  } catch {
    return "/";
  }
}
function SignIn() {
  const session = useQuery(sessionOptions).data;
  const { returnTo } = loginRoute.useSearch();
  const router = useRouter();
  const signedIn = Boolean(session?.user && !session.mfaPending);
  useEffect(() => {
    if (signedIn) router.history.replace(returnTo ?? "/");
  }, [signedIn, router, returnTo]);
  const hosting = useQuery(hostingOptions).data;
  const action = useAction();
  if (signedIn) return <p role="status">Opening your workspace…</p>;
  return (
    <div className={s.stack}>
      <div>
        <h1>{session?.mfaPending ? "Verify it’s you" : "Sign in"}</h1>
        {session?.mfaPending && (
          <p className={s.muted}>
            Enter an authenticator or recovery code to continue.
          </p>
        )}
      </div>
      <section className={s.stack}>
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
            Password changed. <Link to="/login">Sign in</Link>
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
    <AuthFrame>
      <p>
        Page not found. <Link to="/">Return to projects</Link>
      </p>
    </AuthFrame>
  ),
});
const application = createRoute({
  getParentRoute: () => root,
  id: "application",
  beforeLoad: ({ location }) => ({ returnTo: location.href }),
  component: ApplicationLayout,
});
const auth = createRoute({
  getParentRoute: () => root,
  id: "auth",
  component: AuthLayout,
});
const loginRoute = createRoute({
  getParentRoute: () => auth,
  path: "/login",
  validateSearch: (search: Record<string, unknown>): { returnTo?: string } => ({
    returnTo:
      search.returnTo === undefined
        ? undefined
        : loginDestination(search.returnTo),
  }),
  component: SignIn,
});
const dashboardRoute = createRoute({
  getParentRoute: () => application,
  path: "/",
  validateSearch: (search: Record<string, unknown>): WorkspaceSelection => ({
    workspace:
      typeof search.workspace === "string" ? search.workspace : undefined,
    project: typeof search.project === "string" ? search.project : undefined,
    environment:
      typeof search.environment === "string" ? search.environment : undefined,
  }),
  component: DashboardPage,
});
function DashboardPage() {
  const selection = dashboardRoute.useSearch();
  const navigate = dashboardRoute.useNavigate();
  return (
    <Dashboard
      selection={selection}
      onSelectionChange={(search) => navigate({ search })}
    />
  );
}
const servers = createRoute({
  getParentRoute: () => application,
  path: "/servers",
  component: Servers,
});
const alerts = createRoute({
  getParentRoute: () => application,
  path: "/alerts",
  component: Alerts,
});
const account = createRoute({
  getParentRoute: () => application,
  path: "/account",
  component: Account,
});
const members = createRoute({
  getParentRoute: () => application,
  path: "/members",
  component: Members,
});
const reset = createRoute({
  getParentRoute: () => auth,
  path: "/reset",
  component: Reset,
});
const invitation = createRoute({
  getParentRoute: () => auth,
  path: "/invitation",
  component: Invitation,
});
const signup = createRoute({
  getParentRoute: () => auth,
  path: "/signup",
  component: Signup,
});
export const router = createRouter({
  routeTree: root.addChildren([
    application.addChildren([
      dashboardRoute,
      servers,
      alerts,
      account,
      members,
    ]),
    auth.addChildren([loginRoute, reset, invitation, signup]),
  ]),
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
