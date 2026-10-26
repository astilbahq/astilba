# Server and domain onboarding

The Servers page guides preparation, one-use enrollment and capability checks. Supported hosts are Ubuntu 24.04 LTS on amd64 or arm64, with rootful Docker. Workloads continue independently when management is unavailable. Enrollment grants the agent ongoing Docker authority; it does not adopt existing containers or publish an application.

## Prepare before creating an enrollment code

Choose the server role and architecture, then download `/onboarding/bootstrap.sh` from your management origin. Inspect the saved script before running it. `sudo bash astilba-bootstrap.sh --check` only inspects the host; it does not install packages or start services. The manual [agent installation guide](agent.md) remains available for existing or separately prepared hosts.

Apply requires an explicit release digest and independently trusted signing-key fingerprint. The installation operator must distribute that fingerprint through a trusted channel, such as their independently obtained release documentation. The key and fingerprint served by the same management endpoint are useful for comparison, but cannot establish their own authenticity. An operator can compute the SPKI DER fingerprint from their trusted key:

```sh
openssl pkey -pubin -in release-public.pem -outform DER | openssl dgst -sha256
```

The bootstrap checks the key algorithm and fingerprint, detached Ed25519 signature, canonical signed message, protocol, architecture, size and SHA-256 digest before executing the agent. It then compares the compiled identity with the signed release. Downloads have HTTPS, redirect, timeout and size limits.

`--apply` prepares the agent and canonical systemd unit. `--install-runtime` separately permits installing missing Docker Engine/CLI 29.9.2, Compose 5.6.1 and containerd.io 2.4.2 from checksum-pinned official Ubuntu packages. Apt resolves OS dependencies from the host's signed repositories with package removal disabled. Installing the runtime can start Docker and create its networks and firewall rules; see [Docker networking and firewalls](https://docs.docker.com/engine/network/packet-filtering-firewalls/). `--install-backups` adds checksum-pinned Restic 0.19.1. The customer bootstrap installs no compiler, build daemon or registry.

A deployment or combined server also needs `--certificate-contact ADDRESS`. The bootstrap prepares a systemd drop-in passing that address to the agent. Issuance still requires a separately reviewed host-permission plan. Build-only hosts do not require certificate contact. Existing manual overrides continue to work.

The bootstrap refuses existing agent state, incompatible/stopped runtimes and ambiguous partial installations. It does not upgrade or remove an existing runtime, edit SSH settings or replace ingress. A failure preserves installed state and identifies its stage; inspect that state using the manual guide before deciding how to recover. Do not delete the journal to retry.

Once preparation succeeds, create the ten-minute enrollment code. Paste it through the agent's stdin prompt, keeping it out of shell history. Explicitly enable the service afterward. An expired or lost code can be renewed without reinstalling the agent. The dashboard hides codes on dismissal, workspace change or expiry.

Connection status, role capabilities, certificate contact and workload health are separate observations. Older agents may not report certificate-contact configuration. Resolve missing deployment, build or backup capabilities before using those features.

## Optional generated names

Set `ASTILBA_APPLICATION_DOMAIN` on the management installation to a dedicated application domain, such as `apps.example.net`. It must use a different registrable root from the dashboard/account origin. Leave it empty to disable generated names. Custom application routes continue to work.

An owner confirms each deployment server's public IPv4 and/or IPv6 address using recent authentication. Addresses are never inferred from the management connection. Each server uses one wildcard:

```text
*.s<SERVER_UUID_WITHOUT_DASHES>.apps.example.net
```

Create an A record for the supplied IPv4 address and/or an AAAA record for IPv6 in any DNS provider. Individual application names contain a stable hash of workspace, environment and application identities, followed by that server domain. They point directly to the customer's server; Astilba does not proxy traffic. Each managed HTTPS route uses an ordinary per-host certificate, without sharing a wildcard private key.

For automatic Cloudflare records, also configure `ASTILBA_DNS_CLOUDFLARE_ZONE` and `ASTILBA_DNS_CLOUDFLARE_TOKEN`. Use a token scoped to DNS edit/read for that application zone, supplied only in private operator configuration. Neither setting is required for manual DNS. The adapter uses DNS-only A/AAAA records with a 300-second TTL and an installation/server ownership comment. Do not edit that comment or add competing A/AAAA/CNAME/NS records at the generated wildcard. Foreign records are reported for inspection and never overwritten.

Address approval and its queued reconciliation are transactional. Provider calls run outside database transactions under a per-server lock shared with configuration changes and recovery. Lost mutation responses are reconciled by exact name, type, record ID and ownership marker. A bounded sweep rechecks eligible profiles after fifteen minutes. Provider success is an observed snapshot, with a timestamp; it cannot guarantee future DNS state or propagation. An offline/revoked server does not cause automatic DNS deletion, so running apps are not stranded.

Management recovery retains addresses but invalidates external DNS authority and pending jobs. Completing agent transfer does not restore that authority. An owner must review and approve the addresses again. Workspace transfer excludes installation-local DNS profiles, provider ownership and credentials while preserving explicit route strings. Review/reconfigure generated names at the destination; names are not silently rewritten in source files.

## Attach a route or custom domain

In a visual environment, open “Generate an application domain”, choose a saved web service, deployment server and app port, then prepare the name. Inspect it and stage the route. Save the revision and use the normal plan/apply workflow. Requesting a name does not save a draft or grant ingress authority. Changing placement requires explicit regeneration; the API rejects a generated hostname belonging to another workspace, environment, app or server. Automation draft writes and deployment planning enforce the same namespace boundary.

For your own hostname, create an A/AAAA record pointing to the server, or a CNAME to an existing generated application hostname. Stage an HTTP route for that name and review deployment as usual. Compose environments keep their files authoritative: edit the route in the source and follow the existing save/merge workflow.

The Servers page can compare a hostname's DNS answers with approved server addresses. This is a bounded DNS lookup, without management-side HTTP fetching. A match is not ownership verification or a successful TLS/application probe. CDN/proxy answers are unverified. Use application health and the server's certificate/ingress diagnostics separately; managed HTTP certificates need publicly reachable ports 80 and 443.

## Acceptance checks

`make verify` covers namespace validation, real PostgreSQL/API and queue/recovery behavior, and Chromium/WebKit journeys. Provider tests use the actual HTTP boundary with a disposable DNS fixture, including lost creates and foreign records; they are not a live Cloudflare hosting pass.

The native host CI jobs additionally run `TestBootstrapOnDisposableHost` on amd64 and arm64 before the existing host suites. Its destructive preparation accepts only an explicitly owned fresh VM or a GitHub-hosted disposable runner whose runtime has no workloads, volumes or custom networks. It tests check-only behavior, invalid signed releases, fresh pinned installation, contact configuration, service startup remaining explicit and refusal to overwrite agent state. Never run that fixture on an existing deployment host.

For pull-request previews, reserve a fresh Deployment and build host as **Preview-only** when creating it. Reservation is immutable; initial enrollment refuses existing containers. Keep it off production private networks. Ordinary workloads and preview commands cannot cross this boundary. See [Pull-request previews](pull-request-previews.md).
