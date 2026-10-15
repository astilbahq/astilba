import copy
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

import recover as r

MAIN = "a" * 40


def policy():
    result = json.loads(r.POLICY.read_text())
    result["phase"] = "approved-for-activation"
    result["allowSelfApproval"] = False
    result["acceptance"] = dict.fromkeys(result["acceptance"], True)
    result["runtime"].update(sha256="b" * 64, bytes=20, imageId="sha256:" + "c" * 64)
    return result


def environment():
    return {"GITHUB_ACTIONS": "true", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "astilbahq/astilba", "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main", "RECOVERY_ENABLED": "true", "EXPECTED_MAIN_SHA": MAIN, "GITHUB_SHA": MAIN, "RECOVERY_SCOPE": "all", "RECOVERY_INCIDENT": "INC-123", "PATH": os.environ["PATH"], "CLOUDFLARE_API_TOKEN": "private-test-marker", "CLOUDFLARE_ACCOUNT_ID": "private-account-marker", "AWS_SECRET_ACCESS_KEY": "private-aws-marker", "GITHUB_TOKEN": "private-github-marker"}


def archive(path, entries):
    with tarfile.open(path, "w:gz") as target:
        for entry in entries:
            name, data = entry[:2]
            member = tarfile.TarInfo(name)
            member.size = len(data)
            if len(entry) == 3:
                member.type = entry[2]
                member.linkname = "/outside-target"
            target.addfile(member, io.BytesIO(data))


class RecoveryTests(unittest.TestCase):
    def test_preparation_is_inactive_and_each_gate_is_required(self):
        with self.assertRaises(r.RecoveryError):
            r.activated(json.loads(r.POLICY.read_text()))
        accepted = policy()
        r.activated(accepted)
        for key in accepted["acceptance"]:
            rejected = copy.deepcopy(accepted)
            rejected["acceptance"][key] = False
            with self.subTest(gate=key), self.assertRaises(r.RecoveryError):
                r.activated(rejected)
        for key in ["sha256", "bytes", "imageId"]:
            rejected = copy.deepcopy(accepted)
            rejected["runtime"][key] = None
            with self.subTest(runtime=key), self.assertRaises(r.RecoveryError):
                r.activated(rejected)
        accepted["allowSelfApproval"] = None
        with self.assertRaises(r.RecoveryError):
            r.activated(accepted)

    def test_dispatch_scope_sha_and_incident_are_bound(self):
        accepted = policy()
        self.assertEqual(r.context(accepted, environment()), {"main": MAIN, "scope": "all", "incident": "INC-123", "capsule": accepted["capsule"]["sha256"]})
        for key, value in [("GITHUB_ACTIONS", "false"), ("GITHUB_REPOSITORY", "other/repo"), ("GITHUB_EVENT_NAME", "push"), ("GITHUB_REF", "refs/tags/main"), ("RECOVERY_ENABLED", "false"), ("EXPECTED_MAIN_SHA", "b" * 40), ("RECOVERY_SCOPE", "../../other"), ("RECOVERY_INCIDENT", "bad\ncommand"), ("RECOVERY_INCIDENT", "`code`")]:
            with self.subTest(key=key), self.assertRaises(r.RecoveryError):
                r.context(accepted, environment() | {key: value})

    def test_current_head_check_strips_credentials_and_rejects_stale(self):
        calls = []
        def execute(arguments, **kwargs):
            calls.append(kwargs)
            return subprocess.CompletedProcess(arguments, 0, stdout=f"{MAIN}\trefs/heads/main\n")
        r.current_main(MAIN, execute)
        self.assertNotIn("CLOUDFLARE_API_TOKEN", calls[0]["env"])
        with self.assertRaises(r.RecoveryError):
            r.current_main("b" * 40, execute)

    def test_live_environment_protection_and_immutable_identity(self):
        accepted = policy()
        responses = {
            "environments/production-web": {"can_admins_bypass": False, "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True}, "protection_rules": [{"type": "required_reviewers", "prevent_self_review": True, "reviewers": [{"type": "User", "reviewer": {"id": accepted["reviewerId"]}}]}]},
            "environments/production-web/deployment-branch-policies?per_page=100": {"total_count": 1, "branch_policies": [{"name": "main", "type": "branch"}]},
            "actions/oidc/customization/sub": {"use_default": True, "use_immutable_subject": True, "sub_claim_prefix": accepted["oidcSubjectPrefix"]},
        }
        read = lambda path, token: responses[path]
        r.protected_environment(accepted, "not-a-real-token", read)
        mutations = [("environments/production-web", "can_admins_bypass", True), ("actions/oidc/customization/sub", "use_immutable_subject", False), ("actions/oidc/customization/sub", "sub_claim_prefix", "repo:other/repo")]
        for path, key, value in mutations:
            old = responses[path][key]
            responses[path][key] = value
            with self.subTest(field=key), self.assertRaises(r.RecoveryError):
                r.protected_environment(accepted, "token", read)
            responses[path][key] = old
        responses["environments/production-web/deployment-branch-policies?per_page=100"]["branch_policies"][0]["type"] = "tag"
        with self.assertRaises(r.RecoveryError):
            r.protected_environment(accepted, "token", read)

    def test_immutable_object_configuration_has_no_latest_fallback(self):
        values = {"S3_BUCKET": "example-backup", "AWS_REGION": "eu-west-2", "READER_ROLE_ARN": "arn:aws:iam::123456789012:role/recovery", "CAPSULE_KEY": "capsule.tgz", "CAPSULE_VERSION": "version-one", "RUNTIME_KEY": "runtime.tgz", "RUNTIME_VERSION": "version-two"}
        variables = {f"RECOVERY_{key}": value for key, value in values.items()}
        self.assertEqual(r.object_settings(variables)["capsule_version"], "version-one")
        for key in values:
            with self.subTest(key=key), self.assertRaises(r.RecoveryError):
                r.object_settings(variables | {f"RECOVERY_{key}": ""})
        with self.assertRaises(r.RecoveryError):
            r.object_settings(variables | {"RECOVERY_CAPSULE_VERSION": "null"})

    def test_archive_identity_and_hostile_members_rejected_before_extraction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "input.tgz"
            for index, entries in enumerate([[('../escape', b'x')], [('/absolute', b'x')], [('safe\\escape', b'x')], [('same', b'x'), ('same', b'y')], [('link', b'', tarfile.SYMTYPE)], [('link', b'', tarfile.LNKTYPE)], [('pipe', b'', tarfile.FIFOTYPE)]]):
                archive(source, entries)
                destination = root / f"out-{index}"
                with self.subTest(entries=entries), self.assertRaises(r.RecoveryError):
                    r.safe_extract(source, destination, lambda name: True)
                self.assertFalse(destination.exists())
            source.write_bytes(b"altered")
            with self.assertRaises(r.RecoveryError):
                r.authenticate(source, {"bytes": 7, "sha256": "0" * 64})

    def fixture(self, directory):
        manifest = {"artifacts": {"baseline": {}}}
        for app in r.NAMES:
            target = directory / "capsule/baseline" / app
            (target / "assets/.well-known").mkdir(parents=True)
            (target / "assets/index.html").write_text("accepted static bytes")
            (target / "assets/.well-known/catalog").write_text("discovery")
            config = {"name": r.NAMES[app], "routes": [{"pattern": "astilba.com"}], "assets": {"directory": "./assets"}}
            if app != "showcase":
                (target / "worker").mkdir()
                (target / "worker/index.js").write_text("precompiled code")
                config["main"] = "./worker/index.js"
            (target / "restore-wrangler.json").write_text(json.dumps(config))
            manifest["artifacts"]["baseline"][app] = {"workerName": r.NAMES[app], "originalCommit": r.COMMITS[app], "routes": config["routes"], "files": r.files(target)}
        (directory / "manifest.json").write_text(json.dumps(manifest))
        accepted = policy()
        accepted["capsule"]["manifestSha256"] = r.digest(directory / "manifest.json")
        return accepted, manifest

    def test_file_identity_and_hidden_assets_are_complete(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            _, manifest = self.fixture(directory)
            app = directory / "capsule/baseline/docs"
            r.validate_app(app, "docs", manifest)
            (app / "assets/.well-known/catalog").unlink()
            with self.assertRaises(r.RecoveryError):
                r.validate_app(app, "docs", manifest)

    def test_capsule_extracts_exact_manifest_files_not_mac_tar_metadata(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            accepted, manifest = self.fixture(source)
            frozen = source / "candidate-public-source.tar"
            archive(frozen, [(name, b"frozen source") for name in r.SMOKE_FILES])
            manifest["sources"] = {"candidate": {"sha256": r.digest(frozen)}}
            (source / "manifest.json").write_text(json.dumps(manifest))
            accepted["capsule"]["manifestSha256"] = r.digest(source / "manifest.json")
            entries = [(path.relative_to(source).as_posix(), path.read_bytes()) for path in source.rglob("*") if path.is_file()]
            entries += [("capsule/baseline/site/assets/._index.html", b"mac metadata"), ("capsule/baseline/docs/unapproved", b"not an approved app file")]
            capsule = root / "capsule.tgz"
            archive(capsule, entries)
            accepted["capsule"].update(bytes=capsule.stat().st_size, sha256=r.digest(capsule))
            destination = root / "extracted"
            r.extract_capsule(accepted, capsule, destination)
            self.assertFalse((destination / "capsule/baseline/site/assets/._index.html").exists())
            self.assertFalse((destination / "capsule/baseline/docs/unapproved").exists())
            self.assertEqual(set(r.files(destination / "frozen-smoke")), r.SMOKE_FILES)

    def test_reverse_order_journal_before_write_and_credential_isolation(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            accepted, _ = self.fixture(directory)
            journal = directory / "journal.jsonl"
            approved = r.context(accepted, environment())
            apps = []
            def execute(arguments, **kwargs):
                if arguments[0] == "git":
                    return subprocess.CompletedProcess(arguments, 0, stdout=f"{MAIN}\trefs/heads/main\n")
                if arguments[:2] == ["docker", "run"]:
                    records = [json.loads(line) for line in journal.read_text().splitlines()]
                    self.assertEqual(records[-1]["event"], "apply-started")
                    apps.append(records[-1]["app"])
                    self.assertIn("private-test-marker", kwargs["env"].values())
                    self.assertNotIn("private-test-marker", str(arguments))
                    self.assertNotIn("AWS_SECRET_ACCESS_KEY", kwargs["env"])
                elif arguments[0] == "node":
                    self.assertEqual(arguments[0], "node")
                    self.assertFalse(set(kwargs["env"]) & {"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN"})
                return subprocess.CompletedProcess(arguments, 0, stdout="")
            pauses = []
            r.restore(accepted, approved, directory, journal, environment(), execute, pauses.append)
            self.assertEqual(apps, ["showcase", "docs", "site"])
            self.assertEqual(pauses, [66])
            events = [json.loads(line) for line in journal.read_text().splitlines()]
            final = [(event["app"], event["event"]) for event in events if event["event"] == "integrated-smoke-passed"]
            self.assertEqual(final, [(app, "integrated-smoke-passed") for app in ["site", "docs", "showcase"]])
            self.assertEqual(events[-1]["event"], "integrated-passed")
            self.assertNotIn("private-", journal.read_text())
            for app in r.NAMES:
                self.assertFalse((directory / "capsule/baseline" / app / ".wrangler").exists())

    def test_indeterminate_apply_and_failed_smoke_stop_later_apps(self):
        for failure in ["apply", "smoke"]:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                accepted, _ = self.fixture(directory)
                journal = directory / "journal.jsonl"
                approved = r.context(accepted, environment())
                apps = []
                def execute(arguments, **kwargs):
                    if arguments[0] == "git":
                        return subprocess.CompletedProcess(arguments, 0, stdout=f"{MAIN}\trefs/heads/main\n")
                    if arguments[:2] == ["docker", "run"]:
                        apps.append(json.loads(journal.read_text().splitlines()[-1])["app"])
                    bad = (failure == "apply" and arguments[:2] == ["docker", "run"]) or (failure == "smoke" and arguments[0] == "node")
                    return subprocess.CompletedProcess(arguments, int(bad), stdout="")
                with self.assertRaises(r.RecoveryError):
                    r.restore(accepted, approved, directory, journal, environment(), execute)
                self.assertEqual(apps, ["showcase"])
                events = [json.loads(line)["event"] for line in journal.read_text().splitlines()]
                self.assertIn("apply-indeterminate" if failure == "apply" else "smoke-failed", events)
                self.assertNotIn("integrated-started", events)

    def test_site_only_still_checks_all_apps_and_integrated_failure_propagates(self):
        for fail in [False, True]:
            with self.subTest(fail=fail), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                accepted, _ = self.fixture(directory)
                journal = directory / "journal.jsonl"
                env = environment() | {"RECOVERY_SCOPE": "site"}
                approved = r.context(accepted, env)
                smokes, pauses, applies = [], [], []
                def execute(arguments, **kwargs):
                    if arguments[0] == "git":
                        return subprocess.CompletedProcess(arguments, 0, stdout=f"{MAIN}\trefs/heads/main\n")
                    if arguments[:2] == ["docker", "run"]:
                        applies.append(arguments)
                    if arguments[0] == "node":
                        smokes.append(arguments[1])
                        self.assertTrue(all(json.loads(line)["event"] != "apply-started" or json.loads(line)["app"] == "site" for line in journal.read_text().splitlines()))
                    bad = fail and arguments[0] == "node" and arguments[1].endswith(r.SMOKES["docs"])
                    return subprocess.CompletedProcess(arguments, int(bad), stdout="")
                if fail:
                    with self.assertRaises(r.RecoveryError):
                        r.restore(accepted, approved, directory, journal, env, execute, pauses.append)
                else:
                    r.restore(accepted, approved, directory, journal, env, execute, pauses.append)
                self.assertEqual(len(applies), 1)
                self.assertEqual(pauses, [])
                expected = ["site", "site", "docs"] + ([] if fail else ["showcase"])
                self.assertEqual(smokes, [str(directory / "frozen-smoke" / r.SMOKES[app]) for app in expected])
                self.assertEqual(json.loads(journal.read_text().splitlines()[-1])["event"], "integrated-failed" if fail else "integrated-passed")

    def test_timeout_stops_only_owned_container_and_cleanup_failure_is_indeterminate(self):
        for cleanup_fails in [False, True]:
            with self.subTest(cleanup_fails=cleanup_fails), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                accepted, _ = self.fixture(directory)
                journal = directory / "journal.jsonl"
                approved = r.context(accepted, environment())
                calls, inspections = [], []
                def execute(arguments, **kwargs):
                    calls.append(arguments)
                    if arguments[0] == "git":
                        return subprocess.CompletedProcess(arguments, 0, stdout=f"{MAIN}\trefs/heads/main\n")
                    if arguments[:2] == ["docker", "run"]:
                        self.assertEqual(arguments[arguments.index("--name") + 1], "astilba-recovery-123-1-showcase")
                        raise subprocess.TimeoutExpired(arguments, 300)
                    self.assertNotIn("CLOUDFLARE_API_TOKEN", kwargs["env"])
                    if arguments[:3] == ["docker", "container", "ls"]:
                        inspections.append(arguments)
                        return subprocess.CompletedProcess(arguments, 0, stdout="owned-id" if len(inspections) == 1 else "")
                    self.assertEqual(arguments, ["docker", "container", "rm", "--force", "astilba-recovery-123-1-showcase"])
                    return subprocess.CompletedProcess(arguments, int(cleanup_fails), stdout="")
                with self.assertRaises((subprocess.TimeoutExpired, r.RecoveryError)):
                    r.restore(accepted, approved, directory, journal, environment(), execute)
                events = [json.loads(line)["event"] for line in journal.read_text().splitlines()]
                self.assertEqual(events, ["apply-started", "apply-indeterminate", "cleanup-unresolved" if cleanup_fails else "cleanup-completed"])
                self.assertFalse(any(arguments[0] == "node" for arguments in calls))

    def test_runtime_mismatch_never_loads_or_executes_image(self):
        with tempfile.TemporaryDirectory() as temporary:
            archive_path = Path(temporary) / "runtime"
            archive_path.write_bytes(b"wrong")
            calls = []
            with self.assertRaises(r.RecoveryError):
                r.runtime(policy(), archive_path, environment(), lambda *args, **kwargs: calls.append(args))
            self.assertEqual(calls, [])

    def test_summary_contains_attempts_even_without_completed_write(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            journal = directory / "journal"
            output = directory / "summary"
            r.record(journal, {"scope": "all"}, "apply-started", app="showcase")
            r.summary(journal, output)
            self.assertIn("apply-started", output.read_text())
            self.assertIn("interrupted", output.read_text())


if __name__ == "__main__":
    unittest.main()
