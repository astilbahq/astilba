import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import { Checkbox } from "../components/checkbox";
import { Label } from "../components/label";
import * as s from "../styles";
import type { useAction } from "../ui";
import type { EnvironmentPath } from "./visual-editor";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

/**
 * Applies a reviewed plan. A plan whose host changes need review, because the environment's
 * applied deployment does not already have them, waits for an owner to approve them; the
 * server may then ask them to confirm their identity. Mount it once per plan, so an approval
 * never carries over to the next one.
 */
export function ApplyReviewedPlan({
  path,
  planId,
  plan,
  review,
  idempotencyKey,
  owner,
  write,
  blocked,
  action,
  onApplied,
}: {
  path: EnvironmentPath;
  planId: string;
  plan: Schema["VisualPlan"];
  review: boolean;
  idempotencyKey: string;
  owner: boolean;
  write: boolean;
  blocked: boolean;
  action: ReturnType<typeof useAction>;
  onApplied: (operation: Schema["Operation"]) => void;
}) {
  const queries = useQueryClient();
  const [approved, setApproved] = useState(false);
  return (
    <>
      {review && (
        <div className={s.notice}>
          <p>
            <strong>
              This plan also changes its server&apos;s host configuration,
            </strong>{" "}
            such as the proxy, published ports, certificates or elevated
            container privileges listed above.{" "}
            {owner
              ? "Applying it needs your approval, and you may be asked to confirm your identity."
              : "Only a workspace owner can apply it."}
          </p>
          {owner && (
            <Label className={s.row}>
              <Checkbox
                checked={approved}
                onChange={(event) => setApproved(event.currentTarget.checked)}
              />
              Approve the host changes listed in this plan
            </Label>
          )}
          <p>
            Later deployments, including automatic ones, keep published ports
            this server already has. A privileged or added-capability service,
            and any service sharing a volume or network with one, keeps its
            approval only while its definition and image digest stay exactly as
            applied; any other change to it is reviewed again.
          </p>
        </div>
      )}
      {write && (
        <Button
          type="button"
          variant="primary"
          disabled={
            blocked ||
            action.isPending ||
            Date.now() > Date.parse(plan.expiresAt) ||
            (review && !approved)
          }
          onClick={() =>
            action.run(async () => {
              const operation = await result(
                client.POST(`${root}/deployment-plans/{planId}/apply`, {
                  params: { path: { ...path, planId } },
                  body: {
                    idempotencyKey,
                    approveHostChanges: review && approved,
                  },
                }),
              );
              onApplied(operation);
              await queries.invalidateQueries({
                queryKey: ["operations", path.workspaceId],
              });
              await queries.invalidateQueries({
                queryKey: ["environments", path.workspaceId, path.projectId],
              });
            })
          }
        >
          Apply reviewed plan
        </Button>
      )}
    </>
  );
}
