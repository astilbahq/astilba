import { useTranslation } from "react-i18next";
import type { EnvironmentPath } from "../../api/environment";
import * as s from "../../styles";
import { OpenSessions } from "./open-sessions";
import { SessionSetting } from "./session-setting";
import * as styles from "./troubleshooting.styles";

export function TerminalSessions({
  path,
  owner,
}: {
  path: EnvironmentPath;
  owner: boolean;
}) {
  const { t } = useTranslation("troubleshooting");
  return (
    <section className={s.panel} aria-label={t(($) => $.title)}>
      <div className={styles.intro}>
        <h2>{t(($) => $.title)}</h2>
        <p className={s.muted}>{t(($) => $.description)}</p>
      </div>
      <div className={styles.parts}>
        {owner && <SessionSetting path={path} />}
        <OpenSessions path={path} owner={owner} />
      </div>
    </section>
  );
}
