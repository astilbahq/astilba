import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { client, result } from "./api/client";
import { sessionOptions } from "./api/session";
import { subscriptionOptions } from "./api/subscription";
import { Button } from "./components/button";
import { Link } from "./components/link";
import * as s from "./styles";
import { ErrorNotice, useAction } from "./ui";

export const hostingOptions = {
  queryKey: ["hosting"],
  queryFn: () => result(client.GET("/api/v1/hosting")),
};
export function WorkspaceSubscription({
  workspaceId,
  owner,
}: {
  workspaceId: string;
  owner: boolean;
}) {
  const { t } = useTranslation("auth");
  const user = useQuery(sessionOptions).data?.user;
  const action = useAction();
  const status = useQuery(subscriptionOptions(workspaceId));
  if (!status.data?.hosted) return <ErrorNotice error={status.error} />;
  const data = status.data;
  return (
    <section className={s.panel}>
      <h2>Workspace subscription</h2>
      {owner &&
        (!user?.mfaEnabled ||
          !user.recoveryCodesSaved ||
          user.recoveryCodesRemaining === 0 ||
          !data.managementActive) && (
          <Link to="/onboarding" search={{ workspace: workspaceId }}>
            {t(($) => $.onboarding.resume)}
          </Link>
        )}
      <ErrorNotice error={action.error ?? status.error} />
      <p>
        Status: {data.status}. Server allowance: {data.serverLimit}. No
        per-application, database or member charges.
      </p>
      {data.managementUntil && (
        <p>
          Management access until{" "}
          {new Date(data.managementUntil).toLocaleString()}.
        </p>
      )}
      {data.recoveryUntil && (
        <p>
          Export, recovery and transfer remain available until{" "}
          {new Date(data.recoveryUntil).toLocaleString()}. Running applications
          and installed schedules remain on your servers.
        </p>
      )}
      <p>
        {data.support ||
          "Contact the beta operator for asynchronous support hours."}
      </p>
      {owner && data.billingEnabled && (
        <div className={s.row}>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/subscription/checkout",
                    { params: { path: { workspaceId } } },
                  ),
                );
                location.assign(v.url);
              })
            }
          >
            Start subscription
          </Button>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/subscription/portal",
                    { params: { path: { workspaceId } } },
                  ),
                );
                location.assign(v.url);
              })
            }
          >
            Manage billing
          </Button>
        </div>
      )}
      {owner && (
        <p>
          Billing changes require verified email, two-factor authentication and
          recent identity confirmation in{" "}
          <Link to="/account" search={{ workspace: workspaceId }}>
            account security
          </Link>
          .
        </p>
      )}
    </section>
  );
}
