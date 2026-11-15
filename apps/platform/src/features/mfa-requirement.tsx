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

type Approval = Schema["MfaRequirementApproval"];

// What each kind of approval does, as the owner reviewing them would name it.
const kinds = {
  gitSource: () => "Automatic imports and deployments from Git",
  buildTarget: (a) => `Automatic builds of ${a.subject || "an application"}`,
  automaticDeployment: () => "An automatic deployment not yet planned",
  previewPolicy: () => "Previews",
  previewApproval: (a) => `Preview approval for pull request ${a.subject}`,
  previewCleanup: (a) => `Preview cleanup for pull request ${a.subject}`,
  invitation: (a) => `Invitation for ${a.subject}`,
  enrollmentCode: (a) => `Enrollment code for server ${a.subject}`,
  sourceSubmission: () => "A queued source submission",
  automationCredential: (a) =>
    `Automation credential ${a.subject}, which can still read`,
} satisfies Record<Approval["kind"], (a: Approval) => string>;

function describe(a: Approval) {
  const what = kinds[a.kind](a);
  return a.projectName && a.environmentName
    ? `${what} in ${a.projectName} / ${a.environmentName}`
    : what;
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

// MfaRequirementConfirmation asks an owner to confirm requiring two-factor authentication when
// that would stop approvals given by members who haven't set it up, and lists them. They resume
// if the requirement is turned off; a member who sets up two-factor authentication approves
// again, since setting it up ends what they approved without it.
export function MfaRequirementConfirmation({
  approvals,
  open,
  onOpenChange,
  onConfirm,
}: {
  approvals: Approval[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  return (
    <AlertDialogRoot open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogTitle>Require two-factor authentication?</AlertDialogTitle>
        <AlertDialogDescription>
          These members haven’t set up two-factor authentication, so what they
          approved stops until they approve it again after setting it up, or
          until the requirement is turned off.
        </AlertDialogDescription>
        <div className={s.stack}>
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
                    {describe(a)}
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
        <AlertDialogActions>
          <AlertDialogClose render={<Button>Cancel</Button>} />
          <Button
            variant="danger"
            onClick={() => {
              onOpenChange(false);
              onConfirm();
            }}
          >
            Require it and stop these
          </Button>
        </AlertDialogActions>
      </AlertDialogContent>
    </AlertDialogRoot>
  );
}
