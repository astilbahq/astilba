import type { Meta, StoryObj } from "@storybook/tanstack-react";
import { useQueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw/http";
import { useEffect } from "react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { Schema } from "../api/client";
import { draftOptions, type EnvironmentPath } from "../api/environment";
import { Button } from "../components/button";
import { ErrorNotice, useAction } from "../ui";
import { member, scenario } from "../workshop/fixtures";
import { PreparedPlan, usePreparedPlan } from "./prepared-plan";

const path: EnvironmentPath = {
  workspaceId: "00000000-0000-4000-8000-000000000002",
  projectId: "00000000-0000-4000-8000-000000000003",
  environmentId: "00000000-0000-4000-8000-000000000004",
};
const root = `/api/v1/workspaces/${path.workspaceId}/projects/${path.projectId}/environments/${path.environmentId}`;
const edited = "00000000-0000-4000-8000-000000000010";
const newer = "00000000-0000-4000-8000-000000000011";
const planId = "00000000-0000-4000-8000-000000000020";
const server = "00000000-0000-4000-8000-000000000030";
const app = "00000000-0000-4000-8000-000000000040";
const image =
  "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e";
const specification: Schema["Specification"] = {
  version: 1,
  variables: [],
  applications: [
    { id: app, name: "web", kind: "web", source: { kind: "image", image } },
  ],
};
const ready: Schema["VisualPlanStatus"] = {
  id: planId,
  operationId: "00000000-0000-4000-8000-000000000021",
  state: "ready",
  review: null,
  hostReview: false,
  plan: {
    id: planId,
    serverId: server,
    environmentId: path.environmentId,
    unitId: "00000000-0000-4000-8000-000000000022",
    revisionId: edited,
    expectedAppliedId: null,
    recipe: 1,
    specification,
    images: { [app]: image },
    hostChanges: false,
    effects: ["Start web"],
    hash: "a".repeat(64),
    expiresAt: "2099-01-01T00:00:00Z",
    expectedState: "b".repeat(64),
    acceptDrift: false,
  },
};

// The environment's saved draft as the workshop server reports it, reset before each story.
let saved = edited;

function PlanPanel() {
  const action = useAction();
  const queries = useQueryClient();
  const plan = usePreparedPlan(path, edited);
  // Stands in for the environment's event stream, which the workshop does not serve.
  useEffect(() => {
    const refresh = () =>
      void queries.invalidateQueries({
        queryKey: draftOptions(path).queryKey,
      });
    window.addEventListener("workshop:draft-changed", refresh);
    return () => window.removeEventListener("workshop:draft-changed", refresh);
  }, [queries]);
  return (
    <main id="main">
      <ErrorNotice error={action.error ?? plan.error} />
      <Button
        disabled={plan.behind}
        onClick={() => plan.prepare(planId, edited)}
      >
        Prepare plan
      </Button>
      <PreparedPlan
        state={plan}
        path={path}
        servers={[]}
        owner
        write
        dirty={false}
        action={action}
        onApplied={() => {}}
      />
    </main>
  );
}

const meta = {
  title: "Patterns/Prepared deployment plan",
  component: PlanPanel,
  parameters: scenario(
    member,
    http.get(`${root}/draft`, () =>
      HttpResponse.json({
        id: saved,
        environmentId: path.environmentId,
        hash: "c".repeat(64),
        specification,
        createdAt: "2026-11-12T00:00:00Z",
      }),
    ),
    http.get(`${root}/deployment-plans/${planId}`, () =>
      HttpResponse.json(ready),
    ),
  ),
  beforeEach: () => {
    saved = edited;
  },
} satisfies Meta<typeof PlanPanel>;
export default meta;
type Story = StoryObj<typeof meta>;

// A plan is offered for review and apply only while the revision it was prepared from is still
// the saved one. A save made elsewhere withdraws it, moves focus from its controls to the notice
// that says why, and holds back a new plan until the editor loads the newer revision.
export const WithdrawnBySaveElsewhere: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Prepare plan" }));
    const apply = await canvas.findByRole("button", {
      name: "Apply reviewed plan",
    });
    await expect(apply).toBeEnabled();
    apply.focus();
    saved = newer;
    window.dispatchEvent(new Event("workshop:draft-changed"));
    const notice = await canvas.findByRole("alert");
    await expect(notice).toHaveTextContent(
      "The saved revision changed. Prepare a new plan.",
    );
    await waitFor(() => expect(notice).toHaveFocus());
    await expect(
      canvas.queryByRole("region", { name: "Reviewed deployment plan" }),
    ).toBeNull();
    await expect(
      canvas.getByRole("button", { name: "Prepare plan" }),
    ).toBeDisabled();
  },
};
