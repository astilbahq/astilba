# Astilba management, serving (`astilba serve` and every command except the operator ones).
#
# Written for the Transit mount "astilba-transit". If you mount Transit elsewhere, change every path's
# first segment to match, and set ASTILBA_TRANSIT_MOUNT. See docs/key-custody-transit.md.
#
# Nothing here grants create, delete or list, so no key can be made, replaced or removed through
# this identity, and Transit's encrypt never makes a missing key without create.

# Wrap and unwrap workspace, installation and export keys.
path "astilba-transit/encrypt/astilba-serve" {
  capabilities = ["update"]
}
path "astilba-transit/decrypt/astilba-serve" {
  capabilities = ["update"]
}
path "astilba-transit/encrypt/astilba-export" {
  capabilities = ["update"]
}
path "astilba-transit/decrypt/astilba-export" {
  capabilities = ["update"]
}
path "astilba-transit/encrypt/astilba-installation" {
  capabilities = ["update"]
}
path "astilba-transit/decrypt/astilba-installation" {
  capabilities = ["update"]
}

# A new key generation is also wrapped for the operator, under rotate; only the operator unwraps it.
path "astilba-transit/encrypt/astilba-rotate" {
  capabilities = ["update"]
}

# Management reads each key's settings at start-up and refuses a key that is exportable, deletable
# or not derived. The settings hold no key material.
path "astilba-transit/keys/astilba-serve" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-export" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-rotate" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-installation" {
  capabilities = ["read"]
}

# Explicit denials win over anything another policy on the same identity grants. Keys never leave
# the key service, and their old versions are never trimmed away.
path "astilba-transit/export/*" {
  capabilities = ["deny"]
}
path "astilba-transit/backup/*" {
  capabilities = ["deny"]
}
path "astilba-transit/restore" {
  capabilities = ["deny"]
}
path "astilba-transit/restore/*" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-serve/trim" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-export/trim" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-rotate/trim" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-installation/trim" {
  capabilities = ["deny"]
}
path "astilba-transit/decrypt/astilba-rotate" {
  capabilities = ["deny"]
}
path "astilba-transit/rewrap/astilba-rotate" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-serve/config" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-serve/rotate" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-export/config" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-export/rotate" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-rotate/config" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-rotate/rotate" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-installation/config" {
  capabilities = ["deny"]
}
path "astilba-transit/keys/astilba-installation/rotate" {
  capabilities = ["deny"]
}
