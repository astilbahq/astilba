import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, result } from "../../api/client";
import * as s from "../../styles";
import {
  BusyButton,
  ErrorNotice,
  Field,
  Form,
  text,
  useAction,
} from "../../ui";
import { AutomationCredentials } from "../automation";
import type { EnvProps } from "./environment.types";

export function EnvironmentSettings(props: EnvProps) {
  const path = {
    workspaceId: props.workspaceId,
    projectId: props.projectId,
    environmentId: props.environment.id,
  };
  return (
    <>
      <Secrets {...props} />
      {props.owner && (
        <AutomationCredentials
          key={props.environment.id}
          path={path}
          readOnly={
            !!props.environment.purpose &&
            props.environment.purpose !== "standard"
          }
        />
      )}
    </>
  );
}
function Secrets({ workspaceId, projectId, environment, write }: EnvProps) {
  const path = { workspaceId, projectId, environmentId: environment.id };
  const queries = useQueryClient();
  const action = useAction();
  const [message, setMessage] = useState("");
  const secrets = useQuery({
    queryKey: ["secrets", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/secrets",
          { params: { path } },
        ),
      ),
  });
  const latest = secrets.data?.filter(
    (v, i, all) => all.findIndex((x) => x.secretId === v.secretId) === i,
  );
  return (
    <section className={s.panel}>
      <h2>Secrets</h2>
      <p className={s.muted}>
        Values are write-only. Reference a version ID in your specification with{" "}
        <code>secretVersionId</code>.
      </p>
      <ErrorNotice error={action.error ?? secrets.error} />
      {message && (
        <p className={s.notice} role="status">
          {message}
        </p>
      )}
      <div className={s.list}>
        {latest?.map((v) => (
          <div key={v.id}>
            <strong>{v.name}</strong>
            <p className={s.code}>{v.id}</p>
            <small>
              Version created {new Date(v.createdAt).toLocaleString()}
            </small>
          </div>
        ))}
      </div>
      {write && (
        <Form
          action={action}
          onSubmit={(d, form) =>
            action.run(async () => {
              const name = text(d, "name");
              const existing = latest?.find((v) => v.name === name);
              const value = text(d, "value");
              if (existing)
                await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/secrets/{secretId}/versions",
                    {
                      params: {
                        path: { ...path, secretId: existing.secretId },
                      },
                      body: { value },
                    },
                  ),
                );
              else
                await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/secrets",
                    { params: { path }, body: { name, value } },
                  ),
                );
              form.reset();
              setMessage(
                "Secret version saved. Its value cannot be read back.",
              );
              await queries.cancelQueries({ queryKey: ["secrets", path] });
              await queries.invalidateQueries({ queryKey: ["secrets", path] });
            })
          }
        >
          <Field name="name" label="Secret name" />
          <Field
            name="value"
            label="Secret value"
            type="password"
            autoComplete="off"
          />
          <small>
            Reusing a name adds a version. Confirm your identity in Account
            security if requested.
          </small>
          <BusyButton busy={action.isPending}>Save secret version</BusyButton>
        </Form>
      )}
    </section>
  );
}
