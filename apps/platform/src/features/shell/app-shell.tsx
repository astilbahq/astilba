import { Dialog } from "@base-ui/react/dialog";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { appShell } from "../../../styled-system/recipes";
import { client, result, type Schema } from "../../api/client";
import { useSessionRequest } from "../../api/use-session-request";
import { Button } from "../../components/button";
import { IconButton } from "../../components/icon-button";
import { Icon } from "../../components/icons";
import { Anchor, Link } from "../../components/link";
import {
  MenuContent,
  MenuItem,
  MenuLinkItem,
  MenuRoot,
  MenuTrigger,
} from "../../components/menu";
import { ThemeMenu } from "../../components/theme-menu";
import { ErrorToast } from "../../components/toast";
import { useAction } from "../../ui";

export type AppSection = "/" | "/servers" | "/alerts" | "/members" | "/account";
const paths = {
  projects: <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v10H3Z" />,
  servers: (
    <>
      <rect x="3" y="3" width="18" height="7" rx="2" />
      <rect x="3" y="14" width="18" height="7" rx="2" />
      <path d="M7 6.5h.01M7 17.5h.01" />
    </>
  ),
  alerts: (
    <>
      <path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" />
    </>
  ),
  workspace: (
    <>
      <rect x="4" y="3" width="16" height="18" rx="2" />
      <path d="M8 7h2m4 0h2M8 11h2m4 0h2M10 21v-5h4v5" />
    </>
  ),
};
function Chevron() {
  return (
    <Icon>
      <path d="m8 10 4 4 4-4" />
    </Icon>
  );
}

export function AppShell({
  user,
  workspaces,
  workspace,
  selectedWorkspaceId,
  section,
  navigationKey,
  onWorkspaceChange,
  onSignedOut,
  children,
}: {
  user: Schema["Account"];
  workspaces: readonly Schema["Workspace"][];
  workspace?: Schema["Workspace"];
  selectedWorkspaceId?: string;
  section: AppSection;
  navigationKey: string;
  onWorkspaceChange: (id: string) => void;
  onSignedOut: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation("app");
  const common = useTranslation("common").t;
  const styles = appShell();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const main = useRef<HTMLElement>(null);
  const previousNavigation = useRef(navigationKey);
  const focusAfterClose = useRef(false);
  const logout = useAction();
  const request = useSessionRequest();
  useEffect(() => {
    if (previousNavigation.current === navigationKey) return;
    previousNavigation.current = navigationKey;
    if (open) {
      focusAfterClose.current = true;
      setOpen(false);
    } else main.current?.focus();
  }, [navigationKey, open]);
  const search = { workspace: selectedWorkspaceId ?? workspace?.id };
  const navigation = [
    { to: "/", label: t(($) => $.navigation.projects), icon: paths.projects },
    {
      to: "/servers",
      label: t(($) => $.navigation.servers),
      icon: paths.servers,
    },
    { to: "/alerts", label: t(($) => $.navigation.alerts), icon: paths.alerts },
    {
      to: "/members",
      label: t(($) => $.navigation.workspace),
      icon: paths.workspace,
    },
  ] as const;
  const workspacePicker = () => (
    <MenuRoot>
      <MenuTrigger
        render={
          <Button
            variant="ghost"
            className={styles.workspaceButton}
            aria-label={t(($) => $.workspace.choose, {
              name: workspace?.name ?? t(($) => $.workspace.none),
            })}
          />
        }
      >
        <span className={styles.text}>
          <bdi>{workspace?.name ?? t(($) => $.workspace.none)}</bdi>
        </span>
        <Chevron />
      </MenuTrigger>
      <MenuContent
        aria-label={t(($) => $.workspace.menu)}
        align="start"
        className={styles.workspaceMenu}
      >
        {workspaces.length ? (
          workspaces.map((item) => (
            <MenuItem
              key={item.id}
              onClick={() => {
                if (item.id !== workspace?.id) onWorkspaceChange(item.id);
              }}
              aria-current={item.id === workspace?.id ? "true" : undefined}
            >
              <span className={styles.text}>
                <bdi>{item.name}</bdi>
              </span>
              {item.id === workspace?.id && (
                <Icon>
                  <path d="m5 12 4 4 10-10" />
                </Icon>
              )}
            </MenuItem>
          ))
        ) : (
          <MenuItem disabled>{t(($) => $.workspace.empty)}</MenuItem>
        )}
      </MenuContent>
    </MenuRoot>
  );
  const nav = () => (
    <nav
      className={styles.navigation}
      aria-label={t(($) => $.navigation.label)}
    >
      {navigation.map((item) => (
        <Link
          key={item.to}
          to={item.to}
          search={search}
          variant="navigation"
          className={styles.navigationLink}
          activeOptions={{ exact: true, includeSearch: false }}
          aria-current={section === item.to ? "page" : undefined}
        >
          <Icon>{item.icon}</Icon>
          {item.label}
        </Link>
      ))}
    </nav>
  );
  const footer = () => (
    <div className={styles.footer}>
      <MenuRoot>
        <MenuTrigger
          render={
            <Button
              variant="ghost"
              className={styles.accountButton}
              disabled={logout.isPending}
              aria-label={t(($) => $.account.menu, {
                name: user.name || user.email,
              })}
            />
          }
        >
          <Icon>
            <circle cx="12" cy="8" r="4" />
            <path d="M4 21a8 8 0 0 1 16 0" />
          </Icon>
          <span className={styles.text}>
            <bdi>{user.name || user.email}</bdi>
          </span>
          <Chevron />
        </MenuTrigger>
        <MenuContent
          aria-label={t(($) => $.account.label)}
          side="top"
          align="start"
        >
          <MenuLinkItem render={<Link to="/account" search={search} />}>
            {t(($) => $.navigation.account)}
          </MenuLinkItem>
          <MenuLinkItem
            render={
              <Anchor
                href="/source/NOTICE.txt"
                aria-label={t(($) => $.account.source)}
              />
            }
          >
            {t(($) => $.account.source)}
          </MenuLinkItem>
          <MenuItem
            disabled={logout.isPending}
            onClick={() => {
              setOpen(false);
              logout.run(() =>
                request(
                  (signal) =>
                    result(client.POST("/api/v1/auth/logout", { signal })),
                  onSignedOut,
                ),
              );
            }}
          >
            {t(($) => $.account.signOut)}
          </MenuItem>
        </MenuContent>
      </MenuRoot>
      <div className={styles.drawerHeader}>
        <span className={styles.secondary}>
          <bdi>{user.email}</bdi>
        </span>
        <ThemeMenu />
      </div>
    </div>
  );
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(visible) => {
        if (visible) focusAfterClose.current = false;
        if (!visible && !trigger.current?.getClientRects().length)
          focusAfterClose.current = true;
        setOpen(visible);
      }}
      onOpenChangeComplete={(visible) => {
        if (!visible && focusAfterClose.current) {
          main.current?.focus();
        }
      }}
    >
      <div className={styles.root}>
        <aside
          className={styles.sidebar}
          aria-label={t(($) => $.navigation.region)}
        >
          <Link to="/" search={search} variant="plain" className={styles.brand}>
            {t(($) => $.name)}
          </Link>
          {workspacePicker()}
          {nav()}
          {footer()}
        </aside>
        <div>
          <header className={styles.mobileHeader}>
            <Dialog.Trigger
              render={
                <IconButton
                  ref={trigger}
                  aria-label={t(($) => $.navigation.open)}
                />
              }
            >
              <Icon>
                <path d="M4 6h16M4 12h16M4 18h16" />
              </Icon>
            </Dialog.Trigger>
            <span className={styles.text}>
              <bdi>{workspace?.name ?? t(($) => $.name)}</bdi>
            </span>
            <ThemeMenu />
          </header>
          <main id="main" ref={main} tabIndex={-1} className={styles.main}>
            <ErrorToast
              error={logout.error}
              message={common(($) => $.requestFailed)}
            />
            {children}
          </main>
        </div>
      </div>
      <Dialog.Portal className={styles.drawerPortal}>
        <Dialog.Backdrop className={styles.backdrop} />
        <Dialog.Viewport className={styles.drawerViewport}>
          <Dialog.Popup
            className={styles.drawer}
            finalFocus={() =>
              focusAfterClose.current ? false : trigger.current
            }
          >
            <div className={styles.drawerHeader}>
              <Dialog.Title className={styles.drawerTitle}>
                {t(($) => $.navigation.label)}
              </Dialog.Title>
              <Dialog.Close
                render={
                  <IconButton aria-label={t(($) => $.navigation.close)} />
                }
              >
                <Icon>
                  <path d="m6 6 12 12M6 18 18 6" />
                </Icon>
              </Dialog.Close>
            </div>
            {workspacePicker()}
            {nav()}
            {footer()}
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function WorkspaceUnavailable() {
  const { t } = useTranslation("app");
  const common = useTranslation("common").t;
  return (
    <>
      <h1>{t(($) => $.workspace.unavailableTitle)}</h1>
      <p>{t(($) => $.workspace.unavailable)}</p>
      <Link
        to="/"
        search={{
          workspace: undefined,
          project: undefined,
          environment: undefined,
        }}
      >
        {common(($) => $.returnToProjects)}
      </Link>
    </>
  );
}
