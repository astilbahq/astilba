import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result } from "../../api/client";
import { projectsOptions } from "../../api/projects";
import { useSessionRequest } from "../../api/use-session-request";
import { Button } from "../../components/button";
import type { FormValidation } from "../../components/form-validation";
import { Icon } from "../../components/icons";
import { Link } from "../../components/link";
import { namedValueValidation } from "../../components/named-value-validation";
import { BusyButton, Field, Form, text, useAction } from "../../ui";
import { WorkspaceContext } from "../../workspace-context";
import * as s from "./projects.styles";

export function ProjectsOverview() {
  const { t } = useTranslation("projects");
  const common = useTranslation("common").t;
  const workspace = useContext(WorkspaceContext);
  const workspaceId = workspace?.id ?? "";
  const projects = useQuery(projectsOptions(workspaceId));
  const queries = useQueryClient();
  const action = useAction();
  const request = useSessionRequest();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState(false);
  const creation = useRef<HTMLElement>(null);
  useEffect(() => {
    if (creating) creation.current?.querySelector("input")?.focus();
  }, [creating]);
  const heading = useRef<HTMLHeadingElement>(null);
  const validation: FormValidation = {
    fields: {
      name: namedValueValidation(
        t(($) => $.create.required),
        t(($) => $.create.invalid),
        t(($) => $.create.tooLong, { maximum: 80 }),
      ),
    },
    shouldToastError: (error) =>
      !(error instanceof ApiError) ||
      ![
        "invalid_input",
        "forbidden",
        "mfa_required",
        "email_verification_required",
        "subscription_required",
        "recent_auth_required",
      ].includes(error.detail.code),
    message(error) {
      if (error instanceof ApiError) {
        if (error.detail.code === "invalid_input")
          return t(($) => $.create.invalid);
        if (error.detail.code === "forbidden")
          return t(($) => $.create.forbidden);
        if (error.detail.code === "mfa_required") return t(($) => $.create.mfa);
        if (error.detail.code === "email_verification_required")
          return t(($) => $.create.verifyEmail);
        if (error.detail.code === "subscription_required")
          return t(($) => $.create.subscription);
        if (error.detail.code === "recent_auth_required")
          return t(($) => $.create.recentAuth);
      }
      return common(($) => $.requestFailed);
    },
  };
  if (!workspace)
    return (
      <section className={s.page}>
        <h1 className={s.title}>{t(($) => $.noWorkspaceTitle)}</h1>
        <p className={s.muted}>{t(($) => $.noWorkspace)}</p>
      </section>
    );
  const write = workspace.role !== "viewer";
  return (
    <section className={s.page}>
      <header className={s.header}>
        <div className={s.intro}>
          <h1 ref={heading} tabIndex={-1} className={s.title}>
            {t(($) => $.title)}
          </h1>
          <p className={s.muted}>
            {t(($) => $.description, { workspace: workspace.name })}
          </p>
        </div>
        {write && !creating && (
          <Button
            type="button"
            variant="secondary"
            onClick={() => {
              action.reset();
              setCreating(true);
              setCreated(false);
            }}
          >
            {t(($) => $.newProject)}
          </Button>
        )}
      </header>
      {created && <p role="status">{t(($) => $.create.created)}</p>}
      {creating && write && (
        <section
          ref={creation}
          className={s.form}
          aria-label={t(($) => $.create.title)}
        >
          <h2 className={s.heading}>{t(($) => $.create.title)}</h2>
          <Form
            action={action}
            validation={validation}
            label={t(($) => $.create.title)}
            onSubmit={(data, form) =>
              action.run(() =>
                request(
                  async (signal) => {
                    await result(
                      client.POST("/api/v1/workspaces/{workspaceId}/projects", {
                        params: { path: { workspaceId } },
                        body: { name: text(data, "name").trim() },
                        signal,
                      }),
                    );
                    await queries.cancelQueries({
                      queryKey: projectsOptions(workspaceId).queryKey,
                    });
                    await queries.invalidateQueries({
                      queryKey: projectsOptions(workspaceId).queryKey,
                    });
                  },
                  () => {
                    form.reset();
                    setCreating(false);
                    setCreated(true);
                    heading.current?.focus();
                  },
                ),
              )
            }
          >
            <Field
              name="name"
              label={t(($) => $.create.name)}
              autoComplete="off"
            />
            <div className={s.actions}>
              <BusyButton busy={action.isPending}>
                {t(($) => $.create.submit)}
              </BusyButton>
              <Button
                type="button"
                variant="ghost"
                disabled={action.isPending}
                onClick={() => {
                  setCreating(false);
                  heading.current?.focus();
                }}
              >
                {t(($) => $.create.cancel)}
              </Button>
            </div>
          </Form>
        </section>
      )}
      {projects.isPending ? (
        <p role="status" className={s.state}>
          {t(($) => $.loading)}
        </p>
      ) : projects.isError ? (
        <div className={s.state} role="alert">
          <h2 className={s.heading}>{t(($) => $.loadFailed)}</h2>
          <p className={s.muted}>{t(($) => $.loadRecovery)}</p>
          <Button
            type="button"
            variant="secondary"
            disabled={projects.isFetching}
            onClick={() => void projects.refetch()}
          >
            {t(($) => $.retry)}
          </Button>
        </div>
      ) : projects.data.length ? (
        <ul className={s.list}>
          {projects.data.map((project) => (
            <li key={project.id}>
              <Link
                to="/"
                search={{
                  workspace: workspaceId,
                  project: project.id,
                  environment: undefined,
                }}
                variant="plain"
                className={s.row}
              >
                <strong className={s.name}>
                  <bdi>{project.name}</bdi>
                </strong>
                <span className={s.open}>
                  {t(($) => $.open)}
                  <Icon>
                    <path d="m9 5 7 7-7 7" />
                  </Icon>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <div className={s.state}>
          <h2 className={s.heading}>
            {write ? t(($) => $.emptyTitle) : t(($) => $.emptyViewerTitle)}
          </h2>
          <p className={s.muted}>{t(($) => $.emptyDescription)}</p>
        </div>
      )}
      {!write && <p className={s.muted}>{t(($) => $.readOnly)}</p>}
    </section>
  );
}
