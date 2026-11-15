import { cx } from "../../styled-system/css";
import type { Schema } from "../api/client";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../components/alert-dialog";
import { Button } from "../components/button";
import * as s from "../styles";
import * as styles from "./mfa-requirement.styles";

type Approval = Schema["MfaRequirementApproval"];

// What each kind of approval does, as the owner reviewing them would name it.
const kinds = {
  gitSource: () => "Automatic imports and deployments from Git",
  buildTarget: (a) =>
    `Builds of ${a.subject || "an application"}, and deployments if it deploys`,
  automaticDeployment: () => "An automatic deployment waiting to start",
  previewPolicy: () => "Previews",
  previewApproval: (a) => `Preview approval for pull request ${a.subject}`,
  previewCleanup: (a) => `Preview cleanup for pull request ${a.subject}`,
  invitation: (a) => `Invitation for ${a.subject}`,
  enrollmentCode: (a) => `Enrollment code for server ${a.subject}`,
  sourceSubmission: () => "A queued source submission",
  automationCredential: (a) =>
    `Changes by automation credential ${a.subject}, which can still read`,
} satisfies Record<Approval["kind"], (a: Approval) => string>;

// A deferred action that comes due while the requirement is on is refused, not paused.
const oneShot = new Set<Approval["kind"]>([
  "automaticDeployment",
  "sourceSubmission",
]);

function describe(a: Approval, requiring: boolean) {
  const what = kinds[a.kind](a);
  const where =
    a.projectName && a.environmentName
      ? `${what} in ${a.projectName} / ${a.environmentName}`
      : what;
  return requiring && oneShot.has(a.kind) ? `${where} (refused)` : where;
}

// byApprover groups approvals by the member who gave them, in the order the server listed them.
function byApprover(approvals: Approval[]) {
  const groups = new Map<string, { approver: Approval; items: Approval[] }>();
  for (const a of approvals) {
    const group = groups.get(a.approverId);
    if (group) group.items.push(a);
    else groups.set(a.approverId, { approver: a, items: [a] });
  }
  return [...groups.values()];
}

// MfaRequirementConfirmation asks an owner to confirm changing the two-factor requirement when
// that changes what members without two-factor authentication approved, and lists it. Requiring
// it stops their standing approvals and refuses their deferred actions that come due meanwhile;
// no longer requiring it lets their standing approvals act again. Setting up two-factor
// authentication ends what a member approved without it, so it is approved again afterwards.
export function MfaRequirementConfirmation({
  requiring,
  approvals,
  open,
  onOpenChange,
  onOpenChangeComplete,
  onConfirm,
  finalFocus,
}: {
  requiring: boolean;
  approvals: Approval[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onOpenChangeComplete: (open: boolean) => void;
  onConfirm: () => void;
  finalFocus: () => HTMLElement | null;
}) {
  return (
    <AlertDialogRoot
      open={open}
      onOpenChange={onOpenChange}
      onOpenChangeComplete={onOpenChangeComplete}
    >
      <AlertDialogContent finalFocus={finalFocus}>
        <AlertDialogTitle>
          {requiring
            ? "Require two-factor authentication?"
            : "Stop requiring two-factor authentication?"}
        </AlertDialogTitle>
        <AlertDialogDescription>
          {requiring
            ? "These members haven’t set up two-factor authentication, so what they approved stops while it’s required. Anything marked refused is turned down if it comes due meanwhile. Once a member sets it up, they or another owner can approve it again."
            : "These members haven’t set up two-factor authentication. What they approved acts again once it’s no longer required."}
        </AlertDialogDescription>
        <div className={cx(s.stack, styles.approvals)}>
          {byApprover(approvals).map(({ approver, items }) => (
            <section key={approver.approverId}>
              <h3>
                {approver.approverName}{" "}
                <span className={s.muted}>{approver.approverEmail}</span>
              </h3>
              <ul>
                {items.map((a, i) => (
                  <li
                    key={`${a.kind}-${a.environmentId ?? ""}-${a.subject}-${i}`}
                  >
                    {describe(a, requiring)}
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
        <AlertDialogActions>
          <AlertDialogClose render={<Button>Cancel</Button>} />
          <Button
            variant={requiring ? "danger" : "primary"}
            onClick={() => {
              onOpenChange(false);
              onConfirm();
            }}
          >
            {requiring ? "Require it and stop these" : "Stop requiring it"}
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
