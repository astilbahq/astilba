import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import { Label } from "../components/label";
import { Anchor, Link } from "../components/link";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";

const base = "/api/v1/workspaces/{workspaceId}/github-app" as const;
const grantBase = `${base}/authorizations/{authorizationId}` as const;

export function GitHubAppSettings({ workspaceId }: { workspaceId: string }) {
  const action = useAction();
  const queries = useQueryClient();
  const path = { workspaceId };
  const status = useQuery({
    queryKey: ["github-app", workspaceId],
    queryFn: () => result(client.GET(base, { params: { path } })),
  });
  const connections = useQuery({
    queryKey: ["git-connections", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/git-connections", {
          params: { path },
        }),
      ),
  });
  return (
    <section className={s.stack} aria-label="GitHub App">
      <h3>Connect with the GitHub App</h3>
      <ErrorNotice error={status.error ?? connections.error ?? action.error} />
      {status.isPending ? (
        <p role="status">Checking GitHub App setup…</p>
      ) : status.data?.configured ? (
        <>
          <p>
            Authorize {status.data.name} on GitHub, then choose a repository.
            You need repository administrator access and a recent Astilba
            identity confirmation.
          </p>
          <Button
            type="button"
            variant="primary"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                const v = await result(
                  client.POST(`${base}/authorizations`, { params: { path } }),
                );
                location.assign(v.authorizationUrl);
              })
            }
          >
            {action.isPending ? "Opening GitHub…" : "Continue with GitHub"}
          </Button>
        </>
      ) : status.error ? (
        <Button type="button" onClick={() => void status.refetch()}>
          Retry GitHub setup
        </Button>
      ) : (
        <p className={s.muted}>
          The operator hasn’t configured a GitHub App. You can still connect
          with scoped credentials below.
        </p>
      )}
      {connections.data
        ?.filter((c) => c.authentication === "github_app")
        .map((c) => (
          <div key={c.id} className={s.stack}>
            <p>
              <strong>{c.name}</strong> · {c.repository}
              <br />
              {c.status === "connected"
                ? "Connected through GitHub App"
                : "GitHub authorization required — continue with GitHub to reconnect this repository."}
            </p>
            {c.status === "connected" && (
              <Button
                type="button"
                disabled={action.isPending}
                onClick={() =>
                  action.run(async () => {
                    await result(
                      client.POST(
                        "/api/v1/workspaces/{workspaceId}/git-connections/{connectionId}/disconnect",
                        { params: { path: { ...path, connectionId: c.id } } },
                      ),
                    );
                    await queries.invalidateQueries({
                      queryKey: ["git-connections", workspaceId],
                    });
                  })
                }
              >
                Disconnect {c.name}
              </Button>
            )}
          </div>
        ))}
    </section>
  );
}

// Strip OAuth values before session loading or rendering. They stay in this
// document's memory, never browser storage or a navigation link.
const callback = (() => {
  if (location.pathname !== "/github/callback") return undefined;
  const q = new URLSearchParams(location.search);
  history.replaceState(history.state, "", location.pathname);
  return {
    code: q.get("code") ?? "",
    state: q.get("state") ?? "",
    denied: q.has("error"),
  };
})();

export function GitHubCallback() {
  const action = useAction();
  const started = useRef(false);
  const [grant, setGrant] = useState<Schema["GitHubAuthorization"]>();
  useEffect(() => {
    if (
      started.current ||
      !callback?.code ||
      !callback.state ||
      callback.denied
    )
      return;
    started.current = true;
    action.run(async () =>
      setGrant(
        await result(
          client.POST("/api/v1/github-app/complete", {
            body: { code: callback.code, state: callback.state },
          }),
        ),
      ),
    );
  }, [action]);
  return (
    <section className={s.stack}>
      <h1>Connect a GitHub repository</h1>
      <ErrorNotice error={action.error} />
      {grant ? (
        <GitHubSelection grant={grant} />
      ) : (
        <>
          <p role="status">
            {action.isPending
              ? "Confirming your GitHub authorization…"
              : "Authorization could not be completed. Return to your project and start the GitHub connection again."}
          </p>
          <Link to="/">Return to projects</Link>
        </>
      )}
    </section>
  );
}
function GitHubSelection({ grant }: { grant: Schema["GitHubAuthorization"] }) {
  const action = useAction();
  const [installation, setInstallation] = useState("");
  const [repository, setRepository] = useState("");
  const [installationPage, setInstallationPage] = useState(1);
  const [repositoryPage, setRepositoryPage] = useState(1);
  const [connected, setConnected] = useState(false);
  const path = { workspaceId: grant.workspaceId, authorizationId: grant.id };
  const status = useQuery({
    queryKey: ["github-app", grant.workspaceId],
    queryFn: () =>
      result(
        client.GET(base, {
          params: { path: { workspaceId: grant.workspaceId } },
        }),
      ),
  });
  const connections = useQuery({
    queryKey: ["git-connections", grant.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/git-connections", {
          params: { path: { workspaceId: grant.workspaceId } },
        }),
      ),
  });
  const installations = useQuery({
    queryKey: ["github-installations", grant.id, installationPage],
    queryFn: () =>
      result(
        client.GET(`${grantBase}/installations`, {
          params: { path, query: { page: installationPage } },
        }),
      ),
    retry: false,
  });
  const repositories = useQuery({
    queryKey: ["github-repositories", grant.id, installation, repositoryPage],
    enabled: !!installation,
    queryFn: () =>
      result(
        client.GET(`${grantBase}/repositories`, {
          params: {
            path,
            query: {
              installationId: Number(installation),
              page: repositoryPage,
            },
          },
        }),
      ),
    retry: false,
  });
  const selected = repositories.data?.find((r) => String(r.id) === repository);
  if (connected)
    return (
      <div role="status">
        <p>
          Repository connected. Existing build and deployment approval policies
          are unchanged.
        </p>
        <Link to="/" search={{ workspace: grant.workspaceId }}>
          Return to projects
        </Link>
      </div>
    );
  return (
    <>
      <p>
        Authorized as <strong>{grant.user.login}</strong>. Choose an
        installation and a repository you administer.
      </p>
      <p className={s.muted}>
        Connecting delegates access to this workspace’s App bot. It continues if
        your own GitHub access changes. Disconnect here or remove the App
        installation to stop future access.
      </p>
      <p>
        Authorization expires at{" "}
        {new Date(grant.expiresAt).toLocaleTimeString()}. If identity
        confirmation is needed,{" "}
        <Link to="/account" target="_blank" rel="noopener noreferrer">
          confirm in Account security
        </Link>
        , then retry here.
      </p>
      <ErrorNotice
        error={
          action.error ??
          installations.error ??
          repositories.error ??
          status.error ??
          connections.error
        }
      />
      {status.data?.installUrl && (
        <Anchor
          href={status.data.installUrl}
          target="_blank"
          rel="noopener noreferrer"
        >
          Install the App or change its repository access on GitHub
        </Anchor>
      )}
      <Button
        type="button"
        disabled={installations.isFetching}
        onClick={() => {
          setInstallation("");
          setRepository("");
          void installations.refetch();
        }}
      >
        Refresh installations
      </Button>
      {installations.isPending ? (
        <p role="status">Loading installations…</p>
      ) : !installations.data?.length ? (
        <p>
          No installations on this page. Install the App on GitHub, then refresh
          this list.
        </p>
      ) : (
        <Label>
          GitHub installation
          <Select
            value={installation}
            onChange={(e) => {
              setInstallation(e.target.value);
              setRepository("");
              setRepositoryPage(1);
            }}
          >
            <option value="">Choose an installation</option>
            {installations.data.map((i) => (
              <option key={i.id} value={i.id} disabled={!i.available}>
                {i.account.login}
                {i.available ? "" : " — installation unavailable"}
              </option>
            ))}
          </Select>
        </Label>
      )}
      <div className={s.row}>
        <Button
          type="button"
          disabled={installationPage === 1 || installations.isFetching}
          onClick={() => {
            setInstallationPage((p) => p - 1);
            setInstallation("");
          }}
        >
          Previous installations
        </Button>
        <span>Page {installationPage}</span>
        <Button
          type="button"
          disabled={
            installationPage === 100 ||
            installations.data?.length !== 100 ||
            installations.isFetching
          }
          onClick={() => {
            setInstallationPage((p) => p + 1);
            setInstallation("");
          }}
        >
          Next installations
        </Button>
      </div>
      {installation && (
        <>
          {repositories.isPending ? (
            <p role="status">Loading repositories…</p>
          ) : (
            <Label>
              GitHub repository
              <Select
                value={repository}
                onChange={(e) => setRepository(e.target.value)}
              >
                <option value="">Choose a repository</option>
                {repositories.data?.map((r) => (
                  <option
                    key={r.id}
                    value={r.id}
                    disabled={!r.permissions.admin}
                  >
                    {r.full_name}
                    {r.permissions.admin
                      ? ""
                      : " — administrator access required"}
                  </option>
                ))}
              </Select>
            </Label>
          )}
          {repositories.data?.length === 0 && (
            <p>
              No repositories on this page. Check the App’s access on GitHub,
              then refresh.
            </p>
          )}
          <div className={s.row}>
            <Button
              type="button"
              disabled={repositoryPage === 1 || repositories.isFetching}
              onClick={() => {
                setRepositoryPage((p) => p - 1);
                setRepository("");
              }}
            >
              Previous repositories
            </Button>
            <span>Page {repositoryPage}</span>
            <Button
              type="button"
              disabled={
                repositoryPage === 100 ||
                repositories.data?.length !== 100 ||
                repositories.isFetching
              }
              onClick={() => {
                setRepositoryPage((p) => p + 1);
                setRepository("");
              }}
            >
              Next repositories
            </Button>
            <Button
              type="button"
              disabled={repositories.isFetching}
              onClick={() => void repositories.refetch()}
            >
              Refresh repositories
            </Button>
          </div>
        </>
      )}
      {selected && (
        <Form
          action={action}
          label="Connect GitHub repository"
          onSubmit={(d) =>
            action.run(async () => {
              await result(
                client.POST(`${grantBase}/connections`, {
                  params: { path },
                  body: {
                    name: text(d, "name"),
                    repository: selected.full_name,
                    repositoryId: selected.id,
                    installationId: Number(installation),
                    ...(text(d, "connection")
                      ? { connectionId: text(d, "connection") }
                      : {}),
                  },
                }),
              );
              setConnected(true);
            })
          }
        >
          <p>
            {selected.full_name} ·{" "}
            {selected.private ? "Private repository" : "Public repository"}
          </p>
          <Field name="name" label="Connection name" />
          <Label>
            Connection
            <Select name="connection">
              <option value="">Create a new connection</option>
              {connections.data
                ?.filter(
                  (c) =>
                    c.authentication === "github_app" &&
                    c.repositoryId === selected.id &&
                    c.repository === selected.full_name &&
                    c.private === selected.private,
                )
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    Reconnect {c.name}
                  </option>
                ))}
            </Select>
          </Label>
          <BusyButton busy={action.isPending}>
            Connect selected repository
          </BusyButton>
        </Form>
      )}
    </>
  );
}
export function GitHubInstalled() {
  return (
    <section className={s.stack}>
      <h1>GitHub App installed</h1>
      <p>
        Return to the repository connection tab and refresh installations. An
        installation alone does not connect a repository to an Astilba
        workspace.
      </p>
      <Link to="/">Return to projects</Link>
    </section>
  );
}
