# Resource history

Open **Servers → Resource history** to inspect an enrolled host. Astilba stores
one report per server per minute for 24 hours, including host CPU and memory,
filesystem capacity, and CPU/memory/state for up to 40 managed services. Select a
one-, six- or 24-hour window. Charts leave gaps when data is missing; the sample
and service tables provide the recorded values without interpolation.

Metrics are read-only. They do not restart workloads, reconcile drift, clean
storage or establish application health. [Operational alerts](operational-alerts.md) use these observations and durable
operation state. Bounded log export remains a separate P9 increment.

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
