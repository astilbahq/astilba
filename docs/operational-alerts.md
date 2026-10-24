# Operational alerts

Open **Alerts** to review problems in a workspace. Members can read the inbox;
operators and owners can acknowledge a record. Acknowledgement records that the
problem has been seen and suppresses unsent problem notifications. It does not
restart a service, declare it healthy or resolve a firing incident.

The attention view contains firing incidents and unacknowledged operation
problems. **All history** includes acknowledged and resolved records. Each page
contains at most 100 records. Completed/event history is retained for 30 days,
up to 1,000 records per workspace; firing incidents remain until recovery or an
explicit end to monitoring. Delivery status is visible on each record.

## Detection and recovery

The management process evaluates durable state independently of deployment and
agent-report handling. It checks up to 20 workspaces per minute, oldest checked
first. A delayed or failed alert sweep cannot prevent the agent from executing
work or accepting reports. Detection requires the management application to be
running; it is not an external uptime service.

- **Server disconnected:** an enrolled server has not sent a heartbeat for more
  than two minutes. A heartbeat less than 45 seconds old establishes recovery.
  Revocation or controller transfer ends monitoring explicitly.
- **Filesystem pressure:** three consecutive minute samples show at least 90%
  usage; two below 85% establish recovery. Docker and agent-filesystem readings
  are separate evidence. A fallback reading cannot recover Docker pressure.
- **Service unavailable:** three consecutive samples show a desired service
  stopped, paused or restarting, or absent from a complete inventory. A partial
  or truncated inventory cannot establish absence. An explicit running reading
  establishes runtime recovery.
- **Service health check failed:** three consecutive explicit unhealthy readings.
  Recovery requires an explicit healthy reading. Missing or starting health is
  inconclusive; an explicit removal of the health check ends that monitoring.
- **Scheduled backup failed:** the latest backup-producing outcome failed
  or needs attention, including an interrupted run without a finish time. A
  later successful backup establishes recovery. Restoring
  an older snapshot is not a successful backup and cannot recover this alert.
- **Operation problem:** a deployment, image build or backup operation failed or
  needs attention. One record is created per operation, including when a
  needs-attention operation later fails. This is a historical event, not a claim
  about current service health. Cancellation alone does not create an alert.

Consecutive metric evidence must have no missing minute and the newest reading
must be no more than three minutes old. Unavailable or stale observations do not
prove recovery. Removing a service from the applied specification ends its
monitoring with that reason. Runtime monitoring works with older agents; Docker
health monitoring requires the updated agent. The agent reads only the health
status from Docker inspection when a list response lacks its health summary;
inspection configuration and health-check output are not reported.

Operations already terminal when this feature is installed, and imported
operation history, do not generate retrospective notifications. In-progress
operations remain eligible. Scheduled backup detection uses the latest reported
backup outcome, so a failure followed by recovery entirely during management
downtime may not be notified. No offline notification delivery is promised.

## Optional notifications

An owner with a recent identity confirmation can enable email and/or a signed
webhook. Both start disabled. The inbox works without SMTP or a webhook.

Email goes only to current verified owners. Recipients are checked again before
sending; an unverified or removed owner is skipped. Fan-out is limited to 100
verified owners per transition; exceeding that limit records failed email
instead of sending only a subset. Messages contain a fixed summary and resource
identities, not deployment logs, environment values or operation notes.

Webhook URL and secret are encrypted and write-only. Leave both fields blank to
retain the existing destination, replace both to rotate it, or disable webhooks
to remove it. Public HTTPS destinations are supported; private destinations or
HTTP require an explicit operator origin in `ASTILBA_ALERT_WEBHOOK_ORIGINS`
(comma-separated exact origins). Development mode also allows HTTP loopback.
Each connection resolves and dials the same permitted IP, bypasses environment
proxies and refuses redirects. Response bodies are not stored.

Webhook bodies contain `deliveryId`, `alertId`, `workspaceId`, `resourceId`,
`kind`, `state`, `reason` and `occurredAt`. `Idempotency-Key` and
`X-Astilba-Delivery` contain the stable delivery UUID. Verify
`X-Astilba-Signature` as `sha256=` followed by the hex HMAC-SHA256 of the
`X-Astilba-Timestamp` value, a newline, then the exact request body, using the
configured secret. Receivers should enforce a timestamp tolerance and deduplicate
delivery IDs. A retry retains its delivery ID but receives a fresh signature.

Delivery uses a persisted queue, at most five attempts and a 24-hour expiry.
Attempts have a ten-second network budget; retry delays are 1, 2, 4, 8 and
16 minutes, with no retry after the fifth failure. The delivery worker attempts
at most 20 due records per ten-second cycle, within a 30-second cycle budget.
A crashed lease becomes eligible after 30 seconds and retains its attempt
count. External acceptance followed by a crash can duplicate a notification;
there is no exactly-once SMTP promise.

Changing settings cancels queued deliveries for the old settings revision.
Acknowledging an event or problem suppresses its unsent notifications; a
recovery notification is separate. Recipient and policy changes cannot recall a
message already handed to the network. Failed delivery remains visible and does
not fail the underlying operation.

Alert history, delivery jobs and destination settings are excluded from
workspace transfer archives. Configure notifications explicitly on the new
installation. Full management backups include this database state normally.
