import { useQuery } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  Navigate,
  Outlet,
  useLocation,
  useRouter,
} from "@tanstack/react-router";
import { useEffect } from "react";
import { client, result, setSession } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import { Anchor, Link } from "./components/link";
import { PasswordInput } from "./components/password-input";
import { ThemeMenu } from "./components/theme-menu";
import { TooltipProvider } from "./components/tooltip";
import { Alerts } from "./features/alerts";
import { AuthFrame } from "./features/auth/auth-frame";
import * as authStyles from "./features/auth/auth.styles";
import { Invitation } from "./features/auth/invitation";
import { PasswordReset } from "./features/auth/password-reset";
import { authValidation } from "./features/auth/validation";
import { hostingOptions, Signup } from "./hosting";
import { Account, Members } from "./security";
import { Servers } from "./servers";
import * as s from "./styles";
import { ThemeProvider } from "./theme";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
import { Dashboard, type WorkspaceSelection } from "./workspace";

function Root() {
  const session = useQuery(sessionOptions);
  return (
    <ThemeProvider>
      <TooltipProvider delay={300}>
        <Anchor href="#main" className="skip-link">
          Skip to content
        </Anchor>
        {session.isPending ? (
          <AuthFrame>
            <p role="status">Loading…</p>
          </AuthFrame>
        ) : session.error && !session.data ? (
          <AuthFrame>
            <ErrorNotice error={session.error} />
            <Button
              type="button"
              disabled={session.isFetching}
              onClick={() => void session.refetch()}
            >
              Retry connection
            </Button>
          </AuthFrame>
        ) : (
          <Outlet />
        )}
      </TooltipProvider>
    </ThemeProvider>
  );
}
function ApplicationLayout() {
  const sessionQuery = useQuery(sessionOptions);
  const session = sessionQuery.data;
  const { returnTo } = application.useRouteContext();
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
            <Link to="/" variant="navigation">
              Projects
            </Link>
            <Link to="/servers" variant="navigation">
              Servers
            </Link>
            <Link to="/alerts" variant="navigation">
              Alerts
            </Link>
            <Link to="/members" variant="navigation">
              Workspace
            </Link>
            <Link to="/account" variant="navigation">
              Account security
            </Link>
          </nav>
        </div>
        <div className={s.stack}>
          <small>Open source. On your terms.</small>
          <Anchor href="/source/NOTICE.txt">Licence & source</Anchor>
          <span className={s.muted}>{session.user.email}</span>
          <ErrorNotice error={action.error} />
          <Button
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
          </Button>
        </div>
      </aside>
      <main id="main" className={s.main}>
        <div className={s.appearance}>
          <ThemeMenu />
        </div>
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
      {location.pathname === "/signup" && (
        <Link to="/login" className={authStyles.backLink}>
          Back to sign in
        </Link>
      )}
    </AuthFrame>
  );
}
function loginDestination(value: unknown): string {
  if (typeof value !== "string" || !value.startsWith("/")) return "/";
  try {
    const url = new URL(value, window.location.origin);
    // Return only to application pages or an invitation, never sign-in loops, APIs or other origins.
    if (
      url.origin !== window.location.origin ||
      ![
        "/",
        "/servers",
        "/alerts",
        "/members",
        "/account",
        "/invitation",
      ].includes(url.pathname.replace(/\/$/, "") || "/")
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
    <div className={authStyles.flow}>
      <div>
        <h1>{session?.mfaPending ? "Verify it’s you" : "Sign in"}</h1>
        {session?.mfaPending && (
          <p className={s.muted}>
            Enter an authenticator or recovery code to continue.
          </p>
        )}
      </div>
      <section className={s.stack}>
        {session?.mfaPending ? (
          <Form
            action={action}
            validation={authValidation.mfa}
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
            validation={authValidation.signIn}
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
              placeholder="you@example.com"
              variant="quiet"
            />
            <Field
              name="password"
              label="Password"
              type="password"
              autoComplete="current-password"
              inputComponent={PasswordInput}
              placeholder="Enter your password"
              variant="quiet"
              labelAction={
                <Link
                  to="/reset"
                  variant="plain"
                  className={authStyles.recoveryLink}
                >
                  Forgot password?
                </Link>
              }
            />
            <BusyButton busy={action.isPending}>Sign in</BusyButton>
          </Form>
        )}
        {(session?.mfaPending || hosting?.enabled) && (
          <div className={authStyles.secondaryActions}>
            {session?.mfaPending && (
              <Link to="/reset" className={authStyles.secondaryLink}>
                Reset password
              </Link>
            )}
            {hosting?.enabled && (
              <Link to="/signup" className={authStyles.secondaryLink}>
                Join the hosted beta
              </Link>
            )}
          </div>
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
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token:
      typeof search.token === "string" && search.token
        ? search.token
        : undefined,
  }),
  component: PasswordResetPage,
});
function PasswordResetPage() {
  const { token } = reset.useSearch();
  return <PasswordReset key={token ?? "request"} token={token} />;
}
const invitation = createRoute({
  getParentRoute: () => auth,
  path: "/invitation",
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token:
      typeof search.token === "string" && search.token
        ? search.token
        : undefined,
  }),
  component: InvitationPage,
});
function InvitationPage() {
  const { token } = invitation.useSearch();
  return <Invitation key={token ?? "missing"} token={token} />;
}
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
