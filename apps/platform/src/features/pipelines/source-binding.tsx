import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { ApiError, client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { gitConnectionsOptions } from "../../api/integrations";
import { serversOptions } from "../../api/servers";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../../components/alert-dialog";
import { Button } from "../../components/button";
import { Checkbox } from "../../components/checkbox";
import { Label } from "../../components/label";
import { Select } from "../../components/select";
import { Textarea } from "../../components/textarea";
import * as s from "../../styles";
import { ErrorNotice, Field, Form, text, useAction } from "../../ui";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

// SourceFailure says why automatic imports stopped at a commit. They don't try it again; the
// reason says what changes that.
export function SourceFailure({
  failure,
}: {
  failure: Schema["GitSourceFailure"] | null;
}) {
  if (!failure) return null;
  return (
    <div className={s.notice}>
      <p>
        Commit <code>{failure.commit.slice(0, 12)}</code> was not imported.{" "}
        {failure.reason}
      </p>
    </div>
  );
}

const lines = (value: string) =>
  value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);

// ChangeSource lets an owner bind the environment's source again, with other files, a branch, a
// connection or a runner, as a file renamed upstream needs, or stop importing from Git. Both
// confirm the owner's identity first. changed runs after either succeeds, or after the server
// says the source changed since it was shown, with what to say. Its form starts from the source
// as shown, so its parent keys it by the source's hash: a change made elsewhere starts it again.
export function ChangeSource({
  path,
  revisionId,
  source,
  dirty,
  changed,
}: {
  path: EnvironmentPath;
  revisionId: string;
  source: Schema["GitSourceStatus"];
  dirty: boolean;
  changed: (message: string) => Promise<void>;
}) {
  const action = useAction({ recentAuth: true });
  // Removal has its own action, so its refusal is shown in its dialog, not as the form's error.
  const removal = useAction({ recentAuth: true });
  const connections = useQuery(gitConnectionsOptions(path.workspaceId));
  const servers = useQuery(serversOptions(path.workspaceId));
  const current = source.configuration;
  // The options load after the form, so each choice is held here rather than by the form.
  const [connection, setConnection] = useState(current.connectionId);
  const [runner, setRunner] = useState(current.runnerId);
  const [fallback, setFallback] = useState(current.fallbackRunnerId ?? "");
  const [removing, setRemoving] = useState(false);
  const remove = useRef<HTMLButtonElement>(null);
  const runners = servers.data?.filter(
    (v) => v.role !== "deployment" && v.capabilities?.build,
  );
  const fallbacks = runners?.filter((v) => v.id !== runner);
  // A stored fallback that is no longer a runner here is shown as such, so what is sent is what is
  // shown.
  const fallbackGone =
    !!fallback && !!fallbacks && !fallbacks.some((v) => v.id === fallback);
  return (
    <details>
      <summary>Change or remove the Git source</summary>
      <div className={s.stack}>
        <ErrorNotice error={action.error} />
        <Form
          action={action}
          label="Change the Git source"
          onSubmit={(d) =>
            action.run(async () => {
              const request = client.PUT(`${root}/source`, {
                params: { path },
                body: {
                  baseRevisionId: revisionId,
                  sourceHash: source.sourceHash,
                  configuration: {
                    connectionId: connection,
                    branch: text(d, "branch"),
                    runnerId: runner,
                    fallbackRunnerId: fallback || undefined,
                    entrypoints: lines(text(d, "entrypoints")),
                    paths: lines(text(d, "paths")),
                    autoDeploy: d.has("autoDeploy"),
                  },
                },
              });
              try {
                await result(request);
              } catch (e) {
                if (!(e instanceof ApiError) || e.status !== 409) throw e;
                await changed(
                  "The source was not bound again: it changed since it was shown, or an import of this environment is running. It now shows as it is; try again once any import finishes.",
                );
                return;
              }
              await changed(
                "Source changed. Its import is queued; when it succeeds, use Reload current to load it.",
              );
            })
          }
        >
          <p className={s.muted}>
            Binding the source again imports the branch&rsquo;s current commit,
            replacing the Compose files in the saved draft, and makes automatic
            imports and deployments yours. Deployments wait for that import.
            Earlier revisions stay in history.
          </p>
          <Label>
            Repository connection
            <Select
              name="connection"
              required
              value={connection}
              onChange={(e) => setConnection(e.target.value)}
            >
              {!connections.data?.some((c) => c.id === connection) && (
                <option value="">Choose a repository</option>
              )}
              {connections.data?.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} · {c.repository}
                </option>
              ))}
            </Select>
          </Label>
          <Field name="branch" label="Branch" defaultValue={current.branch} />
          <Label>
            Build runner
            <Select
              name="runner"
              required
              value={runner}
              onChange={(e) => {
                setRunner(e.target.value);
                // A runner can't be its own fallback.
                if (e.target.value === fallback) setFallback("");
              }}
            >
              {!runners?.some((v) => v.id === runner) && (
                <option value="">Choose a runner</option>
              )}
              {runners?.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name} · {v.status} · {v.capabilities?.arch}
                </option>
              ))}
            </Select>
          </Label>
          <Label>
            Fallback runner
            <Select
              name="fallback"
              value={fallback}
              onChange={(e) => setFallback(e.target.value)}
            >
              <option value="">Queue on selected runner</option>
              {fallbackGone && (
                <option value={fallback}>
                  A runner no longer in this workspace
                </option>
              )}
              {fallbacks?.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name} · {v.status}
                </option>
              ))}
            </Select>
          </Label>
          <Label>
            Compose entrypoints, one path per line
            <Textarea
              name="entrypoints"
              required
              defaultValue={current.entrypoints.join("\n")}
            />
          </Label>
          <Label>
            Selected source files, one path per line
            <Textarea
              name="paths"
              required
              defaultValue={current.paths.join("\n")}
            />
          </Label>
          <Label>
            <Checkbox name="autoDeploy" defaultChecked={current.autoDeploy} />
            Automatically deploy future imported commits after the first
            reviewed deployment
          </Label>
          <Button type="submit" disabled={dirty || action.isPending}>
            Bind again and import
          </Button>
        </Form>
        <div>
          <Button
            ref={remove}
            type="button"
            focusableWhenDisabled
            disabled={action.isPending || removal.isPending}
            onClick={() => {
              removal.reset();
              setRemoving(true);
            }}
          >
            Remove Git source
          </Button>
        </div>
        <AlertDialogRoot
          open={removing}
          onOpenChange={(next) => {
            if (!next && !removal.isPending) setRemoving(false);
          }}
        >
          <AlertDialogContent finalFocus={remove}>
            <AlertDialogTitle>Remove the Git source?</AlertDialogTitle>
            <AlertDialogDescription>
              Pushes stop importing into this environment. The saved draft keeps
              its Compose files, and an import that is running saves nothing.
              You can bind a source again later.
            </AlertDialogDescription>
            {/* A refusal stays in the dialog, beside the control that asked. */}
            <ErrorNotice error={removal.error} />
            <AlertDialogActions>
              <AlertDialogClose
                render={
                  <Button disabled={removal.isPending}>Keep source</Button>
                }
              />
              <Button
                type="button"
                variant="danger"
                focusableWhenDisabled
                disabled={removal.isPending}
                onClick={() =>
                  removal.run(async () => {
                    await result(
                      client.DELETE(`${root}/source`, { params: { path } }),
                    );
                    setRemoving(false);
                    await changed(
                      "Git source removed. The saved draft keeps its Compose files.",
                    );
                  })
                }
              >
                Remove source
              </Button>
            </AlertDialogActions>
          </AlertDialogContent>
        </AlertDialogRoot>
      </div>
    </details>
  );
}
