#!/usr/bin/env bash
# Backs up an Astilba management database: a custom-format pg_dump, encrypted with age
# and uploaded with a manifest to S3-compatible storage. The keyring is not included;
# back it up separately. See docs/management-backups.md.
set -Eeuo pipefail
umask 077

config=${ASTILBA_BACKUP_CONFIG:-/etc/astilba/management-backup.env}
step='read the configuration'

checkin() {
  # The URL is kept out of process arguments, which other local users can read.
  [[ -n "$1" ]] || return 0
  printf 'url = "%s"\n' "$1" | curl --config - --fail --silent --show-error \
    --connect-timeout 10 --max-time 30 --retry 5 --retry-connrefused --retry-delay 5 \
    --data-raw "$2" --output /dev/null ||
    printf 'The check-in to the monitor failed.\n' >&2
}

work=
finish() {
  local status=$?
  [[ -n "$work" ]] && rm -rf -- "$work"
  if ((status != 0)); then
    printf 'Backup failed: could not %s.\n' "$step" >&2
    checkin "${ASTILBA_BACKUP_FAILURE_URL:-}" "Astilba management backup failed: could not $step."
  fi
  exit "$status"
}
trap finish EXIT

[[ -r "$config" ]] || { printf 'Cannot read %s.\n' "$config" >&2; exit 1; }
# shellcheck source=/dev/null
source "$config"
: "${ASTILBA_BACKUP_RECIPIENTS:?Set the age recipients file}"
: "${ASTILBA_BACKUP_S3_URL:?Set the S3 URL of the bucket and prefix}"
: "${ASTILBA_BACKUP_S3_REGION:?Set the S3 signing region}"
credentials=${ASTILBA_BACKUP_S3_CREDENTIALS:-${CREDENTIALS_DIRECTORY:+$CREDENTIALS_DIRECTORY/s3}}
[[ -n "$credentials" && -r "$credentials" ]] || { printf 'Cannot read the S3 credentials file.\n' >&2; exit 1; }
[[ -r "$ASTILBA_BACKUP_RECIPIENTS" ]] || { printf 'Cannot read %s.\n' "$ASTILBA_BACKUP_RECIPIENTS" >&2; exit 1; }
[[ "$ASTILBA_BACKUP_S3_URL" =~ ^https?://[^[:space:]\"]+[^/]$ ]] ||
  { printf 'ASTILBA_BACKUP_S3_URL must be an http(s) URL without a trailing slash.\n' >&2; exit 1; }
[[ "$ASTILBA_BACKUP_S3_REGION" =~ ^[a-z0-9-]+$ ]] || { printf 'Invalid ASTILBA_BACKUP_S3_REGION.\n' >&2; exit 1; }
for url in "${ASTILBA_BACKUP_CHECKIN_URL:-}" "${ASTILBA_BACKUP_FAILURE_URL:-}"; do
  [[ -z "$url" || "$url" =~ ^https?://[^[:space:]\"]+$ ]] || { printf 'Invalid check-in URL.\n' >&2; exit 1; }
done
for tool in age curl openssl sha256sum base64; do
  command -v "$tool" >/dev/null || { printf 'Install %s.\n' "$tool" >&2; exit 1; }
done

database_user=${ASTILBA_BACKUP_DATABASE_USER:-astilba}
database_name=${ASTILBA_BACKUP_DATABASE_NAME:-astilba}
# Runs a PostgreSQL client in the database container, or on this host with libpq's
# environment (PGHOST, PGPASSFILE, PGSSLMODE and so on) when no container is set.
# pg_dump must be the server's major version or newer.
postgres() {
  if [[ -n "${ASTILBA_BACKUP_DATABASE_CONTAINER:-}" ]]; then
    docker exec "$ASTILBA_BACKUP_DATABASE_CONTAINER" "$@"
  else
    "$@"
  fi
}
query() {
  postgres psql --no-psqlrc --no-password --tuples-only --no-align --set ON_ERROR_STOP=1 \
    --username "$database_user" --dbname "$database_name" --command "$1"
}

step='connect to the database'
schema=$(query 'SELECT max(version_id) FROM goose_db_version WHERE is_applied')
server_version=$(query 'SHOW server_version')
step='read the schema version'
[[ "$schema" =~ ^[0-9]+$ ]] || exit 1
step='read the PostgreSQL version'
server_version=${server_version%% *}
[[ "$server_version" =~ ^[0-9][0-9.]*$ ]] || exit 1
revision=unknown
if [[ -n "${ASTILBA_BACKUP_APP_CONTAINER:-}" ]]; then
  step='read the running image revision'
  revision=$(docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$ASTILBA_BACKUP_APP_CONTAINER")
  [[ "$revision" =~ ^[0-9A-Za-z._-]{1,64}$ ]] || revision=unknown
fi

step='prepare the work directory'
work=$(mktemp -d "${STATE_DIRECTORY:-/var/tmp}/astilba-management-backup.XXXXXXXX")
now=$(date -u +%s)
stamp=$(date -u -d "@$now" +%Y%m%dT%H%M%SZ)
dump="$stamp.pgdump.age"
manifest="$stamp.json"

# The dump is encrypted as it is written, so no plaintext copy reaches the disk.
step='dump and encrypt the database'
postgres pg_dump --no-password --format=custom --username "$database_user" --dbname "$database_name" |
  age --encrypt --recipients-file "$ASTILBA_BACKUP_RECIPIENTS" --output "$work/$dump"

step='describe the backup'
digest=$(sha256sum "$work/$dump" | cut -d' ' -f1)
size=$(wc -c < "$work/$dump" | tr -d ' ')
printf '{"format":1,"created":"%s","dump":"%s","sha256":"%s","size":%s,"schemaVersion":%s,"postgresVersion":"%s","sourceRevision":"%s"}\n' \
  "$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ)" \
  "$dump" "$digest" "$size" "$schema" "$server_version" "$revision" > "$work/$manifest"

upload() {
  local file=$1 type=$2
  # Content-MD5 lets the store reject a corrupted body and is required by buckets with
  # object lock; the signed SHA-256 binds the body to the request.
  curl --config "$credentials" --fail-with-body --silent --show-error \
    --connect-timeout 10 --max-time 3600 --retry 5 --retry-connrefused --retry-delay 10 \
    --aws-sigv4 "aws:amz:$ASTILBA_BACKUP_S3_REGION:s3" \
    --header "Content-Type: $type" \
    --header "Content-MD5: $(openssl dgst -md5 -binary "$work/$file" | base64)" \
    --header "x-amz-content-sha256: $(sha256sum "$work/$file" | cut -d' ' -f1)" \
    --upload-file "$work/$file" --output /dev/null "$ASTILBA_BACKUP_S3_URL/$file"
}
# The manifest goes last, so its presence marks a complete backup.
step="upload $dump"
upload "$dump" application/octet-stream
step="upload $manifest"
upload "$manifest" application/json

printf 'Uploaded %s (%s bytes, schema version %s).\n' "$dump" "$size" "$schema"
step='report success to the monitor'
checkin "${ASTILBA_BACKUP_CHECKIN_URL:-}" "Uploaded $dump"
