import { useTranslation } from "react-i18next";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import * as s from "../styles";
import * as r from "./backup-recovery.styles";

// The warnings for a destination whose storage keys a revoked server was given
// (docs/backups.md#destination-setup).

/**
 * A persistent warning on a policy whose destination's keys a revoked server was given. A managed
 * destination's keys are revoked by management, whose exposure ends an hour after AWS confirms it.
 */
export function ExposedDestinationNotice({ managed }: { managed: boolean }) {
  const { t } = useTranslation("backup-recovery");
  return (
    <p className={s.notice}>
      {managed ? t(($) => $.exposed.managedPolicy) : t(($) => $.exposed.policy)}
    </p>
  );
}

/** The warning and acknowledgement for creating a policy on an exposed destination. */
export function ExposureAcknowledgement({ managed }: { managed: boolean }) {
  const { t } = useTranslation("backup-recovery");
  return (
    <>
      <p className={s.notice}>
        {managed
          ? t(($) => $.exposed.managedWarning)
          : t(($) => $.exposed.warning)}
      </p>
      <Label className={r.choice}>
        <Checkbox name="exposureAcknowledged" required />
        {t(($) => $.exposed.acknowledge)}
      </Label>
    </>
  );
}
