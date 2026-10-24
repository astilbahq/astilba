# Resource history

Open **Servers → Resource history** to inspect an enrolled host. Astilba stores
one report per server per minute for 24 hours, including host CPU and memory,
filesystem capacity, and CPU/memory/state for up to 40 managed services. Select a
one-, six- or 24-hour window. Charts leave gaps when data is missing; the sample
and service tables provide the recorded values without interpolation.

Metrics are read-only. They do not restart workloads, reconcile drift, clean
storage or establish application health. [Operational alerts](operational-alerts.md) use these observations and durable
operation state. Bounded log export is available in each environment.

A new signed agent release supplies collection. Older agents remain connected
but have no history until updated explicitly. New agents can talk to older
controllers: rejection of the optional metrics endpoint does not block their
heartbeat, execution or local schedules. Metrics use the existing authenticated
outbound connection; no inbound monitoring port is opened.

Host CPU is averaged between successful `/proc/stat` readings. Host memory uses
`MemTotal - MemAvailable`; it is not a measure of process RSS alone. The Linux
[kernel documentation](https://docs.kernel.org/filesystems/proc.html) describes
these counters. The filesystem reading uses Docker's data-root filesystem when
available; otherwise it identifies the agent filesystem fallback. Used capacity
includes reserved blocks unavailable to ordinary workloads. At 90% usage the
UI shows an informational disk-pressure warning, with no automatic cleanup.
Container CPU uses Docker counter deltas and is expressed relative to one core,
so it can exceed 100%. Container memory includes accounted cache.

The first CPU delta, failed reads and unsupported counters remain unavailable,
not zero. A partial report means collection failed or reached its inventory/time
budget. A sample more than three minutes old is stale; no recent report does not
prove that a service is stopped. The UI records receipt time at management. There
is no offline backfill: controller downtime leaves a visible gap in history.

Collection has a ten-second budget, individual Docker stats reads have a
250-millisecond timeout, and only labelled managed service containers are read.
The API validates their registered unit/server ownership again and rejects
foreign resource references. A separate five-second send budget prevents slow
metrics transport from blocking deployment loops. Reports contain no environment
values, labels, arbitrary paths or application output.

Historical metrics are disposable observations and excluded from workspace
transfer archives. Full installation backups include them as ordinary database
contents. Reads ignore samples older than 24 hours; insertion prunes the reporting
server and the controller's existing retention sweep removes up to 1,000 expired
rows per minute for disconnected servers. Storage is therefore subject to sweep
catch-up after controller downtime. Measure fleet size and storage growth before
claiming hosted capacity.

## Export retained logs

Open an environment's **Export logs** section, choose the last one, six or 24
hours, and select **Download logs**. Workspace members who can read the
environment can download its JSON export. The same nested workspace, project
and environment authorization applies to the API.

The download contains the latest retained runtime samples and retained build
output. Runtime samples are selected by the time management received the
observation; build output is selected by the job's creation time, which can
precede its actual execution. Each entry identifies its source, resource and,
for builds, operation. The file also records the environment and exact requested
window. No server contact or new log collection is triggered by a download.

This is a snapshot export, not a complete historical log archive. Runtime
inspection currently captures up to ten lines and 512 bytes per service within
a shared 2 KiB budget; a newer observation replaces the previous one. Build
output is capped at 16 KiB before storage. Existing retention still applies.
An empty export means no retained output matched, not that nothing happened.

Exports select the newest entries first and include at most 100 entries and
64 KiB of log content. The `truncated` fields identify additional shortening or
omission by the export limits. A false flag does not make the original samples
complete; `coverage` is always `retained-snapshots`. UTF-8 characters are never
split at the content limit. JSON metadata and escaping are additional to the
64 KiB content budget.

The export reuses the agent's existing redaction before sampling and storage.
It does not load command payloads, configuration or credentials. Known runtime
and build secret values are redacted, but applications can emit other private
data, so treat downloaded logs as private. Logs and metrics remain disposable
observations and are not added to workspace transfer archives. This feature
does not change retention or add permanent log storage.
