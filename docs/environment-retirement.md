# Retiring an environment

An environment you no longer need can be retired. Retiring removes what it runs on its server and stops everything that would act for it, but keeps its data volumes, configuration and history, so you can restore it later. Environments are never deleted: revisions, plans, operations and audit events refer to them.

Deleting the retained volumes of a retired environment (purge) arrives in a later release. Until then, a retired environment's volumes stay on its server.

## Retire

Open the environment's **Settings** and choose **Retire environment**. Only workspace owners can retire or restore an environment. Each asks you to type the environment's exact current name, and to confirm your identity if you have not done so recently. Automation tokens cannot retire or restore. The API is `POST …/environments/{environmentId}/retire` and `…/restore`, with `{"confirmName": "<name>"}`. Both are recorded in the audit log as `environment.retire` and `environment.restore`.

Retiring immediately:

- revokes the environment's automation credentials, each recorded as `automation.revoke`;
- turns off its pull-request preview policy, if it is a preview template, and asks for each of its live previews to be cleaned up with your authority, as **Remove preview and data** does for one preview. A preview whose cleanup cannot run yet, for example because its server is waiting for a verified agent transfer or your identity changed, shows as blocked; choose **Remove preview and data** on it again under **Previews**, which a retired template still offers its owners;
- turns off its terminal-session setting and closes open sessions;
- turns off its backup policies;
- refuses every new operation for it: deployment plans, builds, imports, automatic deployments, backups, upgrades and terminal sessions. Its draft, secrets and settings become read-only. Reads, revision history and configuration export stay available.

Then its server removes, at a safe checkpoint:

- its routes, so the proxy stops serving its hostnames and stops renewing their certificates;
- its containers, including release-command and scheduled-job helpers, and the networks it created, including Compose networks;
- its backup and job schedules;
- its records in that server's agent journal: its applied plan and release records, and the finished command records of its deployments, builds, backups and upgrades there, which hold the secret values, registry access and backup credentials they were sent. A record whose result management has not yet acknowledged stays until it is delivered. The phase records of an earlier cross-host deployment stay too, because recovery reads them, but lose their secret material, as do the credentials an ended database upgrade kept.

Only the environment's own server is asked to remove anything. A build that ran on a separate build runner left its record, with the build secrets and registry access it was sent, in that runner's journal, where it stays until the runner's normal journal bound ages it out.

Named volumes are kept, with their labels unchanged. When the server confirms, the environment's hostnames and ports are released for other environments, and its state becomes **Retired**. Its secrets keep their encrypted values so that a restored environment can deploy again.

An environment that was never sent to a server has nothing on one and is retired at once. One whose first deployment was applied but failed is assigned to its server, which may hold what that attempt left, so its server is asked to remove it like any other. Pull-request preview environments cannot be retired; they keep their own cleanup.

### When retirement is refused

Retirement does not cancel work or take shortcuts. It is refused, with the reason, when:

- another operation is running for the environment, or on its server;
- a database upgrade still holds its data, or a backup restore cutover is waiting to be deployed;
- it runs on more than one server. Consolidate it onto its primary server, then deploy it without a placement: a consolidated placement still runs as a cross-host deployment;
- its replacement deployment failed and the server it replaced is no longer revoked, so both may run it. Finish the replacement deployment or revoke the former server;
- its server's agent is too old to retire environments, or the server is waiting for a verified agent transfer.

### Offline and revoked servers

If the server is offline, the environment shows as **Retiring** and the operation waits for the server. Retirement has no deadline: a server that comes back after a week still removes the environment. Nothing else can run for the environment meanwhile.

If the server cannot finish, for example because ingress was changed outside Astilba or a container's ownership labels do not match, the operation reports what it kept and the environment stays **Retiring**. Inspect the host as described in [the agent guide](agent.md#observe-and-recover). A `needs_attention` operation is retried on the host. Once the operation has failed or been cancelled, retire the environment again to send a new command.

A revoked server can no longer be contacted, and revocation never stopped its containers. Retiring an environment on a revoked server finishes in management and records that the host was not contacted. Remove its containers from that host yourself, as for anything a revoked server ran.

The same applies to the revoked primary of a replacement whose deployment failed. The environment's unit has already moved to the new server, so retirement removes what the attempt left there and records the revoked former primary as not contacted.

## Restore

A retired environment shows a banner with who retired it and when, and the volumes it kept. **Restore** makes it active again, as configuration only: nothing is deployed. Its automation credentials stay revoked, and its preview, terminal-session and backup policies stay off until an owner turns them back on.

The next deployment goes to the same server and reuses the kept volumes. If another environment on that server now serves one of its hostnames or ports, the plan is refused and names it.

## Exports and recovery

Workspace exports (archive version 7) carry each environment's retirement and the operation that removed it from its server. An imported environment keeps its state: a retired one stays retired and read-only, the importing owner is recorded as having retired it, and it holds none of the hostnames or ports its retirement released, so nothing claims them until a restored environment's reviewed deployment does. An archive in which such an environment still claims one is refused. One imported while retiring stays retiring; its operation is cancelled as all imported work is, so retire it again once its server's agent has been transferred. Environments in an archive from version 6 or earlier are imported as active.

Each managed PostgreSQL data volume also records the PostgreSQL major version that initialised it, and a deployment that would start another major on it is refused, as `applications.postgres.upgrade`, even after a restore. Use a supported upgrade instead. A supported upgrade's target volume records its major when the upgrade is planned, and a backup restore's target records the major of the archive restored into it when its cutover is prepared; a cutover is refused as `restore.compatibility` unless the draft's database runs exactly the image that wrote the archive (see [backups](backups.md#restore-and-cutover)).

Controller recovery takes a checkpoint's retirement when the restored management database has never seen its operation, as when the database is from before the retirement, and then completes it if the server confirmed the removal. A database that has seen it keeps its own state, such as a restore made after the checkpoint. When the database is the one holding a retirement, a checkpoint whose environment has reached that retirement's execution generation has seen it, so the checkpoint's state is taken, such as a restore and the work that followed. A retirement that needed no operation, because nothing was deployed or its server was revoked, carries no such order, and the restored database's state stands unless it holds a retirement the checkpoint has passed.

Copies of secret values outside the database, such as older management backups, keep them until they expire.
