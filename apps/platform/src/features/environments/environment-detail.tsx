import { useIsFetching, useQuery } from "@tanstack/react-query";
import { type ComponentProps, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";
import { draftOptions } from "../../api/environment";
import { Button } from "../../components/button";
import { TabsList, TabsPanel, TabsRoot, TabsTab } from "../../components/tabs";
import { ErrorToast } from "../../components/toast";
import { IntegrationSettings } from "../integrations";
import {
  LifecycleDialog,
  type LifecycleChange,
  RetireSection,
} from "./environment-lifecycle";
import { EnvironmentSettings } from "./environment-settings";
import { EnvironmentWorkspace } from "./environment-workspace";
import * as s from "./environment.styles";
import type { EnvProps, EnvironmentSection } from "./environment.types";
import { RetirementStatus } from "./retirement-status";

// Marks the first load of what a section shows, which browser journeys wait for because it moves
// the page. It is not aria-busy: a slow request would then hold back status messages from
// assistive technology across every section that has already loaded.
function EnvironmentContent(props: ComponentProps<"section">) {
  const loading = useIsFetching({
    predicate: ({ state }) =>
      state.data === undefined && state.errorUpdateCount === 0,
  });
  return <section {...props} data-loading={loading > 0 || undefined} />;
}

export function EnvironmentDetail(props: EnvProps) {
  const { t } = useTranslation("environment");
  const common = useTranslation("common").t;
  const [section, setSection] = useState<EnvironmentSection>("overview");
  const [visited, setVisited] = useState<ReadonlySet<EnvironmentSection>>(
    () => new Set(["overview"]),
  );
  const content = useRef<HTMLElement>(null);
  const tabs = useRef<Partial<Record<EnvironmentSection, HTMLElement>>>({});
  const path = {
    workspaceId: props.workspaceId,
    projectId: props.projectId,
    environmentId: props.environment.id,
  };
  const draft = useQuery(draftOptions(path));
  const [lifecycle, setLifecycle] = useState<{
    change: LifecycleChange;
    open: boolean;
  }>({ change: "retire", open: false });
  // The state a lifecycle change reached, announced until the environment moves on from it.
  const [changed, setChanged] = useState<Schema["Environment"]["state"]>();
  const title = useRef<HTMLHeadingElement>(null);
  const status = useRef<HTMLHeadingElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const openLifecycle = (change: LifecycleChange, from: HTMLButtonElement) => {
    trigger.current = from;
    setChanged(undefined);
    setLifecycle({ change, open: true });
  };
  const selectSection = (value: EnvironmentSection, focus = false) => {
    setVisited((current) => new Set([...current, value]));
    setSection(value);
    if (focus) tabs.current[value]?.focus();
  };
  const template = props.environment.purpose === "preview_template";
  const managed = props.environment.purpose === "preview";
  // A retiring or retired environment opens read-only until an owner restores it.
  const active = props.environment.state === "active";
  const write = props.write && !managed && active;
  const owner = props.owner && active;
  return (
    <TabsRoot
      value={section}
      onValueChange={(value) => selectSection(value as EnvironmentSection)}
      className={s.page}
    >
      <header className={s.header}>
        <div className={s.intro}>
          <h1 ref={title} tabIndex={-1} className={s.title}>
            <bdi>{props.environment.name}</bdi>
          </h1>
          <p className={s.metadata}>
            <span>
              {props.environment.configurationOwner === "compose"
                ? t(($) => $.compose)
                : t(($) => $.visual)}
            </span>
            <span>
              {template
                ? t(($) => $.template)
                : managed
                  ? t(($) => $.preview)
                  : t(($) => $.standard)}
            </span>
          </p>
        </div>
        <Button
          disabled={!draft.data}
          onClick={() => selectSection("deployments", true)}
        >
          {template
            ? t(($) => $.viewPreviews)
            : write
              ? t(($) => $.reviewDeploy)
              : t(($) => $.viewDeployments)}
        </Button>
      </header>
      {template && <p className={s.note}>{t(($) => $.templateDescription)}</p>}
      {managed && <p className={s.note}>{t(($) => $.previewDescription)}</p>}
      {changed === props.environment.state && (
        <p role="status" className={s.note}>
          {changed === "retired"
            ? t(($) => $.lifecycle.retired)
            : changed === "retiring"
              ? t(($) => $.lifecycle.retirementStarted)
              : t(($) => $.lifecycle.restored)}
        </p>
      )}
      {!active && (
        <RetirementStatus
          environment={props.environment}
          path={path}
          owner={props.owner}
          headingRef={status}
          onChange={openLifecycle}
        />
      )}
      {!props.write && <p className={s.muted}>{t(($) => $.readOnly)}</p>}
      <TabsList aria-label={t(($) => $.sections)}>
        <TabsTab
          value="overview"
          ref={(node) => {
            tabs.current.overview = node ?? undefined;
          }}
        >
          {t(($) => $.overview)}
        </TabsTab>
        <TabsTab
          value="configuration"
          ref={(node) => {
            tabs.current.configuration = node ?? undefined;
          }}
        >
          {t(($) => $.configuration)}
        </TabsTab>
        <TabsTab
          value="deployments"
          ref={(node) => {
            tabs.current.deployments = node ?? undefined;
          }}
        >
          {template ? t(($) => $.previews) : t(($) => $.deployments)}
        </TabsTab>
        <TabsTab
          value="settings"
          ref={(node) => {
            tabs.current.settings = node ?? undefined;
          }}
        >
          {t(($) => $.settings)}
        </TabsTab>
      </TabsList>
      <ErrorToast
        error={draft.data ? draft.error : null}
        message={common(($) => $.requestFailed)}
      />
      <EnvironmentContent
        ref={content}
        tabIndex={-1}
        aria-label={t(($) => $.content)}
        className={s.recoveryTarget}
      >
        {draft.data ? (
          <EnvironmentWorkspace
            {...props}
            write={write}
            owner={owner}
            initial={draft.data}
            section={section}
            visited={visited}
            selectSection={(value) => selectSection(value, true)}
          />
        ) : (
          <div hidden={section === "settings"}>
            {draft.isPending ? (
              <p role="status" className={s.state}>
                {t(($) => $.loading)}
              </p>
            ) : (
              <div role="alert" className={s.state}>
                <h2 className={s.heading}>{t(($) => $.loadFailed)}</h2>
                <p className={s.muted}>{t(($) => $.loadRecovery)}</p>
                <Button
                  disabled={draft.isFetching}
                  onClick={() => {
                    content.current?.focus();
                    void draft.refetch();
                  }}
                >
                  {t(($) => $.retry)}
                </Button>
              </div>
            )}
          </div>
        )}
        <TabsPanel value="settings" keepMounted={visited.has("settings")}>
          <div className={s.settings}>
            <EnvironmentSettings {...props} write={write} owner={owner} />
            {draft.data && owner && template && (
              <IntegrationSettings workspaceId={props.workspaceId} />
            )}
            {owner && !managed && (
              <RetireSection
                onRetire={(from) => openLifecycle("retire", from)}
              />
            )}
          </div>
        </TabsPanel>
      </EnvironmentContent>
      <LifecycleDialog
        change={lifecycle.change}
        open={lifecycle.open}
        environment={props.environment}
        path={path}
        onClose={() => setLifecycle((current) => ({ ...current, open: false }))}
        onChanged={(next) => setChanged(next.state)}
        finalFocus={() =>
          trigger.current?.isConnected
            ? trigger.current
            : (status.current ?? title.current)
        }
      />
    </TabsRoot>
  );
}
