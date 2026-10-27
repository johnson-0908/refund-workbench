"""Fail-closed source publication checks, not a complete secret scanner.

Default: inspect staged Git blobs (not worktree copies or Git history).
--worktree: inspect only the explicit source allowlist; private/generated files
outside it are not candidates. This preview is NOT approval of a future index.
Findings contain only relative file, line and rule, never matched content.
"""
from __future__ import annotations

import argparse
from dataclasses import asdict, dataclass
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
from typing import Iterable

MAX_SOURCE_BYTES = 4 * 1024 * 1024
ROOT_FILES = frozenset({
    ".dockerignore", ".gitignore", ".gitattributes", ".env.example",
    "AGENTS.md", "AI_USAGE.md", "DESIGN.md", "README.md", "LICENSE", "NOTICE",
    "compose.yaml", "compose.build.yaml", "compose.deploy.yaml",
    "Dockerfile.backend", "Dockerfile.web",
})
EXACT_FILES = ROOT_FILES | frozenset({
    "backend/go.mod", "backend/go.sum", "web/package.json", "web/package-lock.json",
    "web/tsconfig.json", "web/vite.config.ts", "web/index.html",
    "deploy/.env.example", "deploy/README.md", "deploy/nginx.conf", "deploy/web-image-lock.json",
})
SOURCE_TREES = ("backend/cmd", "backend/internal", "web/src", "scripts", "docs", ".github/workflows")
PRIVATE_PARTS = frozenset({
    ".git", ".runtime", "evidence", ".evidence", "logs", "log", "bin", "deps",
    "archives", "private", "credentials", ".credentials", ".aws", ".ssh", ".docker",
    "node_modules", "vendor", "dist", "coverage", "__pycache__", "reports",
    "exports", "samples", "cases", "playwright-report", "test-results",
})
PRIVATE_NAMES = frozenset({
    ".npmrc", ".pypirc", ".netrc", "_netrc", "id_rsa", "id_dsa", "id_ecdsa",
    "id_ed25519", "credentials.json", "credentials.yaml", "credentials.yml",
})
PRIVATE_SUFFIXES = (".pem", ".key", ".pfx", ".p12", ".jks", ".keystore", ".log", ".exe",
                    ".dll", ".zip", ".zip.sha256", ".7z", ".tar", ".tar.gz", ".bak")

# Deliberately high-confidence forms, not generic "password=" or entropy checks:
# public fictional demo passwords, dependency integrity hashes and CSRF variable
# names must remain publishable. Provider formats will evolve beyond this list.
CONTENT_RULES = (
    ("aws-access-key-id", re.compile(r"(?<![A-Za-z0-9])(?:AKIA|ASIA)[A-Z0-9]{16}(?![A-Za-z0-9])")),
    ("cloud-access-key-id", re.compile(r"(?<![A-Za-z0-9])(?:AKID[A-Za-z0-9]{16,64}|LTAI[A-Za-z0-9]{12,64}|AKLT[A-Za-z0-9]{12,64})(?![A-Za-z0-9])")),
    ("github-token", re.compile(r"(?<![A-Za-z0-9_])(?:gh[pousr]_[A-Za-z0-9]{20,255}|github_pat_[A-Za-z0-9_]{22,255})(?![A-Za-z0-9_])")),
    ("private-key-block", re.compile(r"-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----")),
    ("absolute-user-path", re.compile(r"(?<![A-Za-z0-9])[A-Za-z]:[\\/][^\s\"'<>`]+|(?<![A-Za-z0-9])/(?:Users|home)/[^/\s\"'<>`]+/|(?<![A-Za-z0-9])/" + r"root/[^\s\"'<>`]+")),
)


@dataclass(frozen=True, order=True)
class Finding:
    file: str
    line: int
    rule: str


class IndexUnavailable(RuntimeError):
    """An index cannot be verified; no fallback to worktree is allowed."""


def safe_relative(path: str) -> bool:
    parts = PurePosixPath(path).parts
    return bool(parts) and path == PurePosixPath(path).as_posix() and not path.startswith("/") and "\\" not in path and ":" not in path and all(
        part not in (".", "..") and not any(ord(char) < 32 or ord(char) == 127 for char in part)
        for part in parts
    )


def path_rule(path: str) -> str | None:
    if not safe_relative(path):
        return "invalid-relative-path"
    parts = PurePosixPath(path).parts
    lowered = [part.lower() for part in parts]
    if any(part in PRIVATE_PARTS for part in lowered):
        return "private-or-generated-path"
    name = lowered[-1]
    if (name == ".env" or name.startswith(".env.")) and name != ".env.example":
        return "non-example-environment-file"
    if name in PRIVATE_NAMES or name.endswith(PRIVATE_SUFFIXES):
        return "credential-or-binary-file"
    return None


def source_allowed(path: str) -> bool:
    if path_rule(path):
        return False
    if path in EXACT_FILES:
        return True
    name = PurePosixPath(path)
    if path.startswith(("backend/cmd/", "backend/internal/")):
        return name.suffix == ".go"
    if path.startswith("web/src/"):
        return name.suffix in {".ts", ".vue", ".css"}
    if path.startswith("docs/"):
        return name.suffix == ".md"
    if len(name.parts) == 2 and name.parts[0] == "scripts":
        return name.suffix in {".py", ".ps1", ".sh", ".sql"}
    return len(name.parts) == 3 and name.parts[:2] == (".github", "workflows") and name.suffix in {".yml", ".yaml"}


def source_directory(path: str) -> bool:
    if path_rule(path):
        return False
    return any(tree == path or tree.startswith(path + "/") or path.startswith(tree + "/") for tree in SOURCE_TREES) or any(
        name.startswith(path + "/") for name in EXACT_FILES
    )


def scan_blob(path: str, data: bytes) -> list[Finding]:
    rule = path_rule(path)
    if rule:
        return [Finding(path if safe_relative(path) else "<invalid-path>", 0, rule)]
    if not source_allowed(path):
        return [Finding(path, 0, "outside-source-allowlist")]
    if len(data) > MAX_SOURCE_BYTES:
        return [Finding(path, 0, "source-file-too-large")]
    if b"\0" in data:
        return [Finding(path, 0, "binary-source-file")]
    try:
        text = data.decode("utf-8-sig")
    except UnicodeDecodeError:
        return [Finding(path, 0, "non-utf8-source-file")]
    return [Finding(path, line_number, name) for line_number, line in enumerate(text.splitlines(), 1)
            for name, expression in CONTENT_RULES if expression.search(line)]


def git_read(root: Path, *args: str) -> bytes:
    try:
        result = subprocess.run(["git", "--no-pager", "-C", str(root), *args], capture_output=True,
                                env={**os.environ, "GIT_OPTIONAL_LOCKS": "0"}, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise IndexUnavailable from error
    if result.returncode:
        raise IndexUnavailable
    return result.stdout


def scan_index(root: Path) -> tuple[list[Finding], int]:
    # Refuse to silently scan an ancestor repository if this source is untracked.
    try:
        git_root = Path(os.fsdecode(git_read(root, "rev-parse", "--show-toplevel")).strip()).resolve()
    except (OSError, ValueError) as error:
        raise IndexUnavailable from error
    if git_root != root.resolve():
        raise IndexUnavailable
    entries = git_read(root, "ls-files", "--stage", "-z").split(b"\0")
    findings: list[Finding] = []
    checked = 0
    for entry in filter(None, entries):
        try:
            metadata, raw_path = entry.split(b"\t", 1)
            mode, _oid, stage = metadata.decode("ascii").split()
            path = raw_path.decode("utf-8")
        except (ValueError, UnicodeDecodeError) as error:
            raise IndexUnavailable from error
        shown = path if safe_relative(path) else "<invalid-path>"
        if stage != "0":
            findings.append(Finding(shown, 0, "unmerged-index-entry"))
            continue
        if mode not in ("100644", "100755"):
            findings.append(Finding(shown, 0, "non-regular-index-entry"))
            continue
        rule = path_rule(path)
        if rule or not source_allowed(path):
            findings.append(Finding(shown, 0, rule or "outside-source-allowlist"))
            continue
        # Read the INDEX, even when a safe-looking worktree copy has replaced it.
        # External diff/textconv helpers cannot execute while reading the blob.
        try:
            size = int(git_read(root, "cat-file", "-s", ":" + path).strip())
        except ValueError as error:
            raise IndexUnavailable from error
        if size > MAX_SOURCE_BYTES:
            findings.append(Finding(path, 0, "source-file-too-large"))
            continue
        blob = git_read(root, "show", "--no-ext-diff", "--no-textconv", ":" + path)
        findings.extend(scan_blob(path, blob))
        checked += 1
    if not entries or entries == [b""]:
        findings.append(Finding("<index>", 0, "empty-index"))
    return sorted(set(findings)), checked


def collect_worktree_sources(root: Path) -> tuple[dict[str, bytes], list[Finding]]:
    """Return checked source bytes and findings for preview/ZIP packaging.

    Callers MUST refuse publication if findings is nonempty. Archive these exact
    bytes rather than reopening files after the check (avoids a check/read race).
    Private/generated trees are pruned before traversal; links are never followed.
    """
    root = root.resolve()
    sources: dict[str, bytes] = {}
    findings: list[Finding] = []
    checked = 0
    def walk_error(_error: OSError) -> None:
        findings.append(Finding("<worktree>", 0, "source-tree-unreadable"))
    for directory, dirs, files in os.walk(root, followlinks=False, onerror=walk_error):
        base = Path(directory)
        for name in dirs[:]:
            path = base / name
            relative = path.relative_to(root).as_posix()
            if not source_directory(relative):
                dirs.remove(name)
            elif path.is_symlink() or (hasattr(path, "is_junction") and path.is_junction()):
                dirs.remove(name)
                findings.append(Finding(relative, 0, "source-link-not-allowed"))
        for name in files:
            path = base / name
            relative = path.relative_to(root).as_posix()
            if not source_allowed(relative):
                continue
            if path.is_symlink():
                findings.append(Finding(relative, 0, "source-link-not-allowed"))
                continue
            try:
                with path.open("rb") as source:
                    data = source.read(MAX_SOURCE_BYTES + 1)
            except OSError:
                findings.append(Finding(relative, 0, "source-file-unreadable"))
                continue
            file_findings = scan_blob(relative, data)
            findings.extend(file_findings)
            if not file_findings:
                sources[relative] = data
            checked += 1
    if not checked:
        findings.append(Finding("<worktree>", 0, "no-source-files"))
    return dict(sorted(sources.items())), sorted(set(findings))


def scan_worktree(root: Path) -> tuple[list[Finding], int]:
    sources, findings = collect_worktree_sources(root)
    return findings, len(sources)


def emit_findings(findings: Iterable[Finding]) -> None:
    for finding in findings:
        print(json.dumps(asdict(finding), ensure_ascii=True))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--worktree", action="store_true", help="Preview explicit source allowlist, never substitute for staged index review")
    args = parser.parse_args(argv)
    root = args.root.resolve()
    if not root.is_dir():
        emit_findings([Finding("<root>", 0, "source-root-unavailable")])
        return 2
    try:
        findings, checked = scan_worktree(root) if args.worktree else scan_index(root)
    except IndexUnavailable:
        emit_findings([Finding("<index>", 0, "git-index-unavailable")])
        return 2
    emit_findings(findings)
    if findings:
        return 1
    mode = "worktree-source-preview" if args.worktree else "git-index"
    print(json.dumps({"file": f"<{mode}>", "line": 0, "rule": f"pass-{checked}-files-limited-check-not-complete-secret-scan"}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
