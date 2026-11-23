import { useQuery } from "@tanstack/react-query";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { cx } from "../../../styled-system/css";
import type { Schema } from "../../api/client";
import { ButtonLink } from "../../components/button-link";
import { hostingOptions } from "../../hosting";
import * as s from "../../styles";
import * as styles from "./mfa-setup-notice.styles";

// MfaSetupNotice guides an owner or operator who hasn't set up two-factor authentication to do
// so, while the workspace refuses their changes without it: one that requires it, as new
// workspaces do, and every workspace on a hosted installation. Until then they can still read
// it. A viewer changes nothing either way, so isn't told otherwise. Owners continue in workspace
// setup, which on a hosted installation is only for a workspace they own; anyone else goes to the
// two-factor section of account security.
export function MfaSetupNotice({
  user,
  workspace,
}: {
  user: Schema["Account"];
  workspace: Schema["Workspace"];
}) {
  const { t } = useTranslation("app");
  const id = useId();
  const hosted = useQuery(hostingOptions).data?.enabled;
  if (
    user.mfaEnabled ||
    workspace.role === "viewer" ||
    !(workspace.requireMfa || hosted)
  )
    return null;
  const action = t(($) => $.mfaSetup.action);
  return (
    <section className={cx(s.notice, styles.notice)} aria-labelledby={id}>
      <p id={id} className={styles.title}>
        {t(($) => $.mfaSetup.title)}
      </p>
      <p>{t(($) => $.mfaSetup.description)}</p>
      {workspace.role === "owner" ? (
        <ButtonLink
          to="/onboarding"
          search={{ workspace: workspace.id }}
          size="sm"
        >
          {action}
        </ButtonLink>
      ) : (
        <ButtonLink
          to="/account"
          search={{ workspace: workspace.id }}
          hash="account-mfa"
          size="sm"
        >
          {action}
        </ButtonLink>
      )}
    </section>
  );
}
