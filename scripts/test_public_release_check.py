"""Synthetic publication checks; temporary Git indexes only, no real credentials."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import public_release_check as gate


def synthetic_token() -> str:
    return "gh" + "p_" + "SyntheticOnlyNotAnActualCredential" + "0" * 8


class PinnedBrandAssetTests(unittest.TestCase):
    """Exact original bytes only; no network and no Git writes for these tests."""

    @classmethod
    def setUpClass(cls):
        cls.root = Path(__file__).resolve().parents[1]
        cls.asset = "web/src/assets/yishuld-logo.png"
        cls.original = (cls.root / cls.asset).read_bytes()

    def test_exact_original_is_the_only_pinned_binary(self):
        self.assertEqual({self.asset}, set(gate.PINNED_BINARY_ASSETS))
        self.assertEqual(534086, len(self.original))
        self.assertEqual("ec6c2acc4307de4e81858a228d014c0b9d173ece3b006a7f723dcadd567cf0c2",
                         gate.hashlib.sha256(self.original).hexdigest())
        self.assertTrue(self.original.startswith(b"\x89PNG\r\n\x1a\n"))
        self.assertTrue(gate.source_allowed(self.asset))
        self.assertEqual([], gate.scan_blob(self.asset, self.original))

    def test_other_image_paths_do_not_inherit_exception(self):
        for name in ("web/src/assets/another.png", "web/src/assets/Yishuld-logo.png",
                     "web/src/assets/yishuld-logo.jpg", "web/src/assets/sub/yishuld-logo.png",
                     "docs/yishuld-logo.png"):
            with self.subTest(path=name):
                self.assertFalse(gate.source_allowed(name))
                self.assertEqual([gate.Finding(name, 0, "outside-source-allowlist")],
                                 gate.scan_blob(name, self.original))

    def test_replaced_truncated_appended_and_same_length_changed_content_is_rejected(self):
        changed = self.original[:-1] + bytes([self.original[-1] ^ 1])
        for data in (b"", b"safe-looking text", b"\0", self.original[:-1], self.original + b"extra", changed):
            with self.subTest(size=len(data)):
                self.assertEqual([gate.Finding(self.asset, 0, "pinned-binary-asset-mismatch")],
                                 gate.scan_blob(self.asset, data))

    def test_binary_in_regular_source_remains_rejected(self):
        self.assertEqual([gate.Finding("web/src/logo.ts", 0, "binary-source-file")],
                         gate.scan_blob("web/src/logo.ts", self.original))

    def test_worktree_packaging_returns_original_bytes_and_rejects_replacement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            asset = root / self.asset
            asset.parent.mkdir(parents=True)
            asset.write_bytes(self.original)
            (asset.parent / "other.png").write_bytes(self.original)
            files, findings = gate.collect_worktree_sources(root)
            self.assertEqual([], findings)
            self.assertEqual({self.asset: self.original}, files)
            asset.write_bytes(self.original + b"changed")
            files, findings = gate.collect_worktree_sources(root)
            self.assertEqual({}, files)
            self.assertEqual([gate.Finding(self.asset, 0, "pinned-binary-asset-mismatch")], findings)

    def test_index_checks_index_bytes_not_safe_or_changed_worktree(self):
        # Stub only Git transport: exercise the real index parser/blob validation.
        # Existing GitIndexTests independently cover real isolated Git semantics.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            asset = root / self.asset
            asset.parent.mkdir(parents=True)
            for staged, worktree, expected in (
                (self.original + b"changed", self.original,
                 [gate.Finding(self.asset, 0, "pinned-binary-asset-mismatch")]),
                (self.original, b"changed worktree", []),
            ):
                with self.subTest(staged_size=len(staged)):
                    asset.write_bytes(worktree)
                    def read(_root, *args):
                        self.assertEqual(root, _root)
                        if args == ("rev-parse", "--show-toplevel"):
                            return str(root).encode() + b"\n"
                        if args == ("ls-files", "--stage", "-z"):
                            return f"100644 {'a' * 40} 0\t{self.asset}\0".encode()
                        if args == ("cat-file", "-s", ":" + self.asset):
                            return str(len(staged)).encode()
                        if args == ("show", "--no-ext-diff", "--no-textconv", ":" + self.asset):
                            return staged
                        self.fail("unexpected Git read")
                    with patch.object(gate, "git_read", side_effect=read):
                        self.assertEqual((expected, 1), gate.scan_index(root))

    def test_docker_context_has_only_exact_image_exception(self):
        rules = (self.root / ".dockerignore").read_text(encoding="utf-8").splitlines()
        image_rules = [line for line in rules if line.startswith("!") and
                       any(suffix in line.lower() for suffix in (".png", ".jpg", ".jpeg", ".gif", ".webp"))]
        self.assertEqual(["!" + self.asset], image_rules)
        for broad in ("!web/**", "!web/src/**", "!web/src/assets/**", "!**/*.png"):
            self.assertNotIn(broad, rules)


class ContentTests(unittest.TestCase):
    def test_high_confidence_credential_patterns(self):
        samples = [
            ("AK" + "IA" + "Z" * 16, "aws-access-key-id"),
            ("AS" + "IA" + "Z" * 16, "aws-access-key-id"),
            ("AK" + "ID" + "Z" * 28, "cloud-access-key-id"),
            ("LT" + "AI" + "Z" * 20, "cloud-access-key-id"),
            ("AK" + "LT" + "Z" * 20, "cloud-access-key-id"),
            (synthetic_token(), "github-token"),
            ("github_" + "pat_" + "Z" * 30, "github-token"),
            ("-----BEGIN " + "OPENSSH PRIVATE KEY-----", "private-key-block"),
            ("-----BEGIN " + "PRIVATE KEY-----", "private-key-block"),
        ]
        for content, rule in samples:
            with self.subTest(rule=rule):
                self.assertEqual([gate.Finding("README.md", 2, rule)], gate.scan_blob("README.md", ("heading\n" + content).encode()))

    def test_absolute_user_paths(self):
        paths = ["C:" + "\\Users\\synthetic-user\\work", "D:" + "\\private-work\\sample",
                 "/home" + "/synthetic-user/work", "/Users" + "/synthetic-user/work", "/root" + "/.ssh/example"]
        for value in paths:
            self.assertEqual("absolute-user-path", gate.scan_blob("README.md", value.encode())[0].rule)

    def test_demo_passwords_hashes_placeholders_urls_are_not_secrets(self):
        content = "\n".join([
            "demo:demo-refund-only@tcp(127.0.0.1:3307)/refund_test",
            "MYSQL_ROOT_PASSWORD=local-refund-root-only", "MYSQL_PASSWORD=", "Bearer demo-a-write",
            "sha256:" + "f" * 64, '"integrity": "sha512-' + "a" * 80 + '=="',
            "https://example.invalid/api", "${MYSQL_PASSWORD:?set required}",
        ])
        self.assertEqual([], gate.scan_blob(".env.example", content.encode()))
        self.assertEqual([], gate.scan_blob("web/package-lock.json", content.encode()))

    def test_reports_do_not_contain_matched_values_or_line_contents(self):
        secret = synthetic_token()
        findings = gate.scan_blob("README.md", ("not secret\nprefix " + secret + " suffix").encode())
        output = io.StringIO()
        with redirect_stdout(output):
            gate.emit_findings(findings)
        self.assertEqual({"file": "README.md", "line": 2, "rule": "github-token"}, json.loads(output.getvalue()))
        self.assertNotIn(secret, output.getvalue())
        self.assertNotIn("prefix", output.getvalue())

    def test_sensitive_paths_are_rejected_without_examining_contents(self):
        paths = [".env", "deploy/.env.production", ".runtime/log.txt", "evidence/report.md", "logs/out.txt",
                 "bin/server", "deps/file.ts", "archives/result.zip", "private/note.md", "credentials/config.json",
                 "web/src/.npmrc", "web/src/cert.pem", "web/src/id_ed25519", "web/node_modules/x/index.ts"]
        for path in paths:
            with self.subTest(path=path):
                self.assertTrue(gate.scan_blob(path, b""))
                self.assertFalse(gate.source_allowed(path))
        self.assertTrue(gate.source_allowed("deploy/.env.example"))

    def test_binary_non_utf8_oversize_and_unknown_source_fail_closed(self):
        for path, data, rule in [("README.md", b"\0", "binary-source-file"), ("README.md", b"\xff", "non-utf8-source-file"),
                                 ("README.md", b"x" * (gate.MAX_SOURCE_BYTES + 1), "source-file-too-large"),
                                 ("unknown.txt", b"safe", "outside-source-allowlist")]:
            self.assertEqual(rule, gate.scan_blob(path, data)[0].rule)

    def test_unsafe_filenames_cannot_inject_output_or_escape(self):
        for path in ("../README.md", "/README.md", "web\\src\\x.ts", "README\n.md", "C:" + "/example"):
            self.assertEqual([gate.Finding("<invalid-path>", 0, "invalid-relative-path")], gate.scan_blob(path, b""))


class WorktreeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")
        return target

    def test_worktree_is_explicit_allowlist_and_does_not_read_private_data(self):
        self.write("README.md", "safe source")
        self.write("web/src/main.ts", "export const safe = true")
        for path in (".env", "evidence/out.md", ".runtime/out.md", "web/node_modules/README.md", "unknown.txt"):
            self.write(path, synthetic_token())
        findings, count = gate.scan_worktree(self.root)
        self.assertEqual([], findings)
        self.assertEqual(2, count)

    def test_allowed_source_is_scanned(self):
        self.write("web/src/main.ts", synthetic_token())
        self.assertEqual([gate.Finding("web/src/main.ts", 1, "github-token")], gate.scan_worktree(self.root)[0])

    def test_packaging_api_returns_checked_bytes_not_later_worktree_content(self):
        source = self.write("README.md", "checked source")
        files, findings = gate.collect_worktree_sources(self.root)
        source.write_text(synthetic_token(), encoding="utf-8")
        self.assertEqual([], findings)
        self.assertEqual({"README.md": b"checked source"}, files)

    def test_source_symlink_is_rejected_without_following(self):
        target = self.write("private/value.txt", synthetic_token())
        link = self.root / "README.md"
        try:
            link.symlink_to(target)
        except (OSError, NotImplementedError):
            self.skipTest("symlinks unavailable on this platform")
        self.assertIn(gate.Finding("README.md", 0, "source-link-not-allowed"), gate.scan_worktree(self.root)[0])

    def test_missing_git_index_does_not_fall_back_to_worktree(self):
        self.write("README.md", "safe")
        output = io.StringIO()
        with redirect_stdout(output):
            code = gate.main(["--root", str(self.root)])
        self.assertEqual(2, code)
        self.assertEqual("git-index-unavailable", json.loads(output.getvalue())["rule"])
        self.assertNotIn(str(self.root), output.getvalue())


@unittest.skipUnless(shutil.which("git"), "Git needed for isolated index semantics tests")
class GitIndexTests(WorktreeTests):
    def setUp(self):
        super().setUp()
        self.git("init", "--quiet")

    def git(self, *args, input_data=None):
        return subprocess.run(["git", "-C", str(self.root), *args], check=True, input=input_data, capture_output=True).stdout

    def test_staged_secret_is_detected_when_worktree_has_been_cleaned(self):
        self.write("README.md", synthetic_token())
        self.git("add", "--", "README.md")
        self.write("README.md", "safe worktree copy")
        self.assertEqual([gate.Finding("README.md", 1, "github-token")], gate.scan_index(self.root)[0])
        self.assertEqual([], gate.scan_worktree(self.root)[0])

    def test_unstaged_secret_does_not_replace_the_safe_index_blob(self):
        self.write("README.md", "safe staged copy")
        self.git("add", "--", "README.md")
        self.write("README.md", synthetic_token())
        self.assertEqual(([], 1), gate.scan_index(self.root))

    def test_staged_private_path_is_rejected_even_if_worktree_copy_is_absent(self):
        target = self.write(".env.production", "synthetic-not-actual")
        self.git("add", "--", ".env.production")
        target.unlink()
        self.assertEqual([gate.Finding(".env.production", 0, "non-example-environment-file")], gate.scan_index(self.root)[0])

    def test_index_symlink_is_rejected_without_reading_target(self):
        oid = self.git("hash-object", "-w", "--stdin", input_data=b"private/value.txt").decode().strip()
        self.git("update-index", "--add", "--cacheinfo", f"120000,{oid},README.md")
        self.assertEqual([gate.Finding("README.md", 0, "non-regular-index-entry")], gate.scan_index(self.root)[0])

    def test_unresolved_index_stages_are_not_published(self):
        oid = self.git("hash-object", "-w", "--stdin", input_data=b"synthetic merge content").decode().strip()
        self.git("update-index", "--index-info", input_data=f"100644 {oid} 1\tREADME.md\n100644 {oid} 2\tREADME.md\n".encode())
        self.assertEqual([gate.Finding("README.md", 0, "unmerged-index-entry")], gate.scan_index(self.root)[0])

    def test_empty_index_and_ancestor_repository_fail_closed(self):
        self.assertEqual([gate.Finding("<index>", 0, "empty-index")], gate.scan_index(self.root)[0])
        child = self.root / "untracked-project"
        child.mkdir()
        with self.assertRaises(gate.IndexUnavailable):
            gate.scan_index(child)

    def test_missing_git_index_does_not_fall_back_to_worktree(self):
        # Override inherited no-repository case: this class intentionally has Git.
        nested = self.root / "untracked-project"
        nested.mkdir()
        output = io.StringIO()
        with redirect_stdout(output):
            code = gate.main(["--root", str(nested)])
        self.assertEqual(2, code)
        self.assertEqual("git-index-unavailable", json.loads(output.getvalue())["rule"])


class CIContractTests(unittest.TestCase):
    def test_workflow_is_read_only_pinned_and_does_not_deploy(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("contents: read", workflow)
        self.assertNotIn("pull_request_target", workflow)
        self.assertNotIn("secrets.", workflow)
        self.assertNotIn("docker push", workflow)
        self.assertIn("persist-credentials: false", workflow)
        for reference in ("actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803", "actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16", "actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38"):
            self.assertIn(reference, workflow)

    def test_workflow_runs_linux_start_and_scoped_real_deployment_acceptance(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        start = "bash scripts/start.sh --project refund-deploy-test-ci --host 127.0.0.1 --port 28080"
        stop = "docker compose -f compose.yaml -p yishuld-refund stop"
        self.assertIn(start, workflow)
        self.assertLess(workflow.index("python3 scripts/reset_acceptance.py"), workflow.index(stop))
        self.assertLess(workflow.index(stop), workflow.index(start))
        self.assertIn("python3 scripts/deployment_acceptance.py", workflow)
        self.assertIn("--env-file .runtime/start-refund-deploy-test-ci.env", workflow)
        self.assertIn("--base-url http://127.0.0.1:28080", workflow)
        self.assertEqual(2, workflow.count("if: always()"))
        self.assertIn("-p refund-deploy-test-ci down --volumes --remove-orphans", workflow)
        self.assertIn("-p yishuld-refund down --volumes --remove-orphans", workflow)
        self.assertNotIn("docker system prune", workflow)


if __name__ == "__main__":
    unittest.main(verbosity=2)
