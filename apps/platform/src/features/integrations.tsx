import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result } from "../api/client";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";
import { GitHubAppSettings } from "./github-app";

const wsRoot = "/api/v1/workspaces/{workspaceId}" as const;
export function IntegrationSettings({ workspaceId }: { workspaceId: string }) {
  const action = useAction(),
    queries = useQueryClient();
  const workspace = { workspaceId },
    path = { workspaceId };
  const [webhook, setWebhook] = useState<{ url: string; secret: string }>();
  return (
    <>
      <ErrorNotice error={action.error} />
      <details className={s.panel}>
        <summary>Repository, registry and build secret settings</summary>
        <p className={s.muted}>
          Creating credentials requires a recent account verification. Use
          repository-scoped tokens and separate registry push and pull
          credentials.
        </p>
        <GitHubAppSettings workspaceId={workspaceId} />
        <Form
          action={action}
          label="Connect repository"
          onSubmit={(d, form) =>
            action.run(async () => {
              const v = await result(
                client.POST(`${wsRoot}/git-connections`, {
                  params: { path: workspace },
                  body: {
                    name: text(d, "name"),
                    provider: text(d, "provider") as
                      | "github"
                      | "gitlab"
                      | "gitea",
                    baseUrl: text(d, "base"),
                    repository: text(d, "repository"),
                    api: {
                      username: text(d, "apiUser"),
                      password: text(d, "apiToken"),
                    },
                    clone: {
                      username: text(d, "cloneUser"),
                      password: text(d, "cloneToken"),
                    },
                  },
                }),
              );
              setWebhook({
                url: v.webhookUrl,
                secret: v.webhookSecret ?? "",
              });
              form.reset();
              await queries.invalidateQueries({
                queryKey: ["git-connections", path.workspaceId],
              });
            })
          }
        >
          <h3>Repository connection</h3>
          <Field name="name" label="Connection name" />
          <Label>
            Provider
            <Select name="provider">
              <option value="github">GitHub</option>
              <option value="gitlab">GitLab</option>
              <option value="gitea">Gitea</option>
            </Select>
          </Label>
          <Field
            name="base"
            label="Provider origin, such as https://github.com"
          />
          <Field
            name="repository"
            label="Repository path, such as team/application"
          />
          <Field
            name="apiUser"
            label="API and submission username"
            required={false}
          />
          <Field
            name="apiToken"
            label="API and submission token"
            type="password"
            required={false}
            autoComplete="off"
          />
          <Field
            name="cloneUser"
            label="Read-only clone username"
            required={false}
          />
          <Field
            name="cloneToken"
            label="Read-only clone token"
            type="password"
            required={false}
            autoComplete="off"
          />
          <BusyButton busy={action.isPending}>Connect repository</BusyButton>
        </Form>
        {webhook && (
          <div role="status">
            <p>
              Configure a push webhook in your repository. Copy this secret now;
              it is shown once.
            </p>
            <Label>
              Webhook URL
              <Input readOnly value={webhook.url} />
            </Label>
            <Label>
              Webhook secret
              <Input readOnly value={webhook.secret} />
            </Label>
            <Button type="button" onClick={() => setWebhook(undefined)}>
              Dismiss webhook secret
            </Button>
          </div>
        )}
        <Form
          action={action}
          label="Add registry"
          onSubmit={(d, form) =>
            action.run(async () => {
              await result(
                client.POST(`${wsRoot}/registries`, {
                  params: { path: workspace },
                  body: {
                    name: text(d, "name"),
                    repository: text(d, "repository"),
                    push: {
                      username: text(d, "pushUser"),
                      password: text(d, "pushToken"),
                    },
                    pull: {
                      username: text(d, "pullUser"),
                      password: text(d, "pullToken"),
                    },
                  },
                }),
              );
              form.reset();
              await queries.invalidateQueries({
                queryKey: ["registries", path.workspaceId],
              });
            })
          }
        >
          <h3>Image registry</h3>
          <Field name="name" label="Registry name" />
          <Field
            name="repository"
            label="Image repository, such as ghcr.io/team/application"
          />
          <Field name="pushUser" label="Push username" />
          <Field
            name="pushToken"
            label="Push token"
            type="password"
            autoComplete="off"
          />
          <Field name="pullUser" label="Pull username" />
          <Field
            name="pullToken"
            label="Pull token"
            type="password"
            autoComplete="off"
          />
          <BusyButton busy={action.isPending}>Add registry</BusyButton>
        </Form>
        <Form
          action={action}
          label="Create build secret"
          onSubmit={(d, form) =>
            action.run(async () => {
              await result(
                client.POST(`${wsRoot}/build-secrets`, {
                  params: { path: workspace },
                  body: { name: text(d, "name"), value: text(d, "value") },
                }),
              );
              form.reset();
              await queries.invalidateQueries({
                queryKey: ["build-secrets", path.workspaceId],
              });
            })
          }
        >
          <h3>Build secret version</h3>
          <Field name="name" label="Build secret name" />
          <Field
            name="value"
            label="Build secret value"
            type="password"
            autoComplete="off"
          />
          <BusyButton busy={action.isPending}>Create build secret</BusyButton>
        </Form>
      </details>
    </>
  );
}
