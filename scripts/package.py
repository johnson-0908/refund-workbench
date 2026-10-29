"""Package exactly the public source bytes validated by the release gate."""
from pathlib import Path
from hashlib import sha256
from zipfile import ZipFile, ZipInfo, ZIP_DEFLATED
import sys

from public_release_check import collect_worktree_sources, emit_findings

ROOT = Path(__file__).resolve().parent.parent
TARGET = ROOT / "refund-workbench-source.zip"


def main():
    sources, findings = collect_worktree_sources(ROOT)
    if findings:
        emit_findings(findings)
        return 1
    if not sources:
        raise RuntimeError("No public source files found")
    # Deterministic metadata; publish checked bytes, not a second worktree read.
    with ZipFile(TARGET, "w", compression=ZIP_DEFLATED, compresslevel=9) as archive:
        for relative, data in sorted(sources.items()):
            entry = ZipInfo("refund-workbench/" + relative, (2026, 1, 1, 0, 0, 0))
            entry.compress_type = ZIP_DEFLATED
            entry.external_attr = (0o100755 if relative.endswith((".sh", ".command")) else 0o100644) << 16
            archive.writestr(entry, data)
    with ZipFile(TARGET) as archive:
        assert archive.testzip() is None
        assert len(archive.namelist()) == len(sources)
        for relative, data in sources.items():
            assert archive.read("refund-workbench/" + relative) == data
    digest = sha256(TARGET.read_bytes()).hexdigest()
    TARGET.with_suffix(TARGET.suffix + ".sha256").write_text(
        f"{digest}  {TARGET.name}\n", encoding="ascii")
    print(f"{len(sources)} public source files; ZIP verified; SHA256 {digest}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
