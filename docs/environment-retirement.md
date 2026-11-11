# Retiring an environment

An environment you no longer need can be retired. Retiring removes what it runs on its server and stops everything that would act for it, but keeps its data volumes, configuration and history, so you can restore it later. Environments are never deleted: revisions, plans, operations and audit events refer to them.

Deleting the retained volumes of a retired environment (purge) arrives in a later release. Until then, a retired environment's volumes stay on its server.

## Retire

Open the environment's **Settings** and choose **Retire environment**. Only workspace owners can retire or restore an environment. Each asks you to type the environment's exact current name, and to confirm your identity if you have not done so in the last five minutes. Automation tokens cannot retire or restore. The API is `POST …/environments/{environmentId}/retire` and `…/restore`, with `{"confirmName": "<name>"}`. Both are recorded in the audit log as `environment.retire` and `environment.restore`.

Retiring immediately:

- revokes the environment's automation credentials, each recorded as `automation.revoke`;
- turns off its pull-request preview policy, if it is a preview template, and asks for each of its live previews to be cleaned up with your authority, as **Clean up** does for one preview;
- turns off its terminal-session setting and closes open sessions;
- turns off its backup policies;
- refuses every new operation for it: deployment plans, builds, imports, automatic deployments, backups, upgrades and terminal sessions. Its draft, secrets and settings become read-only. Reads, revision history and configuration export stay available.

Then its server removes, at a safe checkpoint:

- its routes, so the proxy stops serving its hostnames and stops renewing their certificates;
- its containers, including release-command and scheduled-job helpers, and the networks it created, including Compose networks;
- its backup and job schedules;
- its records in the agent's journal, which hold its deployment plans.

Named volumes are kept, with their labels unchanged. When the server confirms, the environment's hostnames and ports are released for other environments, and its state becomes **Retired**. Its secrets keep their encrypted values so that a restored environment can deploy again.

An environment that was never deployed has nothing on a server and is retired at once. Pull-request preview environments cannot be retired; they keep their own cleanup.

### When retirement is refused

Retirement does not cancel work or take shortcuts. It is refused, with the reason, when:

- another operation is running for the environment, or on its server;
- a database upgrade still holds its data, or a backup restore cutover is waiting to be deployed;
- it runs on more than one server. Consolidate it onto its primary server first;
- its server's agent is too old to retire environments, or the server is waiting for a verified agent transfer.

### Offline and revoked servers

If the server is offline, the environment shows as **Retiring** and the operation waits for the server. Retirement has no deadline: a server that comes back after a week still removes the environment. Nothing else can run for the environment meanwhile.

If the server cannot finish, for example because ingress was changed outside Astilba or a container's ownership labels do not match, the operation reports what it kept and the environment stays **Retiring**. Inspect the host as described in [the agent guide](agent.md#observe-and-recover). A `needs_attention` operation is retried on the host. Once the operation has failed or been cancelled, retire the environment again to send a new command.

A revoked server can no longer be contacted, and revocation never stopped its containers. Retiring an environment on a revoked server finishes in management and records that the host was not contacted. Remove its containers from that host yourself, as for anything a revoked server ran.

## Restore

A retired environment shows a banner with who retired it and when, and the volumes it kept. **Restore** makes it active again, as configuration only: nothing is deployed. Its automation credentials stay revoked, and its preview, terminal-session and backup policies stay off until an owner turns them back on.

The next deployment goes to the same server and reuses the kept volumes. If another environment on that server now serves one of its hostnames or ports, the plan is refused and names it.

## Exports and recovery

Workspace exports do not yet record an environment's retirement, so an imported retired environment returns as active, without its credentials. A management database restored from before a retirement also brings the environment back as active, although its server no longer runs it. Review its next deployment plan before applying it.

Copies of secret values outside the database, such as older management backups, keep them until they expire.
