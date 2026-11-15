# Astilba operator commands (`astilba custody rewrap`, `custody convert` and later rotations).
#
# Written for the Transit mount "astilba-transit". If you mount Transit elsewhere, change every path's
# first segment to match, and set ASTILBA_TRANSIT_MOUNT. See docs/key-custody-transit.md.
#
# Nothing here grants create, delete or list, so no key can be made, replaced or removed through
# this identity, and Transit's encrypt never makes a missing key without create.

# Unwrap only what the operator's own purpose is wrapped for.
path "astilba-transit/decrypt/astilba-rotate" {
  capabilities = ["update"]
}

# Wrap new key generations for every purpose.
path "astilba-transit/encrypt/astilba-serve" {
  capabilities = ["update"]
}
path "astilba-transit/encrypt/astilba-export" {
  capabilities = ["update"]
}
path "astilba-transit/encrypt/astilba-rotate" {
  capabilities = ["update"]
}
path "astilba-transit/encrypt/astilba-installation" {
  capabilities = ["update"]
}

# Move wrappings onto a key's newest version inside the key service, which never returns the key.
path "astilba-transit/rewrap/astilba-serve" {
  capabilities = ["update"]
}
path "astilba-transit/rewrap/astilba-export" {
  capabilities = ["update"]
}
path "astilba-transit/rewrap/astilba-rotate" {
  capabilities = ["update"]
}
path "astilba-transit/rewrap/astilba-installation" {
  capabilities = ["update"]
}

# Rotate keys, read their settings, and change only how old a version may still decrypt and how often
# the key rotates itself.
path "astilba-transit/keys/astilba-serve" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-serve/rotate" {
  capabilities = ["update"]
}
path "astilba-transit/keys/astilba-serve/config" {
  capabilities = ["update"]
  allowed_parameters = {
    "min_decryption_version" = []
    "auto_rotate_period"     = []
  }
}
path "astilba-transit/keys/astilba-export" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-export/rotate" {
  capabilities = ["update"]
}
path "astilba-transit/keys/astilba-export/config" {
  capabilities = ["update"]
  allowed_parameters = {
    "min_decryption_version" = []
    "auto_rotate_period"     = []
  }
}
path "astilba-transit/keys/astilba-rotate" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-rotate/rotate" {
  capabilities = ["update"]
}
path "astilba-transit/keys/astilba-rotate/config" {
  capabilities = ["update"]
  allowed_parameters = {
    "min_decryption_version" = []
    "auto_rotate_period"     = []
  }
}
path "astilba-transit/keys/astilba-installation" {
  capabilities = ["read"]
}
path "astilba-transit/keys/astilba-installation/rotate" {
  capabilities = ["update"]
}
path "astilba-transit/keys/astilba-installation/config" {
  capabilities = ["update"]
  allowed_parameters = {
    "min_decryption_version" = []
    "auto_rotate_period"     = []
  }
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
path "astilba-transit/decrypt/astilba-serve" {
  capabilities = ["deny"]
}
path "astilba-transit/decrypt/astilba-export" {
  capabilities = ["deny"]
}
path "astilba-transit/decrypt/astilba-installation" {
  capabilities = ["deny"]
}
