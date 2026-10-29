"""One-command local acceptance. Python 3.11+, Docker, PowerShell 7 / Bash.

No target URL or existing project argument is accepted. Each invocation freezes
reviewed public source, creates a fresh refund-deploy-test-* stack, and keeps
its private config/volumes/evidence. No Git changes, push, prune or online writes.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import html
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess
import sys
import time

from public_release_check import collect_worktree_sources

ROOT = Path(__file__).resolve().parents[1]
OVERRIDES = set("REGISTRY IMAGE_NAMESPACE IMAGE_TAG DEPLOY_NAMESPACE BIND_IP WEB_PORT APP_ORIGIN "
                "MYSQL_ROOT_PASSWORD MYSQL_PASSWORD ENABLE_DATA_RESET SESSION_COOKIE_SECURE MYSQL_MEMORY_LIMIT "
                "REDIS_MEMORY_LIMIT KAFKA_MEMORY_LIMIT BACKEND_MEMORY_LIMIT WEB_MEMORY_LIMIT BUILD_CONTEXT "
                "REFUND_BUILD_ROOT".split())
UI_REQUIRED = (
    "shows an independent login page without loading any business data when no session exists",
    "does not expose business UI while session restoration is pending, then renders real empty components",
    "does not render a late old-session order after a cross-tab identity change",
    "renders API failures in the real ERP without crashing AntD empty state",
    "clears old identity data immediately and ignores late list, detail and activity responses",
    "ignores a late mutation response after identity switch",
    "does not submit duplicate in-flight actions",
    "preserves the original idempotency key AND reason after a network failure",
    "retains UNKNOWN on a query network failure instead of declaring failure",
    "formats integer cents 19900 as ¥199.00",
    "renders PROCESSING with a human-readable status",
    "renders UNKNOWN with a human-readable status",
)
CHECKLIST = (
    ("正常退款", ("SQL confirms one full-amount refund and one channel effect",), "真实 HTTP + MySQL/渠道；页面呈现另由组件测试验证"),
    ("重复提交与异参冲突", ("same key replay returns the original current successful refund",
        "same key with a changed reason conflicts", "same key with a changed order conflicts",
        "replay and changed parameters have no extra financial effects"), "固定键重放、改原因/订单 409、无额外渠道受理"),
    ("并发与换键提交", ("sixteen concurrent distinct keys produce one creation and fifteen explained conflicts",
        "distinct-key concurrency leaves one SQL refund and one channel acceptance"), "16 个不同键并发争抢尚未退款的订单"),
    ("结果未知与原单查证", ("transport uncertainty is never treated as final failure",
        "query preserves the original refund and provider identities", "reconcile only queried once and unpaid order never reached channel"), "UNKNOWN → 原单查询 → SUCCEEDED；未再次提交"),
    ("进程重启后恢复", ("UNKNOWN and original identities survive a real backend restart before any query",
        "query preserves the original refund and provider identities"), "UNKNOWN 时真实重启本次隔离后端及进程内模拟器"),
    ("明确失败与非法状态", ("only explicit rejection produces a failed terminal state with a readable reason",
        "failed refund cannot be resubmitted with a new key", "unpaid order is ineligible",
        "reconcile only queried once and unpaid order never reached channel"), "FAILED 原因、失败后禁重提、未支付拒绝且无渠道记录"),
    ("权限与商户隔离", ("anonymous business access is denied", "read-only role cannot create refunds",
        "viewer cannot reconcile", "cross-merchant object remains indistinguishable from missing",
        "cross-merchant refund access is rejected", "A writer and viewer cannot access B refund objects"), "真实 Cookie API 401/403/404，不以隐藏按钮代替授权"),
    ("前端交互与切换身份", (), "Vitest + jsdom/AntD 组件和异步竞态测试；不是公网真实浏览器 E2E"),
    ("干净环境可启动", ("isolated project contains exactly five containers",
        "database has exactly the five original orders and no active financial records",
        "only web is published and its mapping exactly matches the requested origin"), "源码/依赖锁定构建 + 新项目空卷；允许复用 Docker 下载/构建缓存"),
)


class Failure(RuntimeError):
    """Only fixed diagnostic codes, never arbitrary SQL/HTTP output."""


def clean_env(extra=None):
    env = {k: v for k, v in os.environ.items() if k not in OVERRIDES and not k.startswith(("COMPOSE_", "TEST_"))}
    env.update({"PYTHONUTF8": "1", "PYTHONIOENCODING": "utf-8"})
    env.update(extra or {})
    return env


def assert_plain_path(path: Path):
    for item in (path, *path.parents):
        if item.is_symlink() or (hasattr(item, "is_junction") and item.is_junction()):
            raise Failure("linked-runtime-or-source-path")


def parse_go(data: str):
    rows = [json.loads(line) for line in data.splitlines() if line.strip()]
    endings = [r for r in rows if r.get("Test") and r.get("Action") in {"pass", "fail", "skip"}]
    packages = {r["Package"]: r["Action"] for r in rows if not r.get("Test") and r.get("Action") in {"pass", "fail", "skip"}}
    names = [r["Test"] for r in endings if r["Action"] == "pass"]
    if (not endings or not packages or any(r["Action"] != "pass" for r in endings)
            or any(a != "pass" for a in packages.values())
            or "TestKafkaRedisRealDuplicateDelivery" not in names):
        raise Failure("go-tests-empty-failed-or-skipped")
    return {"passed_including_subtests": len(endings), "skipped": 0, "packages": len(packages),
            "race_detector": False, "names": names}


def parse_frontend(data: str):
    report = json.loads(data)
    assertions = [a for file in report.get("testResults", []) for a in file.get("assertionResults", [])]
    names = [a.get("title", "") for a in assertions if a.get("status") == "passed"]
    if (report.get("success") is not True or not assertions
            or any(a.get("status") != "passed" for a in assertions)
            or any(name not in names for name in UI_REQUIRED)
            or not any("loads orders on the first real login without a page refresh" in name for name in names)):
        raise Failure("frontend-tests-empty-failed-skipped-or-coverage-missing")
    return {"passed": len(assertions), "files": len(report["testResults"]), "skipped": 0,
            "environment": "Vitest/jsdom; API mocked; not live browser E2E", "names": names}


def checklist(report):
    deployment = report.get("deployment", {})
    records = deployment.get("checks", [])
    values = {}
    for row in records:
        values.setdefault(row["name"], []).append(row["passed"])
    result = []
    for number, (name, required, scope) in enumerate(CHECKLIST, 1):
        if number == 8:
            state = "PASS" if report.get("frontend") else "NOT_RUN"
            if any(r["status"] == "FAIL" and "前端组件" in r["name"] for r in report.get("steps", [])):
                state = "FAIL"
        elif any(key in values and not all(values[key]) for key in required):
            state = "FAIL"
        elif all(key in values and all(values[key]) for key in required):
            state = "PASS"
        else:
            state = "NOT_RUN"
        if number == 9 and not report.get("go"):
            state = "NOT_RUN" if state == "PASS" else state
        if number == 1 and state == "PASS" and not report.get("frontend"):
            state = "NOT_RUN"
        result.append({"number": number, "name": name, "status": state, "scope": scope})
    return result


class Runner:
    def __init__(self, keep_running=False):
        self.run_id = datetime.now().strftime("%Y%m%d-%H%M%S-") + secrets.token_hex(4)
        self.project = "refund-deploy-test-auto-" + self.run_id
        self.evidence = ROOT / "evidence" / ("checklist-" + self.run_id)
        self.source = ROOT / ".runtime" / ("checklist-" + self.run_id) / "source"
        self.env_file = self.source / ".runtime" / ("start-" + self.project + ".env")
        self.keep_running = keep_running
        self.report = {"result": "RUNNING", "run_id": self.run_id, "project": self.project,
                       "started_at": datetime.now(timezone.utc).isoformat(), "steps": [],
                       "limitations": ["前端是 jsdom/模拟 API 组件测试，不是公网真实浏览器端到端操作。",
                           "测试只处理本轮虚构数据；没有访问线上服务，没有提交 Git 或推送镜像。",
                           "不运行 Go race detector/依赖漏洞审计；不替代 CI 或完整安全审计。",
                           "本入口不运行共享 refund_acceptance 的旧三组进程脚本；使用独立容器 HTTP 验收。",
                           "工作区源码预检不是 Git index 发布检查；缓存复用不等于全新安装操作系统。"]}
        self.sequence = 0
        self.suite = None

    def redact(self, output):
        if self.env_file.exists():
            for line in self.env_file.read_text(encoding="utf-8-sig").splitlines():
                name, _, value = line.partition("=")
                if name in {"MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD"} and value:
                    output = output.replace(value.strip(), "<redacted>")
        return output

    def command(self, args, *, cwd=None, env=None, stdin=None, timeout=120, allow_failure=False):
        self.sequence += 1
        logfile = self.evidence / f"command-{self.sequence:03}.log"
        try:
            p = subprocess.run(args, cwd=cwd or ROOT, env=clean_env(env), input=stdin, text=True,
                               encoding="utf-8", errors="replace", stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                               timeout=timeout)
        except (OSError, subprocess.TimeoutExpired) as exc:
            output = getattr(exc, "stdout", "") or ""
            if isinstance(output, bytes):
                output = output.decode("utf-8", errors="replace")
            logfile.write_text(self.redact(output), encoding="utf-8")
            raise Failure("command-unavailable-or-timeout") from exc
        logfile.write_text(self.redact(p.stdout), encoding="utf-8")
        if p.returncode and not allow_failure:
            raise Failure("command-failed-see-local-log")
        return p.stdout, p.returncode

    def step(self, name, action):
        print(name, flush=True)
        row = {"name": name, "status": "RUNNING"}
        self.report["steps"].append(row)
        start = time.monotonic()
        try:
            result = action()
            row["status"] = "PASS"
            return result
        except (Exception, KeyboardInterrupt):
            row["status"] = "FAIL"
            raise
        finally:
            row["seconds"] = round(time.monotonic() - start, 2)

    def preflight(self):
        if sys.version_info < (3, 11):
            raise Failure("python-three-eleven-or-newer-required")
        self.shell = shutil.which("pwsh" if os.name == "nt" else "bash")
        if not self.shell or not shutil.which("docker") or not shutil.which("git"):
            raise Failure("requires-docker-git-and-powershell-seven-or-bash")
        context = self.command(["docker", "context", "show"])[0].strip()
        endpoint = self.command(["docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}", context])[0].strip()
        override = os.environ.get("DOCKER_HOST", "")
        if not endpoint.startswith(("npipe://", "unix://")) or (override and not override.startswith(("npipe://", "unix://"))):
            raise Failure("remote-docker-is-not-allowed")
        version = self.command(["docker", "info", "--format", "{{.ServerVersion}}"])[0].strip()
        self.command(["docker", "compose", "version"])
        self.report["docker_version"] = version
        # Even the astronomically unlikely name collision must fail closed.
        for args in (["ps", "-a"], ["volume", "ls"], ["network", "ls"]):
            output, _ = self.command(["docker", *args, "--quiet", "--filter", "label=com.docker.compose.project=" + self.project])
            if output.strip():
                raise Failure("test-project-already-exists")
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.base_url = f"http://127.0.0.1:{self.port}"
        self.report["local_url"] = self.base_url

    def freeze(self):
        sources, findings = collect_worktree_sources(ROOT)
        if findings:
            self.report["source_findings"] = [vars(f) for f in findings]
            raise Failure("public-source-precheck-failed")
        self.source.mkdir(parents=True, exist_ok=False)
        manifest = []
        for relative, data in sorted(sources.items()):
            path = self.source / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            manifest.append({"path": relative, "sha256": hashlib.sha256(data).hexdigest()})
        raw = json.dumps(manifest, ensure_ascii=False, sort_keys=True).encode("utf-8")
        self.report["source"] = {"files": len(sources), "manifest_sha256": hashlib.sha256(raw).hexdigest(),
                                 "scope": "checked worktree bytes, including uncommitted changes; not Git index"}
        (self.evidence / "source-manifest.json").write_bytes(raw)
        output, _ = self.command([sys.executable, "-m", "unittest", "discover", "-v", "-s", "scripts", "-p", "test_*.py"],
                                 cwd=self.source, timeout=120)
        total = re.search(r"Ran ([0-9]+) tests? in", output)
        skipped = [line for line in output.splitlines() if " ... skipped " in line]
        if not total or int(total[1]) == 0 or any("test_source_symlink_is_rejected_without_following" not in line
                or "symlinks unavailable on this platform" not in line for line in skipped):
            raise Failure("script-tests-empty-or-unexpected-skip")
        self.report["script_tests"] = {"passed": int(total[1]) - len(skipped), "skipped": len(skipped),
                                       "skip_details": skipped}

    def start(self):
        script = self.source / "scripts" / ("start.ps1" if os.name == "nt" else "start.sh")
        args = ([self.shell, "-NoProfile", "-File", str(script), "-Project", self.project, "-Port", str(self.port)]
                if os.name == "nt" else [self.shell, str(script), "--project", self.project, "--port", str(self.port)])
        self.command(args, cwd=self.source, timeout=1800)
        self.context = self.source
        info = self.source / ".runtime" / ("build-" + self.project + (".json" if os.name == "nt" else ".path"))
        if info.exists():
            value = info.read_text(encoding="utf-8-sig").strip()
            self.context = Path(json.loads(value)["build_context"] if os.name == "nt" else value)
        assert_plain_path(self.context)

    def deployment(self):
        # Import the frozen suite in its own process so ROOT and its saved config
        # cannot accidentally refer to an existing checkout/deployment.
        try:
            self.command([sys.executable, str(self.source / "scripts/deployment_acceptance.py"),
                          "--project", self.project, "--env-file", str(self.env_file), "--base-url", self.base_url],
                         cwd=self.source, timeout=600)
        finally:
            result = self.source / "evidence/deployment-validation.json"
            if result.exists():
                self.report["deployment"] = json.loads(result.read_text(encoding="utf-8"))
                shutil.copyfile(result, self.evidence / "deployment.json")
        if self.report.get("deployment", {}).get("result") != "PASS":
            raise Failure("deployment-report-not-passed")

    def build_test_image(self, kind):
        image = self.project + "-" + kind + ":local"
        self.command(["docker", "build", "--target", "build", "--tag", image, "--file",
                      str(self.context / ("Dockerfile.web" if kind == "frontend" else "Dockerfile.backend")),
                      str(self.context)], timeout=1800)
        # Pin the execution to the resulting immutable image ID, not the tag.
        return self.command(["docker", "image", "inspect", "--format", "{{.Id}}", image])[0].strip()

    def owned_container(self, cid):
        label = self.command(["docker", "inspect", "--format", '{{index .Config.Labels "io.refund.checklist"}}', cid])[0].strip()
        if label != self.project:
            raise Failure("test-container-owner-mismatch")

    def test_container(self, kind, image, command, report_path, *, network="none", environment=None):
        args = ["docker", "create", "--name", self.project + "-" + kind, "--label", "io.refund.checklist=" + self.project,
                "--network", network, "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "2g"]
        for name in environment or {}:
            args += ["--env", name]  # value inherited, never included in argv
        cid = self.command([*args, image, "sh", "-ec", command], env=environment)[0].strip()
        if not re.fullmatch(r"[a-f0-9]{64}", cid):
            raise Failure("unexpected-test-container-id")
        passed = False
        try:
            _, code = self.command(["docker", "start", "--attach", cid], timeout=900, allow_failure=True)
            self.command(["docker", "cp", cid + ":" + report_path, str(self.evidence / (kind + ".json"))])
            status = self.command(["docker", "inspect", "--format", "{{.State.ExitCode}}", cid])[0].strip()
            if code or status != "0":
                raise Failure("test-container-failed")
            passed = True
            return self.redact((self.evidence / (kind + ".json")).read_text(encoding="utf-8"))
        finally:
            self.owned_container(cid)
            self.command(["docker", "stop", "--time", "10", cid], timeout=30)
            # Failed containers and all business volumes are retained for diagnosis.
            if passed:
                self.command(["docker", "rm", cid])
            raw = self.evidence / (kind + ".json")
            if raw.exists():
                raw.write_text(self.redact(raw.read_text(encoding="utf-8")), encoding="utf-8")

    def frontend(self):
        image = self.build_test_image("frontend")
        data = self.test_container("frontend", image, "node --version; ./node_modules/.bin/vitest run --reporter=json --outputFile=/tmp/frontend.json", "/tmp/frontend.json")
        self.report["frontend"] = parse_frontend(data)

    def backend(self):
        image = self.build_test_image("go")
        ids = self.command(["docker", "ps", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + self.project,
                            "--filter", "label=com.docker.compose.service=mysql"])[0].split()
        if len(ids) != 1 or not re.fullmatch(r"[a-f0-9]{64}", ids[0]):
            raise Failure("isolated-mysql-container-missing")
        # This is a freshly owned deployment already verified by the HTTP suite.
        # Deliberately no IF NOT EXISTS: a collision must never reuse old data.
        self.command(["docker", "exec", "-i", ids[0], "sh", "-ec",
                      'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch'],
                     stdin="CREATE DATABASE refund_test CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;\n"
                           "GRANT ALL ON refund_test.* TO 'refundapp'@'%';\n")
        config = dict(line.split("=", 1) for line in self.env_file.read_text(encoding="utf-8-sig").splitlines()
                      if line and not line.startswith("#") and "=" in line)
        password = config["MYSQL_PASSWORD"]
        if not re.fullmatch(r"[a-f0-9]{64}", password):
            raise Failure("invalid-isolated-database-credential-format")
        environment = {"TEST_MYSQL_DSN": f"refundapp:{password}@tcp(127.0.0.1:3306)/refund_test?parseTime=true&loc=UTC",
                       "TEST_REDIS_ADDR": "redis:6379", "TEST_KAFKA_BROKERS": "kafka:9092",
                       "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off"}
        data = self.test_container("go", image, "go version; go test -json -count=1 -timeout=10m ./... > /tmp/go.json; go vet ./...; go build ./...",
                                   "/tmp/go.json", network="container:" + ids[0], environment=environment)
        self.report["go"] = parse_go(data)

    def stop_own_stack(self):
        # Stops only exact IDs that still carry this generated project label.
        # No volume removal, unrelated containers, orphan removal or global prune.
        ids = self.command(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + self.project])[0].split()
        for cid in ids:
            label = self.command(["docker", "inspect", "--format", '{{index .Config.Labels "com.docker.compose.project"}}', cid])[0].strip()
            if label != self.project:
                raise Failure("compose-container-owner-mismatch")
        if ids:
            self.command(["docker", "stop", "--time", "20", *ids], timeout=150)
        self.report["environment_state"] = "stopped; dedicated volumes and private configuration retained"

    def write_report(self):
        self.report["finished_at"] = datetime.now(timezone.utc).isoformat()
        self.report["checklist"] = checklist(self.report)
        (self.evidence / "report.json").write_text(json.dumps(self.report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        lines = ["# 公开验收自动测试报告", "", "结果：**" + self.report["result"] + "**", "",
                 "本报告只对本轮源码快照和所列验证层负责；不是公网验收或面试官签收。", "",
                 "|项|名称|结果|验证范围|", "|---|---|---|---|"]
        for row in self.report["checklist"]:
            lines.append(f'|{row["number"]}|{row["name"]}|{row["status"]}|{row["scope"]}|')
        counts = ["- 真实 HTTP/SQL 断言通过：" + str(sum(c["passed"] for c in self.report.get("deployment", {}).get("checks", []))),
                  "- Go 通过（含子测试）：" + str(self.report.get("go", {}).get("passed_including_subtests", "未完成")),
                  "- 前端组件测试通过：" + str(self.report.get("frontend", {}).get("passed", "未完成")),
                  "- Python 守卫通过：" + str(self.report.get("script_tests", {}).get("passed", "未完成"))
                  + "；SKIP（不计通过）：" + str(self.report.get("script_tests", {}).get("skipped", "未完成"))]
        lines += ["", "## 执行阶段", *[f'- {r["status"]} {r["name"]} ({r["seconds"]}s)' for r in self.report["steps"]],
                  "", "## 实际计数", *counts,
                  "", "## 已知边界", *["- " + line for line in self.report["limitations"]], "",
                  "详细断言见同目录 deployment.json；测试计数和源码快照摘要见 report.json。",
                  "配置及数据卷保留在本轮私有目录/项目；重复运行使用新项目，绝不清空旧项目。"]
        if self.report.get("script_tests", {}).get("skipped"):
            lines += ["", "## 未执行的辅助测试（不计通过）", *["- " + line for line in self.report["script_tests"]["skip_details"]]]
        markdown = "\n".join(lines) + "\n"
        (self.evidence / "report.md").write_text(markdown, encoding="utf-8")
        rows = "".join("<tr>" + "".join("<td>" + html.escape(str(row[k])) + "</td>" for k in ("number", "name", "status", "scope")) + "</tr>"
                       for row in self.report["checklist"])
        page = ('<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width">'
                '<title>退款工作台自动验收报告</title><style>body{font:16px/1.7 system-ui;max-width:1100px;margin:40px auto;padding:20px;color:#183044}'
                'table{border-collapse:collapse;width:100%}td,th{border:1px solid #ccd7df;padding:10px;text-align:left}h1{font-size:26px}</style>'
                '<h1>公开验收自动测试：' + html.escape(self.report["result"]) + '</h1><p>本轮本地隔离验证，不代表公网浏览器验收或人工签收。</p>'
                '<table><thead><tr><th>项</th><th>名称</th><th>结果</th><th>验证范围</th></tr></thead><tbody>' + rows + '</tbody></table>'
                '<h2>执行与边界</h2><pre style="white-space:pre-wrap">' + html.escape(markdown.split("## 执行阶段", 1)[1]) + '</pre></html>')
        (self.evidence / "report.html").write_text(page, encoding="utf-8")

    def run(self):
        for path in (ROOT, self.source, self.evidence):
            assert_plain_path(path)
        self.evidence.mkdir(parents=True, exist_ok=False)
        try:
            self.step("[1/6] 检查本地 Docker 与隔离资源", self.preflight)
            self.step("[2/6] 公开源码预检并冻结本轮源码", self.freeze)
            self.step("[3/6] 从源码构建并启动全新五服务", self.start)
            self.step("[4/6] HTTP / SQL / 并发 / 重启 / 权限 / 恢复 / Kafka→Redis", self.deployment)
            self.step("[5/6] 前端组件与异步竞态自动测试", self.frontend)
            self.step("[6/6] 真实 MySQL/Redis/Kafka Go 集成、vet、build", self.backend)
            if any(row["status"] != "PASS" for row in checklist(self.report)):
                raise Failure("required-checklist-evidence-incomplete")
            if not self.keep_running:
                self.step("仅停止本轮测试容器，保留证据和数据卷", self.stop_own_stack)
            else:
                self.report["environment_state"] = "kept running by explicit option"
            self.report["result"] = "PASS"
        except (Exception, KeyboardInterrupt) as exc:
            self.report["result"] = "FAIL"
            self.report["error_type"] = type(exc).__name__
            if isinstance(exc, Failure):
                self.report["error_code"] = str(exc)
            self.report["environment_state"] = "failure retained; no business volumes removed"
        finally:
            self.write_report()
        print("\n" + self.report["result"] + " — " + str(self.evidence / "report.html"), flush=True)
        if self.report["result"] != "PASS":
            print("失败现场已保留；查看 report.json 的阶段与 command-*.log，不要删除旧卷重试。", flush=True)
        return 0 if self.report["result"] == "PASS" else 1


def main():
    # PowerShell 7, CI and captured logs use UTF-8; do not emit a legacy Windows
    # codepage into an otherwise UTF-8 command transcript.
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, "reconfigure"):
            stream.reconfigure(encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--keep-running", action="store_true", help="成功后保持本轮测试站点运行（默认仅停止自己的容器，保留数据）")
    args = parser.parse_args()
    return Runner(args.keep_running).run()


if __name__ == "__main__":
    raise SystemExit(main())
