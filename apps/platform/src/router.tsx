import { useQuery } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  Navigate,
  Outlet,
  useLocation,
} from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { Schema } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import { Anchor, Link } from "./components/link";
import { ErrorToast, ToastProvider } from "./components/toast";
import { TooltipProvider } from "./components/tooltip";
import { Account } from "./features/account/account";
import { Alerts } from "./features/alerts";
import { AuthFrame } from "./features/auth/auth-frame";
import { VerifyEmailPage } from "./features/auth/email-verification";
import { HostedOnboarding } from "./features/auth/hosted-onboarding";
import { Invitation } from "./features/auth/invitation";
import { PasswordReset } from "./features/auth/password-reset";
import { SignIn } from "./features/auth/sign-in";
import { Signup } from "./features/auth/signup";
import { GitHubCallback, GitHubInstalled } from "./features/github-app";
import {
  AppShell,
  type AppSection,
  WorkspaceUnavailable,
} from "./features/shell/app-shell";
import { usePageTitle } from "./i18n/use-page-title";
import { Members } from "./security";
import { Servers } from "./servers";
import { ThemeProvider } from "./theme";
import { ErrorNotice } from "./ui";
import { Dashboard, type WorkspaceSelection } from "./workspace";
import { WorkspaceContext } from "./workspace-context";

function Root() {
  const { t } = useTranslation("common");
  const session = useQuery(sessionOptions);
  return (
    <ThemeProvider>
      <ToastProvider>
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
              <h1>{t(($) => $.connectionUnavailable)}</h1>
              <ErrorToast
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
      </ToastProvider>
    </ThemeProvider>
  );
}
function ApplicationLayout() {
  const sessionQuery = useQuery(sessionOptions);
  const session = sessionQuery.data;
  const { returnTo } = application.useRouteContext();
  if (!session?.user || session.mfaPending)
    return <Navigate to="/login" search={{ returnTo }} replace />;
  return (
    <SignedInLayout
      user={session.user}
      workspaces={session.workspaces}
      error={sessionQuery.error}
    />
  );
}
function SignedInLayout({
  user,
  workspaces,
  error,
}: {
  user: Schema["Account"];
  workspaces: readonly Schema["Workspace"][];
  error: unknown;
}) {
  const search = application.useSearch();
  const navigate = application.useNavigate();
  const location = useLocation();
  // OAuth authorization owns its workspace; switching context would reset its selection.
  if (location.pathname === "/github/callback")
    return (
      <AuthFrame key={user.id}>
        <Outlet />
      </AuthFrame>
    );
  const workspace =
    search.workspace === undefined
      ? workspaces[0]
      : workspaces.find((item) => item.id === search.workspace);
  const section = location.pathname as AppSection;
  const unavailable =
    search.workspace !== undefined && !workspace && section !== "/account";
  return (
    <WorkspaceContext value={workspace}>
      <AppShell
        key={user.id}
        user={user}
        workspaces={workspaces}
        workspace={workspace}
        selectedWorkspaceId={search.workspace}
        section={section}
        navigationKey={location.href}
        onWorkspaceChange={(id) => {
          void navigate({ to: section, search: { workspace: id } });
        }}
        onSignedOut={() => window.location.assign("/login")}
      >
        <ErrorNotice error={error} />
        <div
          key={section === "/account" ? "account" : (workspace?.id ?? "none")}
        >
          {unavailable ? <WorkspaceUnavailable /> : <Outlet />}
        </div>
      </AppShell>
    </WorkspaceContext>
  );
}
function AuthLayout() {
  return (
    <AuthFrame>
      <Outlet />
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
        "/signup",
        "/onboarding",
        "/verify-email",
        "/github/callback",
      ].includes(url.pathname.replace(/\/$/, "") || "/")
    )
      return "/";
    return url.pathname + url.search + url.hash;
  } catch {
    return "/";
  }
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
  validateSearch: (
    search: Record<string, unknown>,
  ): { workspace?: string } => ({
    workspace:
      typeof search.workspace === "string" ? search.workspace : undefined,
  }),
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
  component: SignInPage,
});
function SignInPage() {
  const { returnTo } = loginRoute.useSearch();
  return <SignIn returnTo={returnTo} />;
}
const dashboardRoute = createRoute({
  getParentRoute: () => application,
  path: "/",
  validateSearch: (
    search: Record<string, unknown>,
  ): Omit<WorkspaceSelection, "workspace"> => ({
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
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token:
      typeof search.token === "string" && search.token
        ? search.token
        : undefined,
  }),
  component: SignupPage,
});
function SignupPage() {
  const { token } = signup.useSearch();
  const navigate = signup.useNavigate();
  return (
    <Signup
      key={token ?? "missing"}
      token={token}
      onCreated={(workspace) =>
        void navigate({
          to: "/onboarding",
          search: { workspace },
          replace: true,
        })
      }
    />
  );
}
const onboarding = createRoute({
  getParentRoute: () => auth,
  path: "/onboarding",
  validateSearch: (
    search: Record<string, unknown>,
  ): { workspace?: string; billing?: "returned" | "cancelled" } => ({
    workspace:
      typeof search.workspace === "string" && search.workspace
        ? search.workspace
        : undefined,
    billing:
      search.billing === "returned" || search.billing === "cancelled"
        ? search.billing
        : undefined,
  }),
  component: OnboardingPage,
});
function OnboardingPage() {
  const { workspace, billing } = onboarding.useSearch();
  return <HostedOnboarding workspaceId={workspace} billing={billing} />;
}
const verifyEmail = createRoute({
  getParentRoute: () => auth,
  path: "/verify-email",
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token:
      typeof search.token === "string" && search.token
        ? search.token
        : undefined,
  }),
  component: EmailVerificationRoute,
});
function EmailVerificationRoute() {
  const { token } = verifyEmail.useSearch();
  return <VerifyEmailPage token={token} />;
}
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
    auth.addChildren([
      loginRoute,
      reset,
      invitation,
      signup,
      onboarding,
      verifyEmail,
      githubInstalled,
    ]),
  ]),
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
