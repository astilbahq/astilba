import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { draftOptions } from "../../api/environment";
import { Button } from "../../components/button";
import { TabsList, TabsPanel, TabsRoot, TabsTab } from "../../components/tabs";
import { ErrorToast } from "../../components/toast";
import { IntegrationSettings } from "../integrations";
import { EnvironmentSettings } from "./environment-settings";
import { EnvironmentWorkspace } from "./environment-workspace";
import * as s from "./environment.styles";
import type { EnvProps, EnvironmentSection } from "./environment.types";

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
  const selectSection = (value: EnvironmentSection, focus = false) => {
    setVisited((current) => new Set([...current, value]));
    setSection(value);
    if (focus) tabs.current[value]?.focus();
  };
  const template = props.environment.purpose === "preview_template";
  const managed = props.environment.purpose === "preview";
  return (
    <TabsRoot
      value={section}
      onValueChange={(value) => selectSection(value as EnvironmentSection)}
      className={s.page}
    >
      <header className={s.header}>
        <div className={s.intro}>
          <h1 className={s.title}>
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
            : props.write && !managed
              ? t(($) => $.reviewDeploy)
              : t(($) => $.viewDeployments)}
        </Button>
      </header>
      {template && <p className={s.note}>{t(($) => $.templateDescription)}</p>}
      {managed && <p className={s.note}>{t(($) => $.previewDescription)}</p>}
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
      <section
        ref={content}
        tabIndex={-1}
        aria-label={t(($) => $.content)}
        className={s.recoveryTarget}
      >
        {draft.data ? (
          <EnvironmentWorkspace
            {...props}
            write={props.write && !managed}
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
            <EnvironmentSettings {...props} write={props.write && !managed} />
            {draft.data && props.owner && template && (
              <IntegrationSettings workspaceId={props.workspaceId} />
            )}
          </div>
        </TabsPanel>
      </section>
    </TabsRoot>
  );
}
