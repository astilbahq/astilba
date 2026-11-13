# Retiring an environment

An environment you no longer need can be retired. Retiring removes what it runs on its server and stops everything that would act for it, but keeps its data volumes, configuration and history, so you can restore it later. Environments are never deleted: revisions, plans, operations and audit events refer to them.

When you no longer need what a retired environment kept, you can [purge](#purge) it: its data volumes are deleted and its secret values erased. A purged environment can never be restored. To delete a single secret's values from an environment you keep, [retire the secret](#retire-a-secret).

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
- its applied plan and release records in that server's agent journal. Its finished command records stay under the journal's normal bound, without the secret values, registry access and backup credentials they were sent: every agent drops those from a command once it has finished and management has its result.

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

If the server is offline, the environment shows as **Retiring** and the operation waits for the server. A server that comes back after a week still removes the environment, as long as its agent journal still holds the environment: what it deployed, published or tried to deploy. A server whose journal holds none of it never ran the environment; its retirement is cancelled, and you retire again. Nothing else can run for the environment meanwhile.

If the server cannot finish, for example because ingress was changed outside Astilba or a container's ownership labels do not match, the operation reports what it kept and the environment stays **Retiring**. Inspect the host as described in [the agent guide](agent.md#observe-and-recover). A `needs_attention` operation is retried on the host. Once the operation has failed or been cancelled, retire the environment again to send a new command.

A revoked server can no longer be contacted, and revocation never stopped its containers. Retiring an environment on a revoked server finishes in management and records that the host was not contacted. Remove its containers from that host yourself, as for anything a revoked server ran.

The same applies to the revoked primary of a replacement whose deployment failed. The environment's unit has already moved to the new server, so retirement removes what the attempt left there and records the revoked former primary as not contacted.

## Restore

A retired environment shows a banner with who retired it and when, and the volumes it kept. **Restore** makes it active again, as configuration only: nothing is deployed. Its automation credentials stay revoked, and its preview, terminal-session and backup policies stay off until an owner turns them back on.

The next deployment goes to the same server and reuses the kept volumes. If another environment on that server now serves one of its hostnames or ports, the plan is refused and names it.

## Purge

A retired environment's banner offers owners **Delete retained data**. It lists each data volume the environment kept and asks you to type the environment's exact current name, and to confirm your identity if you have not done so recently. Automation tokens cannot purge. The API is `POST …/environments/{environmentId}/purge`, with `{"confirmName": "<name>", "volumeIds": [...]}`: `volumeIds` must name exactly the volumes the environment keeps, as its volume list shows them, so a purge never deletes data you were not shown. If they have changed, the purge is refused and you confirm the current list. It is recorded in the audit log as `environment.purge`.

Purging cannot be undone:

- every secret value of the environment is erased at once, in management's database. Each secret version's record stays, without its value, because the environment's revisions name it;
- its server deletes the kept volumes, after checking every volume's ownership labels and that no upgrade's retention period protects it. If any check fails, nothing is deleted and the operation needs attention. A volume that a container still uses is kept, and the operation needs attention until you retry it on the host, as described in [the agent guide](agent.md#observe-and-recover);
- the environment becomes **Purged** when the server confirms. It cannot be restored, retired again or changed. Its revisions, configuration export and audit history stay readable.

An environment that kept no volumes is purged at once. A purge is refused while another operation runs for the environment or on its server, while a database upgrade still holds its data, while its server waits for a verified agent transfer, and when its server's agent is too old to purge.

A preview template's previews each hold their own copies of its secret values, from when they are given an environment until their cleanup deletes them. Purging a template is therefore refused, as `retirement.previews`, while any of its previews that was given an environment is not deleted yet. A request that never got one holds nothing and does not hold the purge back. The refusal names each one by its request number, with its state and what it is waiting for, such as a host's verified agent transfer, and the purge dialog lists them and stays disabled until none is left. Wait for each cleanup, or choose **Remove preview and data** on it under **Previews**. The check holds the workspace's write lock, which every change to a preview takes, and a template that is not active starts or revives no previews, so none can appear after the check. Once a template is purged, neither it nor any of its previews holds one of its secret values in management's secret records.

Recovery bases that management stored for cross-host deployments before a purge are sealed snapshots of the whole workspace, and a purge does not rewrite them, so they still hold the secret values they captured. PLAT-353 makes those copies unreadable by destroying a purged environment's own data key. Offline cross-host recovery from such a base can also bring back a deleted preview's secret copies, which PLAT-354 stops.

While its server deletes the volumes, the environment shows as **Purging**, and nothing can be restored. A server that is offline when its purge is due does not run it later: the operation fails once its 30-minute admission deadline passes, and you purge again when the server is back. A purge that failed or was cancelled can be asked for again in the same way. A volume that is already gone counts as deleted.

If the environment's server was revoked, nothing can be sent to it. The purge then erases the secret values and finishes in management, recording that the server was not contacted. Its volumes stay listed, because nothing deleted them; remove them from that host yourself, as for anything a revoked server ran.

Backups of the environment at the workspace's backup destination are not deleted. They stay encrypted, and the destination's own retention governs them.

## Retire a secret

An owner can retire one secret of an active environment from its **Secrets** list, confirming their identity if they have not done so recently. The API is `DELETE …/environments/{environmentId}/secrets/{secretId}`, recorded in the audit log as `secret.retire`. Retiring erases the value of every version of the secret, removes it from the list and frees its name. It cannot be undone.

It is refused while anything that may still deploy names one of the secret's versions: the draft, the applied revision or what the server runs, the revision a preview policy creates previews from, a deployment plan that can still be applied, an operation that has not finished, or a supported upgrade that has not ended. Replace the secret there first, and wait for a plan's 30-minute review window to pass or its operation to finish.

The versions' records stay, because older revisions name them. A draft or plan that names a retired version is refused, as for a version that does not exist.

## Exports and recovery

Workspace exports (archive version 9) carry each environment's retirement and purge, and the operations that removed it and its volumes from its server. An imported environment keeps its state: a retired one stays retired and read-only, the importing owner is recorded as having retired it, and it holds none of the hostnames or ports its retirement released, so nothing claims them until a restored environment's reviewed deployment does. An archive in which such an environment still claims one is refused. One imported while retiring stays retiring; its operation is cancelled as all imported work is, so retire it again once its server's agent has been transferred. The same holds for one imported while purging: purge it again. A purged environment stays purged, and the importing owner is recorded as having purged it. Retired secrets and erased secret versions are exported without values and import as they were. Environments in an archive from version 7 or earlier are imported as active, and nothing in an archive from version 8 or earlier is purged, retired or erased.

Each managed PostgreSQL data volume also records the PostgreSQL major version that initialised it, and a deployment that would start another major on it is refused, as `applications.postgres.upgrade`, even after a restore. Use a supported upgrade instead. A supported upgrade's target volume records its major when the upgrade is planned, and a backup restore's target records the major of the archive restored into it when its cutover is prepared; a cutover is refused as `restore.compatibility` unless the draft's database runs exactly the image that wrote the archive (see [backups](backups.md#restore-and-cutover)).

Controller recovery takes a checkpoint's retirement when the restored management database has never seen its operation, as when the database is from before the retirement, and then completes it if the server confirmed the removal. A database that has seen it keeps its own state, such as a restore made after the checkpoint. When the database is the one holding a retirement, a checkpoint whose environment has reached that retirement's execution generation has seen it, so the checkpoint's state is taken, such as a restore and the work that followed. A retirement that needed no operation, because nothing was deployed or its server was revoked, carries no such order, and the restored database's state stands unless it holds a retirement the checkpoint has passed.

Controller recovery never undoes a purge or an erasure. A checkpoint's purge is taken whenever the restored database's environment has not got as far, with the retirement it followed, and two purges of one environment are ordered by their operations as retirements are. A secret version erased in either the database or the checkpoint ends erased, and so does a retired secret.

Copies of secret values outside the database, such as older management backups, keep them until they expire.
