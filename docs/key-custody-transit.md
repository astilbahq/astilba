# Key custody with OpenBao or Vault Transit

By default, management keeps every workspace key wrapped under the keyring file
([key custody](hosting.md#key-custody)). With the Transit backend, the workspace, installation and
token keys are wrapped instead by [OpenBao](https://openbao.org) or HashiCorp Vault Transit keys
that never leave the key service. Each unwrap is then a request to the key service, which
records it in its own audit log, outside management. The log shows the installation, workspace,
key generation and purpose of each request. Management stores the key service's request ID with
each [secret access record](hosting.md#operational-bounds-and-retention), so the two can be
matched.

OpenBao is the reference implementation: the tests and the local stack run OpenBao 2.7. Vault
uses the same API.

## What it protects against, and what it doesn't

- A copy of the database, or of a backup, opens nothing without the key service and a
  management identity it accepts.
- Every use of a key by management, including by someone who has taken management over, is
  recorded by the key service. An auditor compares that log with management's records.
- The serving identity can't unwrap a key generation's `rotate` wrapping. Only the operator can,
  so moving keys to a new generation or backend needs the operator's credentials.
- No identity can export a key, back one up, restore one, delete one, or trim old versions.

It doesn't stop someone controlling management from opening what management can open while
they control it. It records that they did.

The keyring is still required (`ASTILBA_KEYRING`): values sealed before per-workspace keys
existed open under it until they are converted (`astilba custody convert`). It wraps nothing new.

## The policy files

Management uses two identities, each with a policy shipped in
[`services/platform/internal/custody/policies`](../services/platform/internal/custody/policies):

| File | Identity | Grants |
| --- | --- | --- |
| [`astilba-serving.hcl`](../services/platform/internal/custody/policies/astilba-serving.hcl) | `astilba serve` and every command except the operator's | Encrypt and decrypt with `astilba-serve`, `astilba-export` and `astilba-installation`. Encrypt only with `astilba-rotate`. Read each key's settings. |
| [`astilba-operator.hcl`](../services/platform/internal/custody/policies/astilba-operator.hcl) | `astilba custody rewrap`, `custody convert` and later rotations | Decrypt only with `astilba-rotate`. Encrypt and rewrap with every key. Rotate keys. Change only `min_decryption_version` and `auto_rotate_period`. |

Both deny export, backup, restore and trim explicitly, so a broader policy attached to the same
identity by mistake can't grant them. Neither grants create, delete or list.

The files name the mount `astilba-transit`. To mount Transit elsewhere, change the first segment
of every path in both files, and set `ASTILBA_TRANSIT_MOUNT`.

## Set up the key service

Run these as an OpenBao or Vault administrator, not on the management host. Management never
needs a token that can change the key service's own configuration. The commands use `bao`; with
Vault, use `vault`.

1. **Turn on an audit device.** OpenBao 2.4 and later create audit devices from the server's
   configuration only, for example:

   ```hcl
   audit "file" "astilba" {
     options = {
       file_path = "/var/log/openbao/audit.log"
     }
   }
   ```

   On Vault, run `vault audit enable file file_path=/var/log/vault/audit.log`. Ship the log off
   the key service host.

2. **Mount Transit, and show each request's context in the audit log.** The context holds no
   secret. Keys and wrappings stay hashed in the log.

   ```bash
   bao secrets enable -path=astilba-transit -description="Astilba key custody" transit
   bao secrets tune -audit-non-hmac-request-keys=context astilba-transit
   ```

3. **Create one key per class.** Each key is derived per context, can't be exported or backed up
   in plaintext, and can't be deleted (`deletion_allowed` stays false, and no policy can change
   it).

   ```bash
   for class in serve export rotate installation; do
     bao write -f astilba-transit/keys/astilba-$class type=aes256-gcm96 derived=true exportable=false allow_plaintext_backup=false
   done
   ```

4. **Write the policies.**

   ```bash
   bao policy write astilba-serving services/platform/internal/custody/policies/astilba-serving.hcl
   bao policy write astilba-operator services/platform/internal/custody/policies/astilba-operator.hcl
   ```

5. **Give each identity a way to sign in**, with AppRole or JWT.

   **AppRole**, bound to the management hosts' addresses. Replace the CIDRs with your management
   hosts' for the serving role, and with the host operators run commands from for the operator
   role.

   ```bash
   bao auth enable approle
   bao write auth/approle/role/astilba-serving token_policies=astilba-serving \
     token_ttl=1h token_max_ttl=24h \
     secret_id_bound_cidrs=10.0.1.10/32,10.0.1.11/32 token_bound_cidrs=10.0.1.10/32,10.0.1.11/32
   bao write auth/approle/role/astilba-operator token_policies=astilba-operator \
     token_ttl=30m token_max_ttl=8h \
     secret_id_bound_cidrs=10.0.2.5/32 token_bound_cidrs=10.0.2.5/32
   bao read -field=role_id auth/approle/role/astilba-serving/role-id > role-id
   bao write -f -field=secret_id auth/approle/role/astilba-serving/secret-id > secret-id
   ```

   Install `role-id` and `secret-id` on each management host as private files (mode `0600`,
   owned by the user management runs as). Management reads them again at each sign-in, so you can
   replace the secret ID without a restart. Keep the operator's secret ID off the management
   hosts.

   **JWT**, where the platform issues workload tokens, such as Kubernetes service account tokens:

   ```bash
   bao auth enable jwt
   bao write auth/jwt/config oidc_discovery_url=https://issuer.example.com
   bao write auth/jwt/role/astilba-serving role_type=jwt token_policies=astilba-serving \
     bound_audiences=astilba-transit bound_subject=system:serviceaccount:astilba:management \
     user_claim=sub token_ttl=1h token_max_ttl=24h
   ```

   Mount the token as a private file. A Kubernetes projected token needs `defaultMode: 0400` or
   `0600`, because management refuses a credential file that others can read.

## Configure management

| Variable | Value |
| --- | --- |
| `ASTILBA_KEY_BACKEND` | `transit` |
| `ASTILBA_TRANSIT_ADDR` | The key service's origin, such as `https://bao.internal:8200`. HTTPS is required. |
| `ASTILBA_TRANSIT_CA_FILE` | Optional. A PEM file of the only certificate authorities trusted for that address. |
| `ASTILBA_TRANSIT_MOUNT` | Optional. Transit's mount, `astilba-transit` by default. |
| `ASTILBA_TRANSIT_NAMESPACE` | Optional. The namespace, for Vault Enterprise and HCP Vault. |
| `ASTILBA_TRANSIT_AUTH` | `approle` (the default) or `jwt`. |
| `ASTILBA_TRANSIT_ROLE_ID_FILE`, `ASTILBA_TRANSIT_SECRET_ID_FILE` | For AppRole: the private files holding the role ID and secret ID. |
| `ASTILBA_TRANSIT_JWT_FILE`, `ASTILBA_TRANSIT_JWT_ROLE` | For JWT: the private file holding the token, and the role. |
| `ASTILBA_TRANSIT_AUTH_MOUNT` | Optional. Where the sign-in method is mounted, if not at `approle` or `jwt`. |

Credentials are only ever read from files. Management refuses to start if `VAULT_TOKEN`,
`BAO_TOKEN` or an `ASTILBA_TRANSIT_*` credential is in its environment. It sends the token only
to the configured address: it ignores proxy settings and doesn't follow redirects.

Operator commands use the same variables, with the operator's role ID and secret ID files.

### What management checks

At every start, before it uses a key, management:

- signs in, and reads each class's key settings. It refuses a key that is exportable, can be
  backed up in plaintext, can be deleted, or isn't derived, and records the refusal in the
  installation's key history;
- opens the canary of each class it may unwrap under: serve, export and installation for
  serving, and rotate for the operator.

Each call has a 2-second deadline. A call that times out, gets a 5xx answer or is rate limited is
tried once more. A refused token is replaced by a new sign-in once. Management renews its token
two thirds of the way through its lease.

## Which installations can use it

A new installation registers the backend it first starts with. An installation that already
started with the keyring file refuses to start with Transit, until moving an installation from
one backend to another is supported.

## Rotating the Transit keys

1. Rotate the key: `bao write -f astilba-transit/keys/astilba-serve/rotate`. New wrappings use
   the new version at once, and old ones still open.
2. Run `astilba custody rewrap --operator <name>` with the operator's credentials. It moves
   every wrapping onto the newest version inside the key service, without unwrapping it.
3. Once `custody status` shows nothing pending, raise `min_decryption_version` so the old
   version no longer decrypts:
   `bao write astilba-transit/keys/astilba-serve/config min_decryption_version=2`.

You can also set `auto_rotate_period` on each key.

## Backups

The Transit keys can't be exported, so the key service's own backup is the only backup of
them. Back up OpenBao or Vault (for example Raft snapshots) together with its unseal
arrangement. Without the key service, a management database backup opens nothing. Keep the
keyring file backed up too, as long as values sealed under it remain ([management
backups](management-backups.md#the-keyring)).

## Local development

`pnpm dev --vm PROFILE --custody openbao` runs OpenBao in the local stack's VM and sets it up the
same way, with `astilba custody init-transit --dev`. See
[local development](local-development.md#key-custody-with-openbao). `init-transit` works only
with `--dev` and a loopback address: it uses the dev server's root token, which a real key
service never gives management.
