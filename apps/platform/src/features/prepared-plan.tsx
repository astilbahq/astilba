import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { draftOptions, type EnvironmentPath } from "../api/environment";
import * as s from "../styles";
import type { useAction } from "../ui";
import { ApplyReviewedPlan } from "./apply-reviewed-plan";
import { ReviewedPlacements } from "./reviewed-placements";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

/**
 * The deployment plan prepared in this panel. A plan freezes the revision it was prepared from,
 * and the server refuses to apply it once another revision is saved. It stays this panel's plan
 * only while that revision is still the saved one: the one being edited here (`revisionId`) and
 * the environment's latest draft, which a save from another tab, member or automation changes.
 * The environment's event stream refreshes that draft within seconds; polling covers a stream
 * that has failed for good.
 */
export function usePreparedPlan(path: EnvironmentPath, revisionId: string) {
  const [prepared, setPrepared] = useState<{
    id: string;
    revisionId: string;
    idempotencyKey: string;
  }>();
  const saved = useQuery({
    ...draftOptions(path),
    select: (d) => d.id,
    refetchInterval: 10000,
  });
  const current =
    prepared?.revisionId === revisionId && prepared.revisionId === saved.data
      ? prepared
      : undefined;
  const withdrawn = !!prepared && !current;
  // A save elsewhere can withdraw the plan while focus is on its controls. Focus then moves to
  // the notice that says why, rather than to the page body; it is never taken from elsewhere.
  const withdrawnNotice = useRef<HTMLParagraphElement>(null);
  const focusWasInPlan = useRef(false);
  const trackPlanFocus = useCallback((section: HTMLElement) => {
    focusWasInPlan.current = false;
    return () => {
      focusWasInPlan.current = section.contains(document.activeElement);
    };
  }, []);
  useEffect(() => {
    const restore = withdrawn && focusWasInPlan.current;
    focusWasInPlan.current = false;
    if (!restore) return;
    // WebKit ignores focus() in the task that removed the focused control, so wait a frame.
    const frame = requestAnimationFrame(() => {
      if (document.activeElement === document.body)
        withdrawnNotice.current?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [withdrawn]);
  const planId = current?.id ?? "";
  const plan = useQuery({
    queryKey: ["visual-plan", path, planId],
    enabled: !!current,
    queryFn: () =>
      result(
        client.GET(`${root}/deployment-plans/{planId}`, {
          params: { path: { ...path, planId } },
        }),
      ),
    refetchInterval: (query) =>
      query.state.data?.plan ||
      ["failed", "cancelled"].includes(query.state.data?.state ?? "")
        ? false
        : 2000,
  });
  return {
    current,
    withdrawn,
    /** The server only plans the latest saved revision, which the editor must load first. */
    behind: saved.data !== undefined && saved.data !== revisionId,
    status: current ? plan.data : undefined,
    error: plan.error,
    prepare: (id: string, from: string) =>
      setPrepared({
        id,
        revisionId: from,
        idempotencyKey: crypto.randomUUID(),
      }),
    clear: () => setPrepared(undefined),
    withdrawnNotice,
    trackPlanFocus,
  };
}

/** What the prepared plan says: resolving, ready for review and apply, or withdrawn. */
export function PreparedPlan({
  state,
  path,
  servers,
  owner,
  write,
  dirty,
  action,
  onApplied,
}: {
  state: ReturnType<typeof usePreparedPlan>;
  path: EnvironmentPath;
  servers?: Schema["Server"][];
  owner: boolean;
  write: boolean;
  dirty: boolean;
  action: ReturnType<typeof useAction>;
  onApplied: (operation: Schema["Operation"]) => void;
}) {
  const { current, status } = state;
  const ready = status?.plan;
  return (
    <>
      {state.withdrawn && (
        <p role="alert" ref={state.withdrawnNotice} tabIndex={-1}>
          The saved revision changed. Prepare a new plan.
        </p>
      )}
      {status && !ready && (
        <p role="status">
          Plan: {status.state}. View the resolution operation below for details.
        </p>
      )}
      {current && ready && (
        <section
          ref={state.trackPlanFocus}
          className={s.stack}
          aria-label="Reviewed deployment plan"
        >
          <h3>Plan ready</h3>
          <p>
            <strong>
              {ready.distributed
                ? "Coordinated cross-host release"
                : ready.release
                  ? "Protected stateless HTTP release"
                  : "Standard deployment"}
            </strong>
          </p>
          {ready.release && (
            <p>
              Readiness must pass before traffic moves. Drain:{" "}
              {ready.release.options.drainSeconds} seconds. Release command:{" "}
              {ready.release.options.command.length
                ? ready.release.options.command.join(" · ")
                : "none"}
              .
            </p>
          )}
          <p>
            Server:{" "}
            {servers?.find((server) => server.id === ready.serverId)?.name ??
              ready.serverId}
          </p>
          <ul>
            {ready.specification.applications.map((app) => (
              <li key={app.id}>
                {app.name}: {app.replicas ?? 1}{" "}
                {(app.replicas ?? 1) === 1 ? "instance" : "instances"}
              </li>
            ))}
          </ul>
          {ready.distributed && (
            <ReviewedPlacements
              distributed={ready.distributed}
              applications={ready.specification.applications}
              servers={servers}
            />
          )}
          <ul>
            {ready.effects.map((effect) => (
              <li key={effect}>{effect}</li>
            ))}
          </ul>
          <p>
            Expires {new Date(ready.expiresAt).toLocaleTimeString()} · revision{" "}
            {ready.revisionId.slice(0, 8)}
          </p>
          <ul>
            {!ready.distributed &&
              ready.specification.applications.map((app) => (
                <li key={app.id}>
                  <strong>{app.name}</strong>
                  <code className={s.code}> {ready.images[app.id]}</code>
                </li>
              ))}
          </ul>
          <p>
            {ready.specification.bindings?.length ?? 0} database bindings.
            Secret values are withheld; the plan uses immutable version
            references.
          </p>
          <details>
            <summary>Effective configuration and secret references</summary>
            <pre className={`${s.code} ${s.wrappedCode}`}>
              {JSON.stringify(ready.specification, null, 2)}
            </pre>
          </details>
          <ApplyReviewedPlan
            key={current.id}
            path={path}
            planId={current.id}
            plan={ready}
            review={!!status?.hostReview}
            idempotencyKey={current.idempotencyKey}
            owner={owner}
            write={write}
            blocked={dirty}
            action={action}
            onApplied={onApplied}
          />
        </section>
      )}
    </>
  );
}
