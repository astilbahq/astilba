# JSON encoding

Astilba's Go code reads JSON with `encoding/json/v2` and writes everything except requests to third parties through `internal/stablejson`. The owner chose full adoption while the project is in alpha.

## The rule

| What you are doing | Use |
| --- | --- |
| Reading JSON | `encoding/json/v2`. Add `RejectUnknownMembers(true)` where unknown names must be refused. |
| Hashing or signing a value | `stablejson.Hash`, or the package `Hash` and `Digest` helpers that call it. |
| Writing a value that is stored, journaled, exported or sent between Astilba processes that can be on different releases, an API response, or command output that scripts read | `stablejson.Marshal`, `stablejson.Encode` for a stream, `stablejson.MarshalIndent` or `stablejson.EncodeIndent`. |
| Cloning a value through JSON, when the clone can reach a digest | `stablejson.Marshal`, so nil collections stay nil. |

Requests to third parties, alert webhooks and Docker registry credentials use plain `encoding/json/v2`, because nothing hashes, stores or compares them. Compared with v1, `&`, `<`, `>`, U+2028 and U+2029 are no longer escaped, map order is random and invalid UTF-8 is an error rather than replaced.

The `jsonguard` analyzer in `make check-app` checks this: `encoding/json` only in the files listed under [Remaining v1 use](#remaining-v1-use), and plain v2 writers only in the functions it names. A named function can still add a second writer, so a clean run does not rule that out.

`stablejson.Marshal` calls v2 with `encoding/json`'s `DefaultOptionsV1`, which the standard library defines as equal to v1's `Marshal`. It is the reason production code still names the v1 package.

## Why API responses stay on v1 bytes

Plain v2 would change what responses mean, not only how they look. Measured across the integration tests, `applications[].replicas` would appear as `0` inside specifications and plans, `omittedResources`, `coordinated`, `intervalMinutes`, `timeoutSeconds` and `networks[].internal` would appear with zero values, `PreviewPolicy.note` would disappear when empty, and nil arrays and maps would change from `null` to `[]` or `{}`.

The cause is that responses share Go structs with stored and hashed values, such as `deployment.Plan`, `domain.Specification` and `backup.Policy`, so one `omitempty` tag has to mean one thing. 

Moving responses to plain v2 stays possible later. It would mean retagging scalar and pointer `omitempty` fields as `omitzero`, keeping `omitempty` on slices and maps, and setting `FormatNilSliceAsNull`, `FormatNilMapAsNull`, `Deterministic` and `jsontext.AllowInvalidUTF8`. The one `jsontext.Value` field with `omitempty`, `recoveryContinuation.UpgradeStatus`, needs its own decision, because v1 writes empty and `null` raw values that v2 drops. There are up to 356 tags to review, 190 of them in `api.gen.go` and so changed through the contract, or roughly two or three days of work, and no versioned digest format would be needed. Mirrors of `deployment.Plan`, `domain.Specification` and `backup.Policy` with synthetic values wrote the same bytes this way, but that did not run against the real types or stored data, so the pinned digests would have to confirm it. The only response change left would be that `&`, `<`, `>`, U+2028 and U+2029 stop being escaped, which is the same JSON, so the owner chose not to do it now.

v2 with `OmitEmptyWithLegacySemantics` was also rejected: it still changes nil collections and escaping, still names the v1 package and adds a third format.

## Why hashed JSON stays on v1 bytes

Most digests are recomputed from decoded values by a different process: the controller hashes a command, the agent decodes it and hashes it again, and each compares the result with a value it was given. A signed agent update means those two processes can be on different releases, and the agent is the one being updated. Plans, journals and recovery bundles also keep digests for as long as the data exists.

Plain v2 output would change these digests:

| Input | v1 and `stablejson` | Plain v2 |
| --- | --- | --- |
| Nil slice or map, without `omitempty` | `null` | `[]` or `{}` |
| `false` or `0` with `omitempty` | omitted | written |
| Pointer to `""` or to an empty slice, with `omitempty` | written | omitted |
| `&&`, `<`, `>` and U+2028 in a string | escaped | written as is |
| Invalid UTF-8 | replaced with U+FFFD | error |
| `time.Duration`, including Docker health check intervals | nanoseconds | error |
| Map order | sorted | random unless `Deterministic(true)` |

Accepting the change for alpha was rejected for three reasons:

- **Hosts would stop working during a rollout.** Commands an older agent receives would fail their digest check, and so would journaled commands an updated agent re-checks. Refusing is safe in isolation, but deployments and backups stop on every host until its agent matches. The update manifest itself encodes the same under both versions, so agents could still be updated, and `TestDigestsKeepTheirReleasedValues` pins it so that stays true.
- **Recovery would fail when it is needed most.** A recovery bundle exported before the change would no longer match its recorded hashes, so restoring it would be refused during a disaster.
- **Some failures would be silent.** Hash helpers ignore `Marshal` errors, and plain v2 returns the output written before an error. A value holding a `time.Duration` would hash only the bytes before that field, so for a Docker container with a health check, drift in anything after it, including host config, mounts and running state, would go unnoticed.

### Why the v1 format is the long-term choice

A digest needs a format that does not change, and v1's is the one every existing digest already uses. Go's [compatibility promise](https://go.dev/doc/go1compat) keeps v1 in the standard library, and `encoding/json` defines its `Marshal` as semantically identical to v2 with `DefaultOptionsV1`. That promise does not name every output byte, so the pinned digests below are what fails a Go upgrade that moves one.

A versioned format, for example RFC 8785 canonical JSON, was not chosen. It would need a version next to every stored digest column, every command and report on the wire and every recovery bundle, and two encoders kept for as long as any older data exists, which includes offline recovery bundles. It would gain nothing today: the two formats describe the same values. Choose it when a requirement appears that v1 bytes cannot meet, such as a digest that other software has to reproduce.


### Writers must match the digest

Using `stablejson` only for the hash is not enough. The writer of a hashed value has to use it too. If a controller wrote a command with plain v2, a nil slice would travel as `[]`, the agent would decode an empty slice, and its digest would contain `[]` where the controller's contains `null`. The same happens to a value stored in plain v2, read back and compared. This is why every stored or peer-to-peer write uses `stablejson`, not only the hash helpers.

## Hashed and signed values

`TestDigestsKeepTheirReleasedValues` and `TestRecoveryHashesKeepTheirReleasedValues` pin digests recorded from v1.

| Value | Compared against | Decision |
| --- | --- | --- |
| `deployment.Plan.Hash`, `Payload.Digest`, `CrossHostCommand.Digest` | Agent reports, journal, stored plans, recovery descriptors | Stable |
| `pipeline.Command.Digest`, `pipeline.Hash` (build inputs, targets, cache keys, Railpack plans), `RailpackSourcePolicy` | Agent, stored rows, build cache, journal | Stable |
| `backup.Command.Digest`, `upgrade.Command.Digest`, `backup.Hash` (policies, manifests) | Agent, journal, stored manifests in the backup repository | Stable |
| `hostnetwork.Hash` (manifests, inventories, applied state) | Agent's installed network snapshot, stored network members | Stable |
| `execution.Hash` (root plans) | Stored command `plan_hash`, recovery proofs | Stable |
| Agent fingerprints and `StateHash` over Docker inspect output | Expected state frozen in plans | Stable. Holds `time.Duration`. |
| `domain.ParseSpec` canonical bytes, `content_hash`, including preview revisions | Stored immutable revisions | Stable |
| Agent update manifest, `execution.Hash(release)` | Agent, before it replaces itself | Stable |
| Recovery proof lines and `ProofDigest` | `EvidenceHash` kept through a controller handover | Stable, including each line's trailing newline |
| Upgrade target plans and route snapshots, cloned through JSON | `Plan.Hash` on recovery, `StateHash` | Stable |
| `portability` recovery row and state hashes | Recovery bases in earlier bundles | Stable, through `stablejson.Normalize`, because only the v1 decoder reads numbers as text into `any`. |
| `deployment.RecoveryDigest` | The sealed capsule bytes it was computed from | Encoding-independent |
| `agentrelease.Message` | Ed25519 signature | Not JSON |
| Stripe, GitHub, Git provider webhook signatures | Raw request bytes | Not re-encoded |
| Alert webhook signature | Bytes sent with it | Encoding-independent |

## Reading older data

v2 refuses duplicate names and invalid UTF-8, and matches names by exact case. Data already written by v1 stays readable:

- **PostgreSQL `jsonb`.** It removes duplicate names and rejects invalid UTF-8 on write, so stored rows cannot contain either. No migration uses a plain `json` column for this data.
- **Journals, generated keyrings, network snapshots, recovery files.** Go's v1 encoder writes struct tag names exactly, cannot produce duplicates from a struct and replaces invalid UTF-8. No JSON tag in this repository's history has been renamed by case alone.
- **Workspace archives and recovery bundles.** Rows come from PostgreSQL, so the same applies. `map[string]jsontext.Value` keeps `null` as the text `null` under both versions.
- **`null`, empty arrays and timestamps.** These decode to the same Go values, so digests of decoded values do not move.

Some readers stay looser, because other people or tools write their input and refusing it would take a working system down:

- **Compose `x-astilba` metadata** keeps v1 rules, through `DefaultOptionsV1`. The YAML is stored verbatim and re-read every time a revision is parsed, so refusing a saved `allowCIDRs:` would stop planning, automatic deploys, runtime views and recovery for that environment. An older controller after a rollback reads it the same way. This reader has no second parser to disagree with.
- **`railpack.json`** keeps v1 rules, through `DefaultOptionsV1`. It is a repository's own file, reread for every build of that commit, so refusing it or merging a repeated block differently would stop or change builds of unchanged source. v2's `AllowDuplicateNames` is not equivalent, because it merges a repeated object where v1 replaces it.
- **`ip` and `nft` output** accepts invalid UTF-8. Both print interface aliases and comments byte for byte, and another administrator's alias must not stop this host's network from converging.
- **Keyrings** accept line breaks inside base64 key values. Operators can build a keyring by hand or through a secret manager, and refusing a value such as one from `jq --rawfile` would stop the control plane at startup. Member names must still match exactly: a hand-built `"Active"` now stops startup until it is corrected, with no effect on stored data. Duplicate key IDs are refused.

## Trust boundaries

`httpapi.decode`, agent poll responses, `hostnetwork.Decode`, recovery records, specifications, archives and keyrings all use `RejectUnknownMembers`. A body such as `{"role":"viewer","ROLE":"owner"}` used to read as `owner` in `decode`. It was not exploitable before this work because every request object with named properties in the OpenAPI schema sets `additionalProperties: false`, so the validator refused `ROLE`, and kin-openapi refuses duplicate names when built with `goexperiment.jsonv2`. `decode` now refuses both on its own. It also refuses invalid UTF-8 and lone surrogates, which v1 replaced silently.

## Text PostgreSQL cannot store

PostgreSQL's `text` and `jsonb` cannot hold U+0000. A write with one fails with 22021, or 22P05 for the `\u0000` escape in `jsonb`, and the failure aborts its transaction. Input is kept clear of it where it enters:

- **Request bodies.** `httpapi.decode` is the one reader of a request body, which the `requestbody` analyzer enforces. It walks every string the body decodes to, including map keys and the strings inside raw JSON members. A person's or program's request is refused with `invalid_input` naming the member. That includes values never stored as text, such as secret values, build secrets and passwords: none can be an environment variable or typed into a form with NUL, and one rule for every body is what keeps a new field covered. An agent's request, one authenticated as an agent or one of the requests an agent makes for its credentials, has each NUL replaced with a space instead. Its strings carry its host's output, such as os-release and Docker's version, and refusing them would refuse every heartbeat or report after it. The only refusal is a map in which a replacement would make two keys equal.
- **Paths and query strings.** Every request whose path or query decodes to NUL is refused before a handler runs. Today every path and query parameter is a UUID, number or enumeration, which their binding refuses anyway; this covers a text parameter added later.
- **Archives.** An export cannot write NUL, so an archive row holding one is refused as an invalid archive.
- **Signed webhooks.** The Stripe, GitHub App and Git push handlers read their raw bytes to check the provider's signature, then decode a few identifiers from them. Only the provider can write those, and the store's refusal below covers them.

`store.Normalize` maps 22021 and 22P05 to `domain.UnstorableText`, an `invalid_input` refusal, so a path these miss, such as a provider's reply, is refused rather than answered with a 500, and the API logs a warning naming the route. The recovery CLI reads hosts' exports from disk rather than through a request, so it is not covered yet (PLAT-321).

## Where third-party code keeps v1

These libraries call `encoding/json` v1 themselves, so they accept v1's looser input at that boundary:

- `kin-openapi` request validation, which runs before `decode` and reads the body into `any`. It sees names exactly, so a case variant is an unknown property.
- `oapi-codegen/runtime` parameter binding, and the embedded specification in `api.gen.go`.
- `pgx` JSON codecs, on values this repository wrote.
- River job arguments, written and read only by the controller.
- `moby` client responses and `compose-go` types.

## Remaining v1 use

Once the series finishes, production code names `encoding/json` in only four places. Each reads or writes a format that Astilba does not control or must not change, so moving it would change behaviour rather than tidy an import:

- **`internal/stablejson`**, for `DefaultOptionsV1`, the decoder behind `Normalize` and `MarshalIndent`, whose output `jsontext.WithIndent` does not reproduce. This is the frozen format itself.
- **`internal/domain/compose.go`**, for `DefaultOptionsV1` on `x-astilba` metadata. The YAML is the user's own text in an immutable, content-hashed revision, so it cannot be rewritten to suit a stricter reader.
- **`internal/pipeline/railpack_plan.go`**, for `DefaultOptionsV1` on a repository's `railpack.json`, for the same reason as Compose metadata: it is the user's own text, and it is read again for every build.
- **`internal/cryptobox`**, for `ParseBytesWithLooseRFC4648` on keyrings. Operators write these files, and this option is the standard library's named switch for the line breaks they contain.

Tests follow the same rules, because a test decoding with v1 folds case and can pass where production would not. `stablejson` writes exactly what an older peer writes, so compatibility tests use it. Only `internal/stablejson/golden_test.go` still names v1, to decode as an older peer does.
