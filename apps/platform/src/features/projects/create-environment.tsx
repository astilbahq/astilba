import { useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import { environmentsOptions } from "../../api/environments";
import { useSessionRequest } from "../../api/use-session-request";
import { ButtonLink } from "../../components/button-link";
import type { FormValidation } from "../../components/form-validation";
import { Label } from "../../components/label";
import { namedValueValidation } from "../../components/named-value-validation";
import { Select } from "../../components/select";
import { usePageTitle } from "../../i18n/use-page-title";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import { ProjectBreadcrumbs, ProjectFrame } from "./project-frame";
import * as s from "./projects.styles";

export function CreateEnvironment({
  projectId,
  onCreated,
}: {
  projectId: string;
  onCreated: (environment: Schema["Environment"], workspaceId: string) => void;
}) {
  return (
    <ProjectFrame key={projectId} projectId={projectId}>
      {(project, workspace) => (
        <EnvironmentForm
          project={project}
          workspace={workspace}
          onCreated={onCreated}
        />
      )}
    </ProjectFrame>
  );
}

function EnvironmentForm({
  project,
  workspace,
  onCreated,
}: {
  project: Schema["Project"];
  workspace: Schema["Workspace"];
  onCreated: (environment: Schema["Environment"], workspaceId: string) => void;
}) {
  const { t } = useTranslation("projects");
  const common = useTranslation("common").t;
  const action = useAction();
  const request = useSessionRequest();
  const queries = useQueryClient();
  const configurationId = useId();
  const descriptionId = useId();
  const [kind, setKind] = useState("dashboard");
  usePageTitle(t(($) => $.createEnvironment.pageTitle));
  const contextualCodes = [
    "invalid_input",
    "forbidden",
    "mfa_required",
    "email_verification_required",
    "subscription_required",
    "recent_auth_required",
    "not_found",
  ];
  const validation: FormValidation = {
    fields: {
      name: namedValueValidation(
        t(($) => $.createEnvironment.required),
        t(($) => $.createEnvironment.invalid),
        t(($) => $.createEnvironment.tooLong, { maximum: 80 }),
      ),
    },
    shouldToastError: (error) =>
      !(error instanceof ApiError) ||
      !contextualCodes.includes(error.detail.code),
    message(error) {
      if (error instanceof ApiError) {
        switch (error.detail.code) {
          case "invalid_input":
            return t(($) => $.createEnvironment.invalid);
          case "forbidden":
            return t(($) => $.createEnvironment.forbidden);
          case "mfa_required":
            return t(($) => $.createEnvironment.mfa);
          case "email_verification_required":
            return t(($) => $.createEnvironment.verifyEmail);
          case "subscription_required":
            return t(($) => $.createEnvironment.subscription);
          case "recent_auth_required":
            return t(($) => $.createEnvironment.recentAuth);
          case "not_found":
            return t(($) => $.detail.unavailable);
        }
      }
      return common(($) => $.requestFailed);
    },
  };
  return (
    <>
      <ProjectBreadcrumbs workspaceId={workspace.id} project={project} />
      <div className={s.intro}>
        <h1 className={s.title}>{t(($) => $.createEnvironment.title)}</h1>
        <p className={s.muted}>
          <bdi>{project.name}</bdi>
        </p>
      </div>
      {workspace.role === "viewer" ? (
        <p className={s.muted}>{t(($) => $.environments.readOnly)}</p>
      ) : (
        <div className={s.form}>
          <Form
            action={action}
            validation={validation}
            label={t(($) => $.createEnvironment.title)}
            onSubmit={(data) =>
              action.run(() =>
                request(
                  async (signal) => {
                    const environment = await result(
                      client.POST(
                        "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments",
                        {
                          params: {
                            path: {
                              workspaceId: workspace.id,
                              projectId: project.id,
                            },
                          },
                          body: {
                            name: text(data, "name").trim(),
                            configurationOwner:
                              kind === "compose" ? "compose" : "dashboard",
                            purpose:
                              kind === "preview_template"
                                ? "preview_template"
                                : "standard",
                          },
                          signal,
                        },
                      ),
                    );
                    const queryKey = environmentsOptions(
                      workspace.id,
                      project.id,
                    ).queryKey;
                    await queries.cancelQueries({ queryKey });
                    await queries.invalidateQueries({ queryKey });
                    return environment;
                  },
                  (environment) => onCreated(environment, workspace.id),
                ),
              )
            }
          >
            <Field
              name="name"
              label={t(($) => $.createEnvironment.name)}
              autoComplete="off"
            />
            <div className={s.configuration}>
              <Label htmlFor={configurationId}>
                {t(($) => $.createEnvironment.configuration)}
              </Label>
              <Select
                id={configurationId}
                name="configuration"
                value={kind}
                aria-describedby={descriptionId}
                onChange={(event) => setKind(event.currentTarget.value)}
              >
                <option value="dashboard">
                  {t(($) => $.environments.visual)}
                </option>
                <option value="compose">
                  {t(($) => $.environments.compose)}
                </option>
                <option value="preview_template">
                  {t(($) => $.environments.previewTemplate)}
                </option>
              </Select>
              <p id={descriptionId} className={s.muted}>
                {kind === "compose"
                  ? t(($) => $.createEnvironment.composeDescription)
                  : kind === "preview_template"
                    ? t(($) => $.createEnvironment.templateDescription)
                    : t(($) => $.createEnvironment.visualDescription)}
              </p>
            </div>
            <div className={s.actions}>
              <BusyButton busy={action.isPending}>
                {t(($) => $.createEnvironment.submit)}
              </BusyButton>
              <ButtonLink
                to="/"
                search={{ workspace: workspace.id, project: project.id }}
                variant="ghost"
              >
                {t(($) => $.create.cancel)}
              </ButtonLink>
            </div>
          </Form>
        </div>
      )}
    </>
  );
}
