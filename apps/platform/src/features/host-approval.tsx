import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import * as s from "../styles";

/** An owner's approval of a plan's host permissions, and how far automatic deployment carries it. */
export function HostApproval() {
  return (
    <>
      <Label className={s.row}>
        <Checkbox name="host" />
        Approve the host permissions listed in the plan, including proxy, ports,
        certificates and elevated container privileges
      </Label>
      <p className={s.muted}>
        Automatic deployments carry approved published ports forward. A
        privileged or added-capability service is carried forward only while its
        definition and image digest stay exactly as approved here; any other
        change to it stops for review.
      </p>
    </>
  );
}
