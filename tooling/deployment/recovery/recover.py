"""Fail-closed artifact restoration; no package installer or provider API fallback."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[3]
POLICY = Path(__file__).with_name("policy.json")
SCOPES = {"site": ["site"], "docs-site": ["docs", "site"], "all": ["showcase", "docs", "site"]}
NAMES = {"site": "astilba-site", "docs": "astilba-docs", "showcase": "astilba-ui-showcase"}
COMMITS = {"site": "2580aa7b7bc0e6b5c574e41df6391c5c2edd6038", "docs": "2580aa7b7bc0e6b5c574e41df6391c5c2edd6038", "showcase": "e295ce4aa79278ae0d865a88435f6539542808fe"}
SMOKES = {"site": "apps/site/scripts/smoke-production.mjs", "docs": "apps/docs/scripts/smoke-production.mjs", "showcase": "scripts/smoke-production.mjs"}
SMOKE_FILES = {*SMOKES.values(), "apps/docs/src/docs/security.ts", "apps/docs/src/docs/urls.ts", "apps/docs/package.json"}
HEX = re.compile(r"[0-9a-f]{64}\Z")
SHA = re.compile(r"[0-9a-f]{40}\Z")


class RecoveryError(Exception):
    """Messages are safe for public job logs; never include provider output."""


def require(condition, message):
    if not condition:
        raise RecoveryError(message)


def digest(path):
    with Path(path).open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def activated(policy):
    require(policy["phase"] == "approved-for-activation", "Recovery preparation is inactive")
    require(policy["repository"] == "astilbahq/astilba" and policy["environment"] == "production-web", "Unexpected recovery authority")
    require(policy["enableVariable"] == "WEB_RECOVERY_ENABLED", "Unexpected recovery switch")
    require(type(policy["allowSelfApproval"]) is bool, "Self-approval needs an explicit decision")
    expected = {"protectedEnvironmentVerified", "durableBackupAndRuntimeVerified", "maintainerActivationApproval"}
    require(set(policy["acceptance"]) == expected and all(value is True for value in policy["acceptance"].values()), "Recovery acceptance is incomplete")
    runtime = policy["runtime"]
    require(isinstance(runtime["sha256"], str) and HEX.fullmatch(runtime["sha256"]), "Offline runtime is not pinned")
    require(type(runtime["bytes"]) is int and 0 < runtime["bytes"] <= 1024**3, "Offline runtime size is not pinned")
    require(isinstance(runtime["imageId"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", runtime["imageId"]), "Offline image identity is not pinned")
    require((runtime["node"], runtime["wrangler"], runtime["platform"]) == ("v24.18.1", "4.114.0", "linux/amd64"), "Unexpected offline toolchain")


def context(policy, environment):
    activated(policy)
    require(environment.get("GITHUB_ACTIONS") == "true" and environment.get("GITHUB_REPOSITORY") == policy["repository"], "Only the approved GitHub repository may recover")
    require(environment.get("GITHUB_EVENT_NAME") == "workflow_dispatch" and environment.get("GITHUB_REF") == "refs/heads/main", "Recovery requires manual main dispatch")
    require(environment.get("RECOVERY_ENABLED") == "true", "Recovery switch is disabled")
    expected = environment.get("EXPECTED_MAIN_SHA", "")
    require(SHA.fullmatch(expected) and expected == environment.get("GITHUB_SHA"), "Dispatch SHA is not the approved current head")
    scope = environment.get("RECOVERY_SCOPE")
    incident = environment.get("RECOVERY_INCIDENT", "")
    require(scope in SCOPES, "Unknown affected scope")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9 ._:/#-]{0,159}", incident), "Use a non-confidential incident identifier without markup")
    return {"main": expected, "scope": scope, "incident": incident, "capsule": policy["capsule"]["sha256"]}


def current_main(expected, execute=subprocess.run):
    result = execute(["git", "ls-remote", "--exit-code", "origin", "refs/heads/main"], cwd=ROOT, env=clean_environment(os.environ), capture_output=True, text=True, timeout=30, check=False)
    require(result.returncode == 0 and result.stdout.split() == [expected, "refs/heads/main"], "Main advanced or could not be verified; stop recovery")


def github(path, token):
    require(token, "Read-only GitHub token is absent")
    request = urllib.request.Request(f"https://api.github.com/repos/astilbahq/astilba/{path}", headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"})
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)


def protected_environment(policy, token, read=github):
    actual = read("environments/production-web", token)
    require(actual.get("can_admins_bypass") is False, "Environment administrator bypass is not disabled")
    require(actual.get("deployment_branch_policy") == {"protected_branches": False, "custom_branch_policies": True}, "Environment must use explicit branch policy")
    rules = [rule for rule in actual.get("protection_rules", []) if rule["type"] == "required_reviewers"]
    require(len(rules) == 1 and rules[0].get("prevent_self_review") is (not policy["allowSelfApproval"]), "Manual approval or self-review policy differs")
    require([(reviewer["type"], reviewer["reviewer"]["id"]) for reviewer in rules[0].get("reviewers", [])] == [("User", policy["reviewerId"])], "Required maintainer reviewer differs")
    branches = read("environments/production-web/deployment-branch-policies?per_page=100", token)
    require(branches.get("total_count") == 1 and [(branch["name"], branch["type"]) for branch in branches["branch_policies"]] == [("main", "branch")], "Only the main branch may recover; tags are excluded")
    oidc = read("actions/oidc/customization/sub", token)
    require(oidc.get("use_default") is True and oidc.get("use_immutable_subject") is True and oidc.get("sub_claim_prefix") == policy["oidcSubjectPrefix"], "OIDC identity changed; reconcile AWS trust first")


def object_settings(environment):
    fields = {key: environment.get(f"RECOVERY_{key}", "") for key in ["S3_BUCKET", "AWS_REGION", "READER_ROLE_ARN", "CAPSULE_KEY", "CAPSULE_VERSION", "RUNTIME_KEY", "RUNTIME_VERSION"]}
    require(all(fields.values()), "Approved backup object versions and read role are not provisioned")
    require(re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]", fields["S3_BUCKET"]), "Invalid backup bucket")
    require(re.fullmatch(r"[a-z]{2}-[a-z]+-\d", fields["AWS_REGION"]), "Invalid backup region")
    require(re.fullmatch(r"arn:aws:iam::\d{12}:role/[A-Za-z0-9+=,.@_/-]+", fields["READER_ROLE_ARN"]), "Invalid backup read role")
    for name in ["CAPSULE_KEY", "CAPSULE_VERSION", "RUNTIME_KEY", "RUNTIME_VERSION"]:
        require(re.fullmatch(r"[A-Za-z0-9._/+=-]{1,512}", fields[name]) and fields[name] != "null", "Invalid immutable backup identity")
    require((fields["CAPSULE_KEY"], fields["CAPSULE_VERSION"]) != (fields["RUNTIME_KEY"], fields["RUNTIME_VERSION"]), "Runtime and capsule must be distinct objects")
    return {key.lower(): value for key, value in fields.items()}


def authenticate(path, specification):
    require(Path(path).is_file() and not Path(path).is_symlink(), "Recovery input is not a regular file")
    require(Path(path).stat().st_size == specification["bytes"] and digest(path) == specification["sha256"], "Recovery input identity mismatch before extraction or execution")


def safe_extract(archive, destination, selected):
    destination = Path(destination)
    require(not destination.exists(), "Extraction destination must be fresh")
    with tarfile.open(archive, "r:*") as source:
        members = source.getmembers()
        require(len(members) <= 10000 and sum(member.size for member in members) <= 256 * 1024**2, "Recovery archive exceeds limits")
        seen = set()
        for member in members:
            name = member.name.rstrip("/")
            require(name and not name.startswith("/") and "\\" not in name and not any(ord(character) < 32 for character in name) and all(part not in ("", ".", "..") for part in name.split("/")), "Unsafe archive path")
            require(name not in seen, "Duplicate archive entry")
            seen.add(name)
            require(member.isfile() or member.isdir(), "Archive links and special files are forbidden")
        destination.mkdir(parents=True)
        source.extractall(destination, members=[member for member in members if selected(member.name.rstrip("/"))], filter="data")


def files(directory):
    result = {}
    for path in Path(directory).rglob("*"):
        require(not path.is_symlink(), "Restoration symlink is forbidden")
        if path.is_file():
            result[path.relative_to(directory).as_posix()] = {"bytes": path.stat().st_size, "sha256": digest(path)}
        else:
            require(path.is_dir(), "Restoration special file is forbidden")
    return result


def validate_app(directory, app, manifest):
    entry = manifest["artifacts"]["baseline"][app]
    require(entry["workerName"] == NAMES[app] and entry["originalCommit"] == COMMITS[app], "Baseline target/source differs")
    require(files(directory) == entry["files"], "Restoration file set or content changed")
    config = json.loads((Path(directory) / "restore-wrangler.json").read_text())
    require(config["name"] == NAMES[app] and config["routes"] == entry["routes"] and config["assets"]["directory"] == "./assets", "Restoration target or asset location differs")
    require(config.get("main") == (None if app == "showcase" else "./worker/index.js"), "Unexpected restoration entry point")


def extract_capsule(policy, capsule, destination):
    authenticate(capsule, policy["capsule"])
    with tarfile.open(capsule, "r:*") as source:
        member = source.getmember("manifest.json")
        require(member.isfile() and member.size <= 4 * 1024**2, "Invalid capsule manifest member")
        manifest_bytes = source.extractfile(member).read()
    require(hashlib.sha256(manifest_bytes).hexdigest() == policy["capsule"]["manifestSha256"], "Unapproved capsule manifest")
    manifest = json.loads(manifest_bytes)
    # Select exactly the manifest's application bytes, not macOS tar metadata.
    selected = {"manifest.json", "candidate-public-source.tar"}
    for app in NAMES:
        selected.update(f"capsule/baseline/{app}/{name}" for name in manifest["artifacts"]["baseline"][app]["files"])
    allowed = lambda name: name in selected
    safe_extract(capsule, destination, allowed)
    destination = Path(destination)
    require(digest(destination / "manifest.json") == policy["capsule"]["manifestSha256"], "Unapproved capsule manifest")
    for app in NAMES:
        validate_app(destination / "capsule/baseline" / app, app, manifest)
    frozen = destination / "candidate-public-source.tar"
    require(digest(frozen) == manifest["sources"]["candidate"]["sha256"], "Frozen smoke source changed")
    smoke_root = destination / "frozen-smoke"
    safe_extract(frozen, smoke_root, lambda name: name in SMOKE_FILES)
    require(set(files(smoke_root)) == SMOKE_FILES, "Incomplete frozen smoke import closure")
    return manifest


def clean_environment(environment):
    return {key: environment[key] for key in ["PATH", "TMPDIR", "LANG"] if key in environment} | {"CI": "true", "WRANGLER_SEND_METRICS": "false", "WRANGLER_LOG_SANITIZE": "true"}


def command(arguments, environment, execute=subprocess.run):
    result = execute(arguments, env=environment, capture_output=True, text=True, timeout=300, check=False)
    require(result.returncode == 0, "Recovery command failed; inspect provider state before retrying")
    return result.stdout.strip()


def runtime(policy, archive, environment, execute=subprocess.run):
    authenticate(archive, policy["runtime"])
    clean = clean_environment(environment)
    command(["docker", "load", "--input", str(archive)], clean, execute)
    image = policy["runtime"]["imageId"]
    require(command(["docker", "image", "inspect", image, "--format", "{{.Os}}/{{.Architecture}}"], clean, execute) == policy["runtime"]["platform"], "Loaded offline image has the wrong platform")
    prefix = ["docker", "run", "--rm", "--network", "none", "--entrypoint", "node", image]
    require(command(prefix + ["--version"], clean, execute) == policy["runtime"]["node"], "Offline Node version differs")
    require(command(prefix + ["-p", "require('/opt/wrangler/node_modules/wrangler/package.json').version"], clean, execute) == policy["runtime"]["wrangler"], "Offline Wrangler version differs")


def record(path, approved, event, **fields):
    line = json.dumps({**approved, "event": event, **fields})
    with Path(path).open("a", encoding="utf8") as target:
        target.write(line + "\n")
        target.flush()
        os.fsync(target.fileno())
    print(line, flush=True)


def container_name(environment, app):
    run, attempt = environment.get("GITHUB_RUN_ID", ""), environment.get("GITHUB_RUN_ATTEMPT", "")
    require(re.fullmatch(r"[1-9][0-9]{0,19}", run) and re.fullmatch(r"[1-9][0-9]{0,9}", attempt) and app in NAMES, "Invalid owned-container identity")
    return f"astilba-recovery-{run}-{attempt}-{app}"


def cleanup(approved, journal, environment, apps, execute=subprocess.run):
    clean = clean_environment(environment)
    for app in apps:
        name = container_name(environment, app)
        inspect = ["docker", "container", "ls", "--all", "--filter", f"name=^/{name}$", "--format", "{{.ID}}"]
        try:
            if command(inspect, clean, execute):
                command(["docker", "container", "rm", "--force", name], clean, execute)
            require(not command(inspect, clean, execute), "Owned container still exists")
        except Exception:
            record(journal, approved, "cleanup-unresolved", app=app)
            raise
        record(journal, approved, "cleanup-completed", app=app)


def smoke(approved, directory, journal, clean, app, phase, execute):
    record(journal, approved, f"{phase}-started", app=app)
    try:
        command(["node", str(directory / "frozen-smoke" / SMOKES[app])], clean, execute)
    except Exception:
        record(journal, approved, f"{phase}-failed", app=app)
        raise
    record(journal, approved, f"{phase}-passed", app=app)


def restore(policy, approved, directory, journal, environment, execute=subprocess.run, pause=time.sleep):
    directory = Path(directory)
    manifest = json.loads((directory / "manifest.json").read_text())
    require(digest(directory / "manifest.json") == policy["capsule"]["manifestSha256"], "Unapproved extracted manifest")
    require(environment.get("CLOUDFLARE_API_TOKEN") and environment.get("CLOUDFLARE_ACCOUNT_ID"), "Scoped Cloudflare credentials are absent")
    clean = clean_environment(environment)
    provider = clean | {key: environment[key] for key in ["CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"]}
    for app in SCOPES[approved["scope"]]:
        current_main(approved["main"], execute)
        original = directory / "capsule/baseline" / app
        validate_app(original, app, manifest)
        with tempfile.TemporaryDirectory(prefix="astilba-recovery-") as temporary:
            work = Path(temporary) / app
            shutil.copytree(original, work)
            validate_app(work, app, manifest)
            arguments = ["docker", "run", "--rm", "--name", container_name(environment, app), "--network", "host", "--env", "CI=true", "--env", "WRANGLER_SEND_METRICS=false", "--env", "WRANGLER_LOG_SANITIZE=true", "--env", "CLOUDFLARE_API_TOKEN", "--env", "CLOUDFLARE_ACCOUNT_ID", "--mount", f"type=bind,src={work},dst=/work", "--workdir", "/work", "--entrypoint", "node", policy["runtime"]["imageId"], "/opt/wrangler/node_modules/wrangler/bin/wrangler.js", "deploy", "--no-bundle", "--config", "/work/restore-wrangler.json"]
            record(journal, approved, "apply-started", app=app)
            try:
                command(arguments, provider, execute)
            except Exception:
                record(journal, approved, "apply-indeterminate", app=app)
                raise
            finally:
                cleanup(approved, journal, environment, [app], execute)
            record(journal, approved, "apply-completed", app=app)
            smoke(approved, directory, journal, clean, app, "smoke", execute)
    # Keep the unchanged Docs MCP limiter out of a repeated full-suite window.
    if "docs" in SCOPES[approved["scope"]]:
        pause(66)
    record(journal, approved, "integrated-started")
    try:
        for app in ["site", "docs", "showcase"]:
            smoke(approved, directory, journal, clean, app, "integrated-smoke", execute)
    except Exception:
        record(journal, approved, "integrated-failed")
        raise
    record(journal, approved, "integrated-passed")


def summary(journal, target):
    lines = ["## Recovery journal", "", "All `apply-started` apps may have changed, including interrupted or failed commands.", "", "```json"]
    lines += Path(journal).read_text().splitlines() if Path(journal).exists() else ['{"event":"no-apply-recorded-in-this-job"}']
    lines += ["```", ""]
    with Path(target).open("a") as output:
        output.write("\n".join(lines))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("operation", choices=["plan", "preflight", "verify", "apply", "cleanup", "summary"])
    args = parser.parse_args()
    environment = os.environ
    if args.operation == "summary":
        summary(environment["RECOVERY_JOURNAL"], environment["GITHUB_STEP_SUMMARY"])
        return
    policy = json.loads(POLICY.read_text())
    approved = context(policy, environment)
    if args.operation == "cleanup":
        cleanup(approved, environment["RECOVERY_JOURNAL"], environment, SCOPES[approved["scope"]])
        return
    current_main(approved["main"])
    if args.operation == "plan":
        with Path(environment["GITHUB_OUTPUT"]).open("a") as output:
            output.write(f"label={approved['scope']} / {approved['incident']} / {approved['main']}\n")
        with Path(environment["GITHUB_STEP_SUMMARY"]).open("a") as output:
            output.write("## Approval binds this recovery request\n\n```json\n" + json.dumps(approved, indent=2) + "\n```\n")
        return
    protected_environment(policy, environment.get("GITHUB_TOKEN"))
    if args.operation == "preflight":
        with Path(environment["GITHUB_OUTPUT"]).open("a") as output:
            for key, value in object_settings(environment).items():
                output.write(f"{key}={value}\n")
    elif args.operation == "verify":
        extract_capsule(policy, environment["CAPSULE_PATH"], environment["RECOVERY_DIRECTORY"])
        runtime(policy, environment["RUNTIME_PATH"], environment)
    elif args.operation == "apply":
        runtime(policy, environment["RUNTIME_PATH"], environment)
        restore(policy, approved, environment["RECOVERY_DIRECTORY"], environment["RECOVERY_JOURNAL"], environment)


if __name__ == "__main__":
    def interrupted(signum, frame):
        raise RecoveryError("Interrupted; cleanup and provider readback are required")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        main()
    except Exception:
        # Do not serialize provider output, environment data, or backup identities.
        print("Recovery stopped. Check preparation/approval, verified inputs, current main and journal; read back provider state before retrying.", file=sys.stderr)
        sys.exit(1)
