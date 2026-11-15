import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import * as s from "../styles";
import { BusyButton, Form, text, useAction } from "../ui";
import { MfaRequirementConfirmation } from "./mfa-requirement";

// WorkspaceSettings lets an owner rename the workspace and require two-factor authentication.
// Requiring it stops what members without it approved, so the owner first sees what that is and
// confirms. onSaved runs after a save.
export function WorkspaceSettings({
  workspace,
  onSaved,
}: {
  workspace: Schema["Workspace"];
  onSaved: () => Promise<void>;
}) {
  const action = useAction();
  const [confirming, setConfirming] = useState<{
    settings: Schema["WorkspaceSettings"];
    approvals: Schema["MfaRequirementApproval"][];
  }>();
  const params = { params: { path: { workspaceId: workspace.id } } };
  const save = async (settings: Schema["WorkspaceSettings"]) => {
    await result(
      client.PUT("/api/v1/workspaces/{workspaceId}", {
        ...params,
        body: settings,
      }),
    );
    await onSaved();
  };
  return (
    <section className={s.panel}>
      <h2>Workspace settings</h2>
      <Form
        action={action}
        onSubmit={(d) => {
          const settings = {
            name: text(d, "name"),
            requireMfa: d.get("requireMfa") === "on",
          };
          action.run(async () => {
            if (settings.requireMfa && !workspace.requireMfa) {
              const { approvals } = await result(
                client.GET(
                  "/api/v1/workspaces/{workspaceId}/mfa-requirement",
                  params,
                ),
              );
              if (approvals.length > 0) {
                setConfirming({ settings, approvals });
                return;
              }
            }
            await save(settings);
          });
        }}
      >
        <Label>
          Name
          <Input
            name="name"
            defaultValue={workspace.name}
            required
            maxLength={80}
          />
        </Label>
        <Label className={s.row}>
          <Checkbox name="requireMfa" defaultChecked={workspace.requireMfa} />
          Require two-factor authentication for changes
        </Label>
        <small>
          You’ll be asked to confirm your identity before changing these
          settings.
        </small>
        <BusyButton busy={action.isPending}>Save settings</BusyButton>
      </Form>
      <MfaRequirementConfirmation
        approvals={confirming?.approvals ?? []}
        open={!!confirming}
        onOpenChange={(open) => {
          if (!open) setConfirming(undefined);
        }}
        onConfirm={() => {
          const settings = confirming?.settings;
          if (settings) action.run(() => save(settings));
        }}
      />
    </section>
  );
}
