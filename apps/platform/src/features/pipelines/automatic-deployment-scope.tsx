import * as s from "../../styles";

/** What an owner's automatic deployment policy covers, shown wherever it can be turned on. */
export function AutomaticDeploymentScope() {
  return (
    <p className={s.muted}>
      Each automatic deployment ships only this run's change. It stops for
      review if anything else in the draft changed, if the change adds host
      permissions or alters a service with a volume, privileged mode or added
      Linux capabilities, or if another service's image tag now points at a
      different image than the last reviewed deployment ran. Pinning images by
      digest keeps their tags from moving under later deployments.
    </p>
  );
}
