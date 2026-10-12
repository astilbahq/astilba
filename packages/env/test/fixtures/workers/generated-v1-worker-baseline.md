# Generated v1 Worker baseline

`generated-v1-worker-baseline.json` is the exact byte baseline for `.astilba/env/workerDeployment.server.ts` generated from `worker-admission-v1.astilba.env.txt` by the public `@astilba/env@0.1.0` archive. The package identity is the npm registry's immutable tarball URL and SRI integrity value recorded in that JSON file.

The Workers admission script reads that exact fixture from the checkout, so the baseline input and the admitted package input cannot drift independently. Test fixtures are not included in the published package because `package.json` permits only `dist`, `LICENSE`, and `README.md`.

To reproduce the baseline, start at this repository root and create a clean local installation of the pinned package. Do not use `npm exec --package`, which does not make `@astilba/env` resolvable from the fixture.

POSIX:

```sh
repository_root=$PWD
reproduction=$(mktemp -d)
cp "$repository_root/test/fixtures/workers/worker-admission-v1.astilba.env.txt" "$reproduction/astilba.env.ts"
cd "$reproduction"
npm init --yes
npm pkg set private=true type=module
npm install --ignore-scripts --package-lock=false @astilba/env@0.1.0
BUILD_LABEL=build-boundary node node_modules/@astilba/env/dist/cli/astilba-env.js generate
node --input-type=module -e 'import { createHash } from "node:crypto"; import { readFile } from "node:fs/promises"; const bytes = await readFile(process.argv[1]); console.log(JSON.stringify({ sha256: createHash("sha256").update(bytes).digest("hex"), size: bytes.byteLength }));' .astilba/env/workerDeployment.server.ts
```

PowerShell:

```powershell
$repositoryRoot = (Get-Location).Path
$reproduction = Join-Path ([System.IO.Path]::GetTempPath()) ("astilba-env-v1-baseline-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $reproduction | Out-Null
Copy-Item (Join-Path $repositoryRoot "test/fixtures/workers/worker-admission-v1.astilba.env.txt") (Join-Path $reproduction "astilba.env.ts")
Set-Location $reproduction
npm init --yes
npm pkg set private=true type=module
npm install --ignore-scripts --package-lock=false @astilba/env@0.1.0
$env:BUILD_LABEL = "build-boundary"
node .\node_modules\@astilba\env\dist\cli\astilba-env.js generate
node --input-type=module -e 'import { createHash } from "node:crypto"; import { readFile } from "node:fs/promises"; const bytes = await readFile(process.argv[1]); console.log(JSON.stringify({ sha256: createHash("sha256").update(bytes).digest("hex"), size: bytes.byteLength }));' .astilba/env/workerDeployment.server.ts
```

It must print SHA-256 `8e55ef7cf12958bb2764a0dc9e7c2565695d483040a6dda1e4d11fbc133182ec` and size `19462`. No normalization is applied: the Workers archive-admission lane compares the packed archive's generated file size and raw SHA-256 digest to this fixture. The lane does not contact npm; the integrity is provenance for review and reproduction only.
