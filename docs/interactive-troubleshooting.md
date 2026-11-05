# Interactive troubleshooting

Status: approved P17 design, 3 November 2026. Increment 1 is implemented, so sessions
work through the API when an operator, an owner and the host owner all enable them; the
dashboard terminal is not built yet. The owner decisions were made after comparing
established platforms and security standards and are collected at the end.

Interactive troubleshooting lets an authorized person open a short-lived terminal
session inside one eligible managed container to diagnose a running service. It
is not a host shell, a deployment mechanism or a general remote-execution API.
The [architecture boundary](platform-architecture.md#boundaries-for-later-capabilities)
applies: sessions use a separate authenticated streaming protocol, polling
remains the only command path, and the operation API gains no shell commands.

## Scope and eligible containers

A session targets one running container that the agent's journaled unit plan
expects as one of:

- an instance of a visual web application or worker;
- the managed PostgreSQL container of a visual environment;
- a service container of an Astilba-managed Compose project.

The agent decides eligibility the way it already decides ownership: from the
journaled plan, the container's kind label (for example `visual-v1`) and its
expected name. It then pins the session to that container ID. It refuses every
other container, including scheduled job, release-command, backup, restore,
upgrade, ingress and pipeline containers, which carry their own kind labels and
hold credentials or host-level data.

Containers are ineligible when a shell in them would effectively be a host shell,
because an exec inherits the container's privileges: privileged containers, added
capabilities, mapped devices, the host's network, process or IPC namespaces,
changed kernel parameters, a mounted Docker socket or any host bind mount other
than the read-only bundle files the agent places itself. Managed Compose already rejects several of these; the agent checks them
again for every session. **Decided:** these containers are never eligible.

The session runs the first available shell of `/bin/bash` and `/bin/sh` with a
TTY, as the container's configured user. Many images run as root inside the
container; the dashboard says which user the shell will run as. Images without
a shell (for example distroless) are refused with an explicit reason.

Sessions need runc 1.3.3 or later on the 1.3 branch, or a final 1.x release from
1.4 onwards. Older branches are refused. These versions include the fixes for the
working-directory escape (CVE-2024-21626) and the November 2025 escapes
(CVE-2025-31133, CVE-2025-52565, CVE-2025-52881), the last of which also hardens the
terminal an exec allocates. Later low-severity advisories were reviewed and do not
raise this floor: CVE-2024-45310 is fixed in every accepted version and
CVE-2026-41579 does not apply under Docker. The runtime the guided bootstrap
installs with `--install-runtime` meets the floor; Ubuntu 24.04's original
`1.1.12-0ubuntu3` package does not, and such hosts report the `runtime` reason.

The agent reads the default runtime's version from the Docker API and compares each
branch's upstream version, allowing a distribution's revision suffix. Any other
runtime, a pre-release, an unknown version, or a container created with another
runtime is refused.

Out of scope for this increment: host shells, build runners, file upload or
download, port forwarding and private-service tunnels, database consoles,
unmanaged containers, non-interactive command execution, recording terminal
contents and multi-instance management relays. Each needs its own boundary and
review.

## Who can open a session

- **Role:** workspace owners and operators. Viewers and automation credentials
  (API, CLI and MCP) can never open, join or observe a session. **Decided:** an
  environment can also restrict sessions to owners. This
  matters most for Git-managed Compose environments, where operators otherwise
  cannot change what runs without a merged source change; a shell bypasses that
  review and exposes the service's environment and volumes.
- **Authentication:** a browser session with recent authentication (the existing
  five-minute window) at the moment of opening, and the workspace's existing
  write requirements: MFA on hosted installations or under the workspace MFA
  policy, and a verified email on hosted installations. Recency is checked only
  when the session opens.
- **Environment setting:** an owner-controlled setting allows sessions in an
  environment. Enabling it or widening it beyond owners requires recent
  authentication and is audited; once owner notifications ship (increment 3), it
also notifies the workspace's owners. It defaults to
  off, is never available for pull-request preview
  environments, and is switched off by workspace import and management recovery,
  as preview and automatic-build policies already are.
- **Host consent:** sessions require the host owner's opt-in, stored in the
  agent journal and changed with an `astilba-agent` subcommand while the agent is
  stopped, like transfer. Opting out ends open sessions. Consent names the
  controller's origin, release key and generation, so transfer, recovery handover
  and re-enrolling with a new key all withdraw it, and it is given again to the
  new controller. The agent reports capability values, added explicitly to the agent
  capability schema: `troubleshooting` (supported), `troubleshootingEnabled`
  (opted in) and, instead of `troubleshooting` when a current agent cannot offer
  sessions, `troubleshootingUnavailable` with the reason `runtime` (a container
  runtime below the floor) or `host` (the host is not ready for deployments). The
  dashboard can therefore tell "update the agent", "update the container runtime",
  "fix this host" and "the host owner has not enabled sessions" apart. Opt-in is consent to a new
  access channel, not containment: a compromised management service can already
  deploy arbitrary images with an environment's secrets and volumes. It does mean
  sessions never appear on a host whose owner did not choose them, and the agent
  enforces eligibility, timeouts and limits itself. **Decided:** agent-side
  opt-in, default off.
- **Subscription state:** hosted workspaces past subscription expiry cannot
  open sessions, matching the existing expiry restriction on management changes.

## Session lifecycle

1. **Request.** The browser asks to open a session for an environment service
   and, for replicated applications, one instance. Management checks
   authorization and the host's reported capabilities, then records a session row
   in PostgreSQL: workspace, environment, unit, server, target, actor, the
   actor's `auth_version`, a hash of the browser session token, controller
   generation, state and a deadline at the earlier of 60 minutes and the browser
   session's absolute expiry. It records an audit event and returns a single-use
   stream ticket valid for 30 seconds, bound to the account, browser session,
   workspace and session row.
2. **Offer.** The poll response to that server includes an offer, separate from
   journaled operation commands. The offer field is sent only to agents that
   report `troubleshooting`, which they do only after the v2 feature header, so
   older agents, which reject unknown response fields, are unaffected. An offer
   wakes the poll but holds back no command. Offers are never replayed after their row leaves the
   offered state.
3. **Agent stream.** The agent checks its opt-in, the eligibility rules, its
   local limits and the controller generation, then opens an outbound stream on
   a dedicated connection separate from its polling client. Management
   authenticates the server credential and the session before accepting the
   upgrade, and admits the stream only if the session belongs to that server, is
   still offered, has not been attached and the generation is current. A refusal
   is reported as a typed reason: opt-in off, runtime below the floor, ineligible
   container, no shell, limit reached, or Docker unavailable. Until the session has
   started, a failure is reported this way too, and any shell already started is ended.
4. **Browser stream.** The browser opens a same-origin stream. Management
   refuses the upgrade unless the session cookie and Origin are valid, the account
   is an owner or operator of the workspace, and the limits on streams without a
   redeemed ticket leave room, then
   requires the ticket as the first message within five seconds. Redeeming the
   ticket re-checks membership and allows one browser attachment per session.
   A redemption that cannot finish within five seconds refuses the stream and
   leaves the ticket unused, unless its commit was already on its way to
   PostgreSQL. That can use up the ticket, and the session then closes once the
   ticket expires.
5. **Exec.** Once management confirms that the browser stream is attached, it
   tells the agent to start the shell, so no shell runs before a person is
   connected. The agent starts it through the Docker exec API with a TTY and the
   working directory fixed at `/`,
   `TERM=xterm-256color` and no other injected environment, exec `Privileged`
   false and no user override. It journals the exec ID and the shell's process
   identity, but not terminal content, so it can clean up after a crash.
6. **End.** A session ends on explicit close, after 15 minutes without typed input,
   at its deadline or when either stream drops. Typed input also refreshes the
   browser session's idle timer, at most once a minute, so a person working in
   the terminal is not logged out of the dashboard mid-session. The relay can only extend
   the stored session, so the dashboard terminal also requests the session at most once a
   minute while the person types, which reissues the browser's cookie. The agent enforces the idle
   timeout and deadline itself as well, measuring both as durations from
   when the shell starts so clock differences between management and the host do not
   matter. It ends every process in the shell's
   Linux session, not just its process group, because interactive shells place
   jobs in their own groups. It identifies those processes by the boot, the start time and
   the container's PID namespace, never by a bare host PID that could be reused,
   signals them through a pidfd, and confirms by re-reading the session's processes.
   Zombies have already been killed and do not count. A cleanup failure is logged and
   retried at the next start; it never stops the agent. If the shell has exited and
   the agent stays down until Docker has forgotten the exec (minutes), the entry is
   dropped with a warning, because its PID can no longer be trusted, and any background
   job survives until the container restarts, like the `setsid` case below. At startup the agent
   ends any journaled shell left by a crash. Only typed input counts as activity: output
   and resizes do not, so a terminal left printing logs cannot stay
   open indefinitely. The dashboard warns a minute before either limit.
   **Decided:** 15-minute idle timeout and 60-minute maximum, in line with the
   15-minute idle limits recommended by NIST SP 800-63B (AAL3) and required by
   PCI DSS, and with Heroku Exec's one-hour cap.

Management also ends a session when:

- the actor loses the role or membership;
- the actor's `auth_version` changes (password reset, MFA enrolment, or use of
  a recovery code);
- the browser session is logged out, idles out or reaches its absolute expiry. Only the
  reauthentication endpoint, for the same account, moves the binding to the
  renewed token. The session store defers deleting the old row during that
  renewal; when the renewed session commits, it deletes the old row, inserts the
  new one and moves the binding in one transaction, so a re-check never finds
  neither. Logging in again, changing the password, confirming MFA or using a
  recovery code does not move the binding;
- the workspace MFA policy is enabled for an actor without MFA;
- the environment setting is turned off, or an owner-only restriction is turned
  on for an operator's session;
- the agent credential is revoked or rotated, or the server is revoked;
- the subscription expires.

The relay re-reads all of these conditions from PostgreSQL every five seconds, so
a change made by any request or worker ends the session promptly without a separate
notification channel. It re-reads them once more before telling the agent to start
the shell, because the first stream may have waited up to a ticket's lifetime. A session
whose conditions cannot be read for 30 seconds ends, since it may no longer be allowed.
An owner can end any open session in the workspace, or all of
them at once.

Ending a session needs only membership and that role: not the write requirements of MFA,
a verified email or an active subscription, because stopping access must not be harder
than granting it. The person who opened a session can end their own, and an owner can
end anyone's or all of them. The request still needs a browser session and its CSRF
token, and the close is audited.

A user can still deliberately start a process in a new Linux session (for
example with `setsid`). Such processes can survive the terminal until the
container restarts, and changes made inside the container or its volumes
persist. The dashboard and audit history therefore mark a container as touched
by a session and offer a restart.

## Limits

Management enforces limits transactionally in PostgreSQL, and the agent enforces
its own limits locally. Initial values:

- one session per container, four per host, two per actor, eight per workspace
  and 64 per installation;
- 64 KiB/s of input and 1 MiB/s of output per session, with backpressure rather
  than unbounded buffering. If a shell stops reading and more input arrives than the
  agent can hold (about 1 MiB), the session ends with the reason `input_overflow`,
  because dropping part of it could run a different command from the one sent;
- four browser streams per account, eight per workspace and 64 per installation
  that have not yet redeemed a ticket, counted in the management process, which
  sessions already require to be a single instance. Only an owner's or operator's
  stream counts against a workspace; a viewer gets a 403 and anyone else a 404. An
  upgrade over any of these limits gets a 429 before it happens and writes no audit
  event, so a refusal costs no more than any other refused request. A browser cannot
  read that status and sees only a failed connection.

These are constants, not settings; implementation and measured use can tune
them. Requests over a limit are refused with an explicit reason rather than
queued. [Hosting](hosting.md#per-address-limits-at-the-reverse-proxy) asks operators
to limit connections per address at the proxy, which also covers unauthenticated
requests.

## Transport and deployment

Each side uses a WebSocket. Frames are binary, at most 32 KiB, uncompressed and
read with an explicit per-message limit (the existing 16 KiB agent request limit
does not apply to a hijacked connection). Message types are input, output,
resize (with bounded dimensions), ping, refused and exit. Stream handlers clear
the server's ordinary read and write deadlines and send pings every 30 seconds.
The relay ends both streams together with a close handshake, so the exit or
refused frame reaches the browser while it is still sending input. Frames read
after a session has ended are discarded. Every close handshake, on management
or the agent, including one that refuses a ticket or ends a side whose other
side never attached, drops a peer that has not finished it within 10 seconds.

P17 supports single-instance management installations only: both streams for a
session must reach the instance that holds them, and the existing instances share
state only through PostgreSQL, with no authenticated path between instances.
Management cannot reliably detect how many instances share its database, so an
operator enables sessions with an explicit installation setting,
`ASTILBA_TROUBLESHOOTING=single-instance`, which declares that one instance
serves this installation. It is off by default; without it, management reports
sessions as unavailable. [Hosting](hosting.md) documents the setting beside the
proxy requirement. Multi-instance relays need a separate design covering
instance discovery, authenticated and encrypted forwarding, and where checks run.
**Decided:** single-instance only for P17; a multi-instance relay is a separate
design.

Self-hosted installations need their reverse proxy to forward WebSocket
upgrades, allow an idle timeout of at least 35 seconds, above the 30-second ping
interval, and not cap a stream's total duration below 60 minutes. This is a new deployment
requirement for existing installations; [hosting](hosting.md) and the
[agent guide](agent.md) document it, and the native suite runs a session through
the documented nginx, Caddy and Traefik configurations. **Decided:** accept the new
proxy requirement.

**Decided:** new dependencies, pinned exactly under the existing
dependency rules: `github.com/coder/websocket` for the management service and
agent (maintained, and rejecting cross-origin handshakes and oversized messages
by default), and xterm.js for the dashboard terminal.

## Terminal safety and accessibility

Terminal output comes from the workload and is untrusted. The terminal keeps
window-manipulation and report sequences off, enables no clipboard-write
extension, opens only `http` and `https` links and asks before following them,
and takes no other automatic action on escape sequences. The dashboard warns that
pasted text is visible to processes in the container.

The terminal captures keys such as Tab and Escape, so it offers a documented key
and a visible control to move focus out (WCAG 2.2 success criterion 2.1.2, no
keyboard trap). It uses the terminal component's screen-reader mode, a minimum
contrast ratio of 4.5 and visible focus and reduced-motion behaviour.

## Audit, visibility and privacy

A session table records the target, actor, server, close reason, duration and
byte counts in each direction. `session.open` and `session.close` audit events
reference it, and owners can review session history. Refused attempts are
audited too, coalesced to one event per person and reason each hour so that a
member cannot flood the audit log. If the opening audit event cannot be written, the session does not
open. Management writes a
`session.close` with reason `management restart` for sessions left open by a
restart.

The `session.open`, `session.close` and refusal audit events travel with the workspace
archive, like all audit history. The session table stays with the installation: a new
archive table would enter the offline recovery path for little gain, so a transfer
keeps who opened which session and when, but not close reasons or byte counts.

The agent also writes a local journald record of each session (session ID, actor
ID as supplied, container ID, start, end and reason), giving the host owner a
trail outside the management database. After a crash there is no end record; the
restarted agent records its cleanup, with the session and container IDs.

Terminal input and output are not recorded or logged by default: they can
contain secrets and customer data. Management logs only metadata. Recording would
be a separate, opt-in decision with its own retention and access rules.

Owners and operators see open sessions on the service. The environment event
stream's snapshot includes session state so the indicator refreshes, and the
dashboard fetches open sessions from an authorized endpoint; viewers see neither.

**Decided:** owner inbox notifications for sessions opened by operators and for
enabling or widening the environment setting, on by default.

## Considered and deferred

- **Recording terminal content:** few comparable platforms do it, NIST places
  session-content recording (AU-14) in no baseline, and a recording would be a
  new store of secrets and customer data. Revisit only with explicit opt-in,
  retention and access rules.
- **Just-in-time approval:** commonly suggested for SOC 2 programmes but not
  required; owner notification and audit cover review for this increment.
- **Read-only sessions:** a shell cannot be meaningfully read-only; log and
  observation views already provide read-only diagnosis.

## Failure and recovery

- **Management outage or restart:** no new sessions. Open streams drop, and the
  agent ends each shell's session. Workloads are unaffected. If a stream outlives a
  database fault, management retries recording its end until the sweep takes over a
  minute after the deadline, so the row does not hold its limits.
- **Agent restart, crash or upgrade:** open sessions end; at startup the agent
  ends any shells it journaled.
- **Controller transfer:** transfer is a host-local step, so sessions end when
  the source controller revokes the server; transfer withdraws the opt-in, and the
  agent refuses offers from any controller generation other than its current one.
- **Management restore:** session rows are never restored as active.
- **Older agents without the capability:** the dashboard explains that the host's
  agent must be updated; for a runtime below the floor, that the container
  runtime must be updated.

## Verification

- **Unit:** the permission decision table (role, owner-only restriction, recent
  authentication at open, MFA and email-verification requirements, environment
  setting, preview environments, subscription state), recent authentication and
  audit for enabling or widening the environment setting, ticket binding, single
  use and expiry, deadline calculation and limit accounting.
- **Integration**, against real PostgreSQL and Docker:
  - an end-to-end session through the fixture agent;
  - sessions are unavailable without `ASTILBA_TROUBLESHOOTING=single-instance`,
    and no shell starts before the browser stream attaches;
  - a stolen agent credential cannot attach to another server's session, and a
    ticket from another account, browser session or workspace is refused;
  - agent opt-out, ineligible container kinds, shell-less images and each
    host-equivalent container property (privileged mode, added capabilities,
    devices, host network, PID or IPC namespace, kernel parameters, a Docker
    socket and other host bind mounts) are refused with typed reasons, and
    refused attempts are audited;
  - role removal, `auth_version` changes, logout, credential revocation, the
    owner-only restriction, browser-session idle expiry, the deadline and an
    owner ending all sessions end an open session promptly, and
    reauthentication without a recovery code does not, including a re-check during
    the renewal;
  - stream loss and agent crashes leave no process in the shell's Linux session,
    including background jobs;
  - a browser that leaves after sending at most a few frames, or an actor who loses
    access, while waiting for the agent gets no shell, and a session ends when its conditions cannot be read but survives
    a brief fault;
  - a database fault as a session ends does not leave its row holding limits or lose
    its byte counts;
  - frame size, rate and session limits;
  - only typed input resets the idle timer, the exec working directory is `/`,
    hosts below the runc floor report the `runtime` reason, and a failed audit
    write prevents the session from opening.
- **Browser:** Chromium and WebKit journeys for opening, using with the keyboard,
  leaving the terminal by keyboard and closing a session, the warning before a
  limit, link handling limited to confirmed http and https links and ignored
  window-manipulation and report sequences, with axe checks,
  screen-reader mode, visible focus and reduced motion; and a check that the
  page's content security policy permits the same-origin `wss:` connection.
- **Proxy:** the native suite runs real sessions through pinned nginx, Caddy and
  Traefik, using the configurations in [hosting](hosting.md), with the production
  agent's streams and polls on the proxy's HTTPS origin. It covers the upgrade, input,
  resize, the warning, the relay's idle limit past a proxy's own timeouts, the `Exit`
  frame, the close handshake, a dropped browser and cleanup after each. nginx also runs
  at a 35-second limit, which holds, and below it and without upgrade headers, which
  end the session early and start no shell respectively.
  `make verify-troubleshooting-soak` runs the real 15-minute and 60-minute limits on
  demand. On 4 November 2026 it passed on arm64 through all three proxies. That VM's
  monotonic clock ran about 2% fast, so its 60-minute maximum lasted about 58m44s by the
  wall clock. Chromium and WebKit each completed a session through each proxy once that
  day, opening the stream over HTTP/1.1 even from a page loaded over HTTP/2; that is
  not in CI until the dashboard terminal's journeys run through a proxy.
- **Native hosts:** the native suite runs a session under the production unit on amd64
  and arm64, through the documented commands. Exec and cleanup work under its sandbox,
  journald records the session without its content, and no job survives the shell's
  exit, a service stop or a killed agent once systemd restarts it. The guided bootstrap's
  runtime meets the runc floor.

## Increments

1. Session table, permissions, audit, agent capabilities and opt-in, eligibility,
   the streaming relay and exec, verified without a dashboard terminal.
2. Dashboard setting, terminal, session history and the touched-container
   indicator.
3. Owner notifications and limit tuning from measured use.

## Decisions

Decided on 3 November 2026:

1. Agent-side opt-in, default off.
2. Environment setting, default off, plus an optional owners-only restriction.
3. Containers whose shell would effectively be a host shell are never eligible.
4. 15-minute idle timeout (typed input only) and 60-minute maximum.
5. Single-instance management only for P17, with a multi-instance relay as a
   separate design.
6. The new reverse-proxy requirement for WebSocket upgrades and stream duration.
7. New pinned dependencies: `github.com/coder/websocket` and xterm.js.
8. Owner notifications for operator-opened sessions and environment-setting
   changes, on by default.
