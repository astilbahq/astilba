import { useQuery } from "@tanstack/react-query";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { cx } from "../../../styled-system/css";
import type { Schema } from "../../api/client";
import { ButtonLink } from "../../components/button-link";
import { hostingOptions } from "../../hosting";
import * as s from "../../styles";
import * as styles from "./mfa-setup-notice.styles";

// MfaSetupNotice guides someone who hasn't set up two-factor authentication to do so, while the
// workspace refuses their changes without it: one that requires it, as new workspaces do, and
// every workspace on a hosted installation. Until then they can still read it.
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
  if (user.mfaEnabled || !(workspace.requireMfa || hosted)) return null;
  return (
    <section className={cx(s.notice, styles.notice)} aria-labelledby={id}>
      <p id={id} className={styles.title}>
        {t(($) => $.mfaSetup.title)}
      </p>
      <p>{t(($) => $.mfaSetup.description)}</p>
      <ButtonLink
        to="/onboarding"
        search={{ workspace: workspace.id }}
        size="sm"
      >
        {t(($) => $.mfaSetup.action)}
      </ButtonLink>
    </section>
  );
}
