# Compose deployments

Choose **Compose files** when creating an environment. Its source files are authoritative: the graph is a derived view, and moving nodes only saves layout. Ownership cannot be changed by saving a revision. One Compose project deploys as one unit on one enrolled server.

## Edit and review

The file editor keeps comments, anchors, custom `x-*` extensions and supporting files exactly as entered. Add each supporting file to the bundle, and list Compose entrypoints in merge order. Paths are relative to the first entrypoint's directory; included projects resolve from their own directory. Only explicit local `include` paths are supported. There is no implicit `.env`, host filesystem access or management-process interpolation.

Validation reports a file, line and column where available. The last valid graph stays visible while source has errors. Select a service and preview an image change to see its precise source diff before staging it. Anchors, aliases, merges, interpolated images and other ambiguous edits stay in the file editor. Save a revision, prepare a plan and review the frozen image identities and host permissions before applying. Saving and planning do not deploy.

Use **Interpolation values** for plain configuration or immutable secret-version references. Runtime secrets belong in the secret store. Source files, including environment and configuration files, are visible to workspace members and included in exports; never paste secret values into them. A Compose secret uses `environment: VARIABLE_NAME`, with that variable pointing to a secret version. File-based secret declarations are rejected. Cloning a source with secret references requires explicitly copying those secrets with recent authentication. Sources with `x-astilba.routes` cannot be cloned automatically: copy their bundle into a new Compose environment and explicitly change the routes first.

Git origin metadata records an exact commit. See [Git and build workflows](git-builds.md) for provider import, authoritative branch refresh and batched pull/merge request submission. Git-managed edits must merge before deployment.

## Supported runtime

Services use existing images with commands, entrypoints, environment, labels, health checks, restart policy, resource limits and required `service_started` or `service_healthy` dependencies. Networks use the bridge driver, optionally internal; named volumes use the local driver. Services can mount named volumes and read-only regular files supplied in the bundle. Configs support bundled files or inline content, and secrets support explicit secret-version variables. Config/secret ownership and mode overrides are unsupported.

Published ports must be fixed TCP or UDP ports from 1024 to 65535, excluding reserved port 8080, bound to loopback or all interfaces. Published ports, privileged mode and added Linux capabilities appear in the reviewed plan and require an owner with recent authentication. Host networking, host paths, writable binds, Docker sockets, external resources, custom resource names/drivers, static IPs, build contexts, profiles, optional/completed-job dependencies and lifecycle hooks are rejected. Unsupported properties produce errors instead of being removed from saved files.

The bundle limit is 16 UTF-8 files, eight entrypoints and 64 KiB of total source, with up to 20 services/networks and 40 volumes. The complete derived specification must also fit the existing 128 KiB revision limit. The server must advertise Compose support and use the agent's supported Compose version, 5.6.1.

To use managed HTTP/TCP ingress, add root `x-astilba.routes` metadata. Each route accepts `name`, `service`, `protocol`, `domain`, `port`, `listenPort`, `tls`, `allowCidrs` and `advanced`, following the same [visual route rules](visual-deployments.md). For example:

```yaml
services:
  web:
    image: nginx:stable-alpine
x-astilba:
  routes:
    - name: website
      service: web
      protocol: http
      domain: app.example.com
      port: 80
      tls: managed
```

## Runtime identities and export

Service, network and volume identities derive from their source names within the environment. Editing an image preserves its identities; renaming a resource creates a new identity. Removing a service retains its data. Review removals and renames carefully, including any backup attachment, before deployment. Backup policy management arrives in P6. Delete unused data only through the separate destructive volume action, then remove its declaration from the source before deploying again.

The same operation history, logs, stale-plan checks, drift review and interrupted-deployment recovery apply as for visual environments. Supporting files are materialized in the agent's private journal directory and checked against the frozen plan on retry. Keep this directory with the journal during recovery; old revision bundles are retained and contain the source supplied by users. During partial replacement, logs from containers belonging to another plan are withheld until their own revision can supply matching secret values for redaction.

Export the last applied configuration as a ZIP containing unchanged files in `source/`, frozen `compose.json`, `routes.yaml` and instructions. Supply the indicated secret environment variables, create the documented external networks and volumes, and restore data separately. The resulting bundle runs using ordinary Docker Compose without the management application or agent. Export does not include volume data or secret-store values.

## Verification

`make verify` includes real PostgreSQL admission/authorization and Chromium/WebKit source-editing journeys. `make verify-agent-host` adds the native multi-service Compose fixture: source mounts, secret references, interrupted deployment, subsequent file changes, retained PostgreSQL data and standalone export. CI runs the native fixture on amd64 and arm64.
