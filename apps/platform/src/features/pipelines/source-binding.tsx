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
import { AutomaticDeploymentScope } from "./automatic-deployment-scope";

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

type Choice = [string, (value: string) => void];

// SourceFields are a Git source's settings, for importing one or binding it again. The caller holds
// the connection and runner, which the import shares with the page's other Git forms. The options
// load after the form, so each choice is held in state rather than by the form, and a stored choice
// that is gone is shown as such, so what is sent is what is shown.
function SourceFields({
  workspaceId,
  connection: [connection, setConnection],
  runner: [runner, setRunner],
  initial,
}: {
  workspaceId: string;
  connection: Choice;
  runner: Choice;
  initial?: Schema["GitSourceConfiguration"];
}) {
  const connections = useQuery(gitConnectionsOptions(workspaceId));
  const servers = useQuery(serversOptions(workspaceId));
  const [stored, setFallback] = useState(initial?.fallbackRunnerId ?? "");
  // A runner can't be its own fallback, however the runner was chosen: the import shares it with
  // the page's other Git forms.
  const fallback = stored === runner ? "" : stored;
  const runners = servers.data?.filter(
    (v) => v.role !== "deployment" && v.capabilities?.build,
  );
  const fallbacks = runners?.filter((v) => v.id !== runner);
  const fallbackGone =
    !!fallback && !!fallbacks && !fallbacks.some((v) => v.id === fallback);
  return (
    <>
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
      <Field name="branch" label="Branch" defaultValue={initial?.branch} />
      <Label>
        Build runner
        <Select
          name="runner"
          required
          value={runner}
          onChange={(e) => setRunner(e.target.value)}
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
          defaultValue={initial?.entrypoints.join("\n") ?? "compose.yaml"}
        />
      </Label>
      <Label>
        Selected source files, one path per line
        <Textarea
          name="paths"
          required
          defaultValue={initial?.paths.join("\n") ?? "compose.yaml"}
        />
      </Label>
      <Label>
        <Checkbox name="autoDeploy" defaultChecked={initial?.autoDeploy} />
        Automatically deploy future imported commits after the first reviewed
        deployment
      </Label>
    </>
  );
}

// sourceConfiguration reads SourceFields' settings from their form.
const sourceConfiguration = (
  d: FormData,
): Schema["GitSourceConfiguration"] => ({
  connectionId: text(d, "connection"),
  branch: text(d, "branch"),
  runnerId: text(d, "runner"),
  fallbackRunnerId: text(d, "fallback") || undefined,
  entrypoints: lines(text(d, "entrypoints")),
  paths: lines(text(d, "paths")),
  autoDeploy: d.has("autoDeploy"),
});

// ImportSource binds an environment that has no Git source yet and imports the branch.
export function ImportSource({
  path,
  revisionId,
  dirty,
  action,
  connection,
  runner,
  imported,
}: {
  path: EnvironmentPath;
  revisionId: string;
  dirty: boolean;
  action: ReturnType<typeof useAction>;
  connection: Choice;
  runner: Choice;
  imported: (message: string) => Promise<void>;
}) {
  return (
    <details>
      <summary>Import Compose from Git</summary>
      <Form
        action={action}
        label="Import Compose from Git"
        onSubmit={(d) =>
          action.run(async () => {
            await result(
              client.PUT(`${root}/source`, {
                params: { path },
                body: {
                  baseRevisionId: revisionId,
                  configuration: sourceConfiguration(d),
                },
              }),
            );
            await imported(
              "Import queued. Configure the push webhook shown when creating the connection. Use Reload current after import.",
            );
          })
        }
      >
        <SourceFields
          workspaceId={path.workspaceId}
          connection={connection}
          runner={runner}
        />
        <AutomaticDeploymentScope />
        <Button type="submit" disabled={dirty || action.isPending}>
          Import source
        </Button>
      </Form>
    </details>
  );
}

// ChangeSource lets an owner bind the environment's source again, with other files, a branch, a
// connection or a runner, as a file renamed upstream needs, or stop importing from Git. Both
// confirm the owner's identity first. changed runs after either succeeds, or after the server
// says the source changed since it was shown, with what to say. Its form starts from the source
// as it was when opened and binds only that: a change made elsewhere since is shown, keeping what
// the owner typed, until they start again from the source as it is.
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
  // shown is the source the form started from. A change made elsewhere since is shown, not taken
  // over what the owner typed; this form's own binding starts it from the source that binding made.
  // Until the page has that source too, replacing is the hash the binding replaced, which the page
  // still showing is no change elsewhere.
  const [view, setView] = useState<{
    shown: Schema["GitSourceStatus"];
    replacing: string | null;
  }>({ shown: source, replacing: null });
  // Once the page shows anything but the replaced source, the binding has settled: its own source,
  // or a change made elsewhere, which the notice then shows.
  if (
    view.replacing &&
    (source.sourceHash === view.shown.sourceHash ||
      source.sourceHash !== view.replacing)
  )
    setView({ shown: view.shown, replacing: null });
  const shown = view.shown;
  const [removing, setRemoving] = useState(false);
  const remove = useRef<HTMLButtonElement>(null);
  // After a removal the page says so and moves focus there, so the dialog doesn't take it back.
  const removed = useRef(false);
  const summary = useRef<HTMLElement>(null);
  return (
    <details>
      <summary ref={summary}>Change or remove the Git source</summary>
      <div className={s.stack}>
        <ErrorNotice error={action.error} />
        {source.sourceHash !== shown.sourceHash &&
          source.sourceHash !== view.replacing && (
            <div className={s.notice} role="status">
              <p>
                The source changed since this form was opened, for example
                because another owner bound it again or turned automatic
                deployment off. Binding it again from this form is refused until
                you start again from the source as it is now.
              </p>
              <Button
                type="button"
                onClick={() => {
                  setView({ shown: source, replacing: null });
                  summary.current?.focus();
                }}
              >
                Start again from the current source
              </Button>
            </div>
          )}
        <BindAgain
          key={shown.sourceHash}
          path={path}
          revisionId={revisionId}
          source={shown}
          dirty={dirty}
          action={action}
          changed={changed}
          bound={(next) =>
            setView((v) => ({
              shown: next,
              // Binding again before the page caught up still replaces what the page shows.
              replacing: v.replacing ?? v.shown.sourceHash,
            }))
          }
        />
        <div>
          <Button
            ref={remove}
            type="button"
            focusableWhenDisabled
            disabled={action.isPending || removal.isPending}
            onClick={() => {
              removal.reset();
              removed.current = false;
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
          <AlertDialogContent
            finalFocus={() => (removed.current ? false : remove.current)}
          >
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
                    removed.current = true;
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

// BindAgain is ChangeSource's form. It starts from the source as shown and sends that source's
// hash, so a change made since is refused rather than overwritten.
function BindAgain({
  path,
  revisionId,
  source,
  dirty,
  action,
  changed,
  bound,
}: {
  path: EnvironmentPath;
  revisionId: string;
  source: Schema["GitSourceStatus"];
  dirty: boolean;
  action: ReturnType<typeof useAction>;
  changed: (message: string) => Promise<void>;
  bound: (source: Schema["GitSourceStatus"]) => void;
}) {
  const connection = useState(source.configuration.connectionId);
  const runner = useState(source.configuration.runnerId);
  return (
    <Form
      action={action}
      label="Change the Git source"
      onSubmit={(d) =>
        action.run(async () => {
          const configuration = sourceConfiguration(d);
          const request = client.PUT(`${root}/source`, {
            params: { path },
            body: {
              baseRevisionId: revisionId,
              sourceHash: source.sourceHash,
              configuration,
            },
          });
          try {
            await result(request);
          } catch (e) {
            // Only a source changed since the form was opened is this form's to explain; any
            // other refusal, such as a running import or a retired environment, says why itself.
            if (
              !(e instanceof ApiError) ||
              !e.detail.fields?.includes("sourceHash")
            )
              throw e;
            await changed(
              "The source was not bound again: it changed or was removed since this form was opened. If it is still bound, your changes are kept; start again from the current source to bind it.",
            );
            return;
          }
          // The form starts again from the source this binding made, whose hash the response
          // names, rather than from the page's next poll, which a change elsewhere could reach first.
          const hash = (await request).response.headers.get(
            "Astilba-Source-Hash",
          );
          if (hash) bound({ ...source, configuration, sourceHash: hash });
          await changed(
            "Source changed. Its import is queued; when it succeeds, use Reload current to load it.",
          );
        })
      }
    >
      <p className={s.muted}>
        Binding the source again imports the branch&rsquo;s current commit,
        replacing the Compose files in the saved draft, and makes automatic
        imports and deployments yours. Deployments wait for that import. Earlier
        revisions stay in history.
      </p>
      <SourceFields
        workspaceId={path.workspaceId}
        connection={connection}
        runner={runner}
        initial={source.configuration}
      />
      <Button type="submit" disabled={dirty || action.isPending}>
        Bind again and import
      </Button>
    </Form>
  );
}
