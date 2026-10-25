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
import { useTranslation } from "react-i18next";
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
import { MFAChallenge } from "./features/auth/mfa-challenge";
import { PasswordReset } from "./features/auth/password-reset";
import { useAuthValidation } from "./features/auth/use-auth-validation";
import { GitHubCallback, GitHubInstalled } from "./features/github-app";
import { hostingOptions, Signup } from "./hosting";
import { usePageTitle } from "./i18n/use-page-title";
import { Account, Members } from "./security";
import { Servers } from "./servers";
import * as s from "./styles";
import { ThemeProvider } from "./theme";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
import { Dashboard, type WorkspaceSelection } from "./workspace";

function Root() {
  const { t } = useTranslation("common");
  const session = useQuery(sessionOptions);
  return (
    <ThemeProvider>
      <TooltipProvider delay={300}>
        <Anchor href="#main" className="skip-link">
          {t(($) => $.skipContent)}
        </Anchor>
        {session.isPending ? (
          <AuthFrame>
            <p role="status">{t(($) => $.loading)}</p>
          </AuthFrame>
        ) : session.error && !session.data ? (
          <AuthFrame>
            <ErrorNotice
              error={session.error}
              message={t(($) => $.requestFailed)}
            />
            <Button
              type="button"
              disabled={session.isFetching}
              onClick={() => void session.refetch()}
            >
              {t(($) => $.retryConnection)}
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
  const { t } = useTranslation("auth");
  const location = useLocation();
  return (
    <AuthFrame>
      <Outlet />
      {location.pathname === "/signup" && (
        <Link to="/login" className={authStyles.backLink}>
          {t(($) => $.backToSignIn)}
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
        "/github/callback",
      ].includes(url.pathname.replace(/\/$/, "") || "/")
    )
      return "/";
    return url.pathname + url.search + url.hash;
  } catch {
    return "/";
  }
}
function SignIn() {
  const { t } = useTranslation("auth");
  const authValidation = useAuthValidation();
  const session = useQuery(sessionOptions).data;
  const { returnTo } = loginRoute.useSearch();
  const router = useRouter();
  const signedIn = Boolean(session?.user && !session.mfaPending);
  usePageTitle(
    session?.mfaPending
      ? t(($) => $.mfa.pageTitle)
      : t(($) => $.signIn.pageTitle),
  );
  useEffect(() => {
    if (signedIn) router.history.replace(returnTo ?? "/");
  }, [signedIn, router, returnTo]);
  const hosting = useQuery(hostingOptions).data;
  const action = useAction();
  if (session?.mfaPending) return <MFAChallenge />;
  if (signedIn) return <p role="status">{t(($) => $.signIn.opening)}</p>;
  return (
    <div className={authStyles.flow}>
      <h1>{t(($) => $.signIn.title)}</h1>
      <section className={s.stack}>
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
            label={t(($) => $.signIn.email)}
            type="email"
            autoComplete="username"
            placeholder={t(($) => $.signIn.emailPlaceholder)}
            variant="quiet"
          />
          <Field
            name="password"
            label={t(($) => $.signIn.password)}
            type="password"
            autoComplete="current-password"
            inputComponent={PasswordInput}
            placeholder={t(($) => $.signIn.passwordPlaceholder)}
            variant="quiet"
            labelAction={
              <Link
                to="/reset"
                variant="plain"
                className={authStyles.recoveryLink}
              >
                {t(($) => $.signIn.forgotPassword)}
              </Link>
            }
          />
          <BusyButton busy={action.isPending}>
            {t(($) => $.signIn.submit)}
          </BusyButton>
        </Form>
        {hosting?.enabled && (
          <div className={authStyles.secondaryActions}>
            <Link to="/signup" className={authStyles.secondaryLink}>
              {t(($) => $.signIn.hostedBeta)}
            </Link>
          </div>
        )}
      </section>
    </div>
  );
}
function NotFound() {
  const { t } = useTranslation("common");
  usePageTitle(t(($) => $.titles.notFound));
  return (
    <AuthFrame>
      <p>{t(($) => $.notFound)}</p>
      <Link to="/">{t(($) => $.returnToProjects)}</Link>
    </AuthFrame>
  );
}
const root = createRootRoute({
  component: Root,
  notFoundComponent: NotFound,
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
const githubCallback = createRoute({
  getParentRoute: () => application,
  path: "/github/callback",
  component: GitHubCallback,
});
const githubInstalled = createRoute({
  getParentRoute: () => auth,
  path: "/github/installed",
  component: GitHubInstalled,
});
export const router = createRouter({
  routeTree: root.addChildren([
    application.addChildren([
      githubCallback,
      dashboardRoute,
      servers,
      alerts,
      account,
      members,
    ]),
    auth.addChildren([loginRoute, reset, invitation, signup, githubInstalled]),
  ]),
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
