"""Real five-container acceptance on an explicitly isolated deployment only.

Requires a running refund-deploy-test-* project and its ignored .runtime env
file. Refuses non-pristine financial fixtures; never seeds, truncates, resets a
whole database, changes Docker configuration, or stops unrelated services.
The only reset is the authenticated current-merchant API under test. Cookies,
CSRF tokens, environment contents, SQL payloads and raw errors are never logged.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from datetime import datetime, timezone
import gzip
import hashlib
import http.client
from http.cookies import SimpleCookie
import ipaddress
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys
import time
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
REPORT_PATH = ROOT / "evidence/deployment-validation.json"
AUTO = object()
CONFIRM = {"confirmation": "恢复初始数据"}
SERVICES = {"mysql", "redis", "kafka", "backend", "web"}
FIXTURES = [
    ("A-1001", "A", 19900, "PAID"), ("A-1002", "A", 8800, "PAID"),
    ("A-1003", "A", 5600, "PAID"), ("A-1004", "A", 12900, "UNPAID"),
    ("B-2001", "B", 25600, "PAID"),
]


class SafeFailure(RuntimeError):
    """Only fixed, non-sensitive error codes are allowed in this exception."""


@dataclass(repr=False)
class Session:
    cookie: str
    csrf: str
    user: dict


@dataclass(repr=False)
class Response:
    status: int
    headers: dict[str, str]
    raw: bytes

    def json(self):
        try:
            return json.loads(self.raw)
        except (ValueError, UnicodeDecodeError) as error:
            raise SafeFailure("invalid-json-response") from error


class Acceptance:
    def __init__(self, project: str, env_file: str, base_url: str):
        self.report = {"started_at": datetime.now(timezone.utc).isoformat(), "result": "RUNNING", "checks": [],
                       "scope": "Isolated local deployment, fictional refunds; bounded read baseline is not a production SLA"}
        self.project = project
        self.env_file = Path(env_file)
        if not self.env_file.is_absolute():
            self.env_file = ROOT / self.env_file
        self.base_url = base_url
        self.config: dict[str, str] = {}
        self.containers: dict[str, str] = {}
        self.guard_complete = False

    def check(self, name: str, condition: bool, details=None):
        record = {"name": name, "passed": bool(condition)}
        if details is not None:
            record["details"] = details
        self.report["checks"].append(record)
        print(("PASS " if condition else "FAIL ") + name, flush=True)
        if not condition:
            raise SafeFailure("acceptance-check-failed")

    def command(self, args: list[str], *, stdin: str | None = None, timeout=90) -> str:
        # Do not inherit Compose overrides that could redirect the saved project.
        env = {key: value for key, value in os.environ.items()
               if key not in self.config and not key.startswith("COMPOSE_")}
        try:
            result = subprocess.run(args, input=stdin, text=True, encoding="utf-8", errors="replace",
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=ROOT, env=env, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise SafeFailure("scoped-command-unavailable-or-timeout") from error
        if result.returncode:
            # Docker/MySQL errors may embed configuration, so never echo them.
            raise SafeFailure("scoped-command-failed")
        return result.stdout

    def compose(self, *args: str, stdin=None, timeout=90) -> str:
        if hashlib.sha256(self.env_file.read_bytes()).hexdigest() != self.env_hash:
            raise SafeFailure("runtime-config-changed")
        return self.command(["docker", "compose", "--env-file", str(self.env_file), "-f", str(ROOT / "compose.deploy.yaml"),
                             "-p", self.project, *args], stdin=stdin, timeout=timeout)

    def sql(self, query: str):
        if not self.guard_complete:
            raise SafeFailure("topology-not-verified")
        # Password expansion happens solely inside the verified MySQL container.
        # SQL is sent on stdin; neither passwords nor queries enter host argv/logs.
        output = self.compose("exec", "-T", "mysql", "sh", "-ec",
                              'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --protocol=TCP -h127.0.0.1 --user="$MYSQL_USER" '
                              '--database="$MYSQL_DATABASE" --batch --raw --skip-column-names',
                              stdin="START TRANSACTION READ ONLY;\n" + query + ";\nCOMMIT;\n")
        try:
            return json.loads(output.strip())
        except ValueError as error:
            raise SafeFailure("unexpected-inspection-result") from error

    def inspect_business(self):
        # A diagnostic must not run admin's startup migration. All reads share
        # one read-only statement/snapshot; array order is canonicalized below.
        fields = {
            "orders": {"order_id": "id", **dict.fromkeys(("merchant_id", "amount_cents", "status"))},
            "refunds": {"refund_id": "id", **dict.fromkeys(("order_id", "merchant_id", "amount_cents",
                "provider_request_id", "reason", "status", "message", "created_at", "updated_at"))},
            "channel_requests": dict.fromkeys(("provider_request_id", "order_id", "amount_cents", "status",
                "mode", "submit_calls", "query_calls", "effect_count", "created_at", "updated_at")),
        }
        counts = ",".join(f"'{table}',(SELECT COUNT(*) FROM {table})" for table in
                          ("orders", "refunds", "idempotency_keys", "channel_requests", "outbox_events"))
        counts += ",'outbox_pending',(SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL)"
        arrays = []
        for table, columns in fields.items():
            obj = ",".join(f"'{key}',{value or key}" for key, value in columns.items())
            arrays.append(f"'{table}',(SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT({obj})),JSON_ARRAY()) FROM {table})")
        result = self.sql("SELECT JSON_OBJECT('database',DATABASE(),'counts',JSON_OBJECT(" + counts + ")," + ",".join(arrays) + ")")
        for table, key in (("orders", "order_id"), ("refunds", "refund_id"), ("channel_requests", "provider_request_id")):
            result[table].sort(key=lambda row: row[key])
        return result

    def validate_target(self):
        self.check("project name is explicitly reserved for deployment acceptance",
                   re.fullmatch(r"refund-deploy-test-[a-z0-9][a-z0-9-]{0,39}", self.project) is not None)
        runtime = ROOT / ".runtime"
        original = self.env_file
        self.env_file = original.resolve(strict=True)
        self.check("explicit env file stays inside the ignored local runtime directory",
                   self.env_file.is_relative_to(runtime.resolve()) and self.env_file.is_file()
                   and not any(path.is_symlink() or (hasattr(path, "is_junction") and path.is_junction())
                               for path in [original, *original.parents] if path != ROOT.parent)
                   and self.env_file.stat().st_size <= 65536
                   and ".runtime/" in (ROOT / ".gitignore").read_text(encoding="utf-8-sig").splitlines())
        if (ROOT / ".git").exists():
            self.command(["git", "check-ignore", "--quiet", "--", self.env_file.relative_to(ROOT).as_posix()])
        raw = self.env_file.read_bytes()
        self.env_hash = hashlib.sha256(raw).hexdigest()
        for line in raw.decode("utf-8-sig").splitlines():
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            key, separator, value = line.partition("=")
            key, value = key.strip(), value.strip()
            if not separator or not re.fullmatch(r"[A-Z][A-Z0-9_]*", key) or key in self.config:
                raise SafeFailure("invalid-runtime-config-format")
            if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
                value = value[1:-1]
            self.config[key] = value
        parsed = urlsplit(self.base_url)
        try:
            valid_origin = (parsed.scheme == "http" and ipaddress.ip_address(parsed.hostname or "").is_loopback
                            and parsed.port is not None and not parsed.username and not parsed.password
                            and not parsed.path and not parsed.query and not parsed.fragment)
        except ValueError:
            valid_origin = False
        self.check("HTTP origin is literal loopback and exactly matches the saved deployment",
                   valid_origin and self.base_url == self.config.get("APP_ORIGIN")
                   and self.config.get("DEPLOY_NAMESPACE") == self.project
                   and self.config.get("BIND_IP") == parsed.hostname
                   and self.config.get("WEB_PORT") == str(parsed.port)
                   and self.config.get("ENABLE_DATA_RESET") == "true")
        self.host, self.port = parsed.hostname, parsed.port
        endpoint_override = os.environ.get("DOCKER_HOST", "")
        context = self.command(["docker", "context", "show"]).strip()
        endpoint = self.command(["docker", "context", "inspect", "--format", '{{.Endpoints.docker.Host}}', context]).strip()
        self.check("Docker endpoint is local, not a remote daemon",
                   endpoint.startswith(("npipe://", "unix://"))
                   and (not endpoint_override or endpoint_override.startswith(("npipe://", "unix://"))))
        ids = self.command(["docker", "ps", "-a", "--filter", "label=com.docker.compose.project=" + self.project,
                            "--format", "{{.ID}}"]).split()
        self.check("isolated project contains exactly five containers", len(ids) == 5)
        published = []
        for cid in ids:
            # Select non-secret fields; never fetch or log Config.Env.
            template = "\n".join("{{json " + field + "}}" for field in (
                ".Config.Labels", ".Config.User", ".HostConfig.ReadonlyRootfs", ".HostConfig.CapDrop",
                ".HostConfig.SecurityOpt", ".NetworkSettings.Ports", ".Mounts", ".State.Running", ".NetworkSettings.Networks"))
            labels, user, readonly, caps, security, ports, mounts, running, networks = [json.loads(line) for line in
                self.command(["docker", "inspect", "--format", template, cid]).splitlines()]
            service = labels.get("com.docker.compose.service")
            self.check("container belongs to a unique expected running service",
                       labels.get("com.docker.compose.project") == self.project and service in SERVICES
                       and service not in self.containers and running)
            self.containers[service] = cid
            if service in {"backend", "web"}:
                self.check(service + " uses a non-root, read-only, capability-dropped runtime",
                           bool(user) and user.split(":")[0] not in {"0", "root"} and readonly
                           and "ALL" in (caps or []) and "no-new-privileges:true" in (security or []))
            for container_port, bindings in (ports or {}).items():
                for binding in bindings or []:
                    published.append((service, container_port, binding.get("HostIp"), binding.get("HostPort")))
            for mount in mounts:
                if mount["Type"] == "tmpfs":
                    continue
                # The official Kafka image declares these two anonymous config
                # volumes. They carry no Compose labels; prove exclusive current
                # container ownership instead. Business data still requires the
                # named, project-labelled kafka-data/mysql-data/redis-data volume.
                if (service == "kafka" and mount["Type"] == "volume"
                        and mount.get("Destination") in {"/etc/kafka/secrets", "/mnt/shared/config"}
                        and re.fullmatch(r"[a-f0-9]{64}", mount.get("Name", ""))):
                    owners = self.command(["docker", "ps", "-a", "--filter", "volume=" + mount["Name"], "--format", "{{.ID}}"]).split()
                    self.check("Kafka image-declared anonymous config volume is exclusive to this container", owners == [cid])
                    continue
                self.check("persistent storage is an owned isolated named volume", mount["Type"] == "volume"
                           and mount.get("Name", "").startswith(self.project + "_"))
                volume_labels = json.loads(self.command(["docker", "volume", "inspect", "--format", "{{json .Labels}}", mount["Name"]]))
                self.check("volume project label matches this test", volume_labels.get("com.docker.compose.project") == self.project)
            for network in networks:
                network_labels = json.loads(self.command(["docker", "network", "inspect", "--format", "{{json .Labels}}", network]))
                self.check("container networks are owned by this isolated project", network.startswith(self.project + "_")
                           and network_labels.get("com.docker.compose.project") == self.project)
        self.check("only web is published and its mapping exactly matches the requested origin",
                   set(self.containers) == SERVICES and published == [("web", "8080/tcp", self.host, str(self.port))])
        backend_config = self.compose("exec", "-T", "backend", "sh", "-ec",
                                      'test -z "${MYSQL_DSN+x}"; printf "%s\\n" "$MYSQL_HOST" "$MYSQL_PORT" "$MYSQL_DATABASE" '
                                      '"$ENABLE_DEMO_BEARER" "$ENABLE_DATA_RESET" "$ALLOW_DOCKER_DEMO_RESET" "$AUTH_ALLOWED_ORIGINS"').splitlines()
        self.check("backend is bound to the verified private SQL fixture and exact origin",
                   backend_config == ["mysql", "3306", "refund_demo", "false", "true", "true", self.base_url])
        self.guard_complete = True
        initial = self.inspect_business()
        order_signature = sorted((item["order_id"], item["merchant_id"], int(item["amount_cents"]), item["status"])
                                 for item in initial["orders"])
        self.check("database has exactly the five original orders and no active financial records",
                   initial["database"] == "refund_demo" and order_signature == FIXTURES
                   and initial["counts"]["orders"] == 5
                   and all(initial["counts"][key] == 0 for key in ("refunds", "channel_requests", "idempotency_keys", "outbox_events")))
        baseline = self.sql("SELECT JSON_OBJECT('database',DATABASE(),'archives',(SELECT COUNT(*) FROM maintenance_resets),"
                            "'version_a',COALESCE((SELECT data_version FROM merchant_reset_watermarks WHERE merchant_id='A'),0),"
                            "'version_b',COALESCE((SELECT data_version FROM merchant_reset_watermarks WHERE merchant_id='B'),0),"
                            "'scenarios',(SELECT COUNT(*) FROM channel_scenarios WHERE (order_id='A-1001' AND mode='SUCCESS') "
                            "OR (order_id='A-1002' AND mode='LOST_RESPONSE') OR (order_id='A-1003' AND mode='REJECT') "
                            "OR (order_id='A-1004' AND mode='SUCCESS') OR (order_id='B-2001' AND mode='SUCCESS')))")
        self.check("SQL fixture scenario guard passes without resetting any data",
                   baseline["database"] == "refund_demo" and baseline["scenarios"] == 5)
        self.baseline = baseline
        self.report["initial_counts"] = initial["counts"]
        self.report["baseline"] = {key: baseline[key] for key in ("archives", "version_a", "version_b")}

    def request(self, method: str, path: str, body=None, *, session=None, origin=AUTO, csrf=AUTO, key=None, headers=None):
        if not self.guard_complete:
            raise SafeFailure("target-not-verified")
        request_headers = {"Accept": "application/json", **(headers or {})}
        if session is not None:
            request_headers["Cookie"] = "erp_session=" + session.cookie
        if origin is AUTO:
            origin = self.base_url if method == "POST" else None
        if csrf is AUTO:
            csrf = session.csrf if session else None
        if origin is not None:
            request_headers["Origin"] = origin
        if csrf is not None:
            request_headers["X-CSRF-Token"] = csrf
        if key is not None:
            request_headers["Idempotency-Key"] = key
        encoded = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
        if encoded is not None:
            request_headers["Content-Type"] = "application/json"
        connection = http.client.HTTPConnection(self.host, self.port, timeout=25)
        try:
            connection.request(method, path, body=encoded, headers=request_headers)
            response = connection.getresponse()
            raw = response.read(4 * 1024 * 1024 + 1)
            if len(raw) > 4 * 1024 * 1024:
                raise SafeFailure("response-size-exceeded")
            return Response(response.status, {name.lower(): value for name, value in response.getheaders()}, raw)
        finally:
            connection.close()

    def expect(self, name, response, status, code=None):
        matches = response.status == status
        if code:
            matches = matches and response.json().get("error", {}).get("code") == code
        self.check(name, matches, {"status": response.status})

    def login(self, identity):
        response = self.request("POST", "/api/auth/login", {"identity_id": identity})
        self.expect("login returns authoritative session for " + identity, response, 200)
        cookies = SimpleCookie()
        cookies.load(response.headers.get("set-cookie", ""))
        cookie = cookies.get("erp_session")
        data = response.json()
        role = "viewer" if identity == "a-read" else "operator"
        permissions = {"orders:read", "refunds:read"}
        if role == "operator":
            permissions.update({"refunds:create", "refunds:reconcile", "data:reset"})
        self.check("login cookie and session policy are secure and non-cacheable",
                   cookie is not None and bool(cookie["httponly"]) and cookie["samesite"].lower() == "strict"
                   and cookie["path"] == "/" and not cookie["domain"] and not cookie["secure"]
                   and cookie["max-age"] == "28800" and response.headers.get("cache-control") == "no-store"
                   and isinstance(data.get("csrf_token"), str) and len(data["csrf_token"]) >= 32)
        self.check("identity role, tenant and permissions are server-owned",
                   data["user"].get("identity_id") == identity and data["user"].get("role") == role
                   and data["user"].get("merchant_id") == ("B" if identity == "b-write" else "A")
                   and set(data["user"].get("permissions", [])) == permissions)
        return Session(cookie.value, data["csrf_token"], data["user"])

    def verify_edge(self):
        for path in ("/healthz", "/", "/finance/orders", "/api/orders"):
            response = self.request("GET", path)
            self.check("edge headers are retained on " + path,
                       response.status == (401 if path.startswith("/api/") else 200)
                       and response.headers.get("cache-control") == "no-store"
                       and response.headers.get("x-content-type-options") == "nosniff"
                       and response.headers.get("x-frame-options") == "DENY"
                       and response.headers.get("referrer-policy") == "same-origin"
                       and all(value in response.headers.get("content-security-policy", "") for value in
                               ("frame-ancestors 'none'", "object-src 'none'", "base-uri 'self'", "form-action 'self'"))
                       and response.headers.get("server") == "nginx" and "access-control-allow-origin" not in response.headers)
        html = self.request("GET", "/").raw.decode("utf-8")
        assets = re.findall(r'(?:src|href)="(/assets/[A-Za-z0-9_-]+\.[a-z]+)"', html)
        asset = next((path for path in assets if path.endswith(".js") and "ant-design-" in path), None)
        self.check("HTML references a fingerprinted public JavaScript asset", bool(asset))
        response = self.request("GET", asset, headers={"Accept-Encoding": "gzip"})
        self.check("hashed JavaScript is immutable and gzip encoded",
                   response.status == 200 and response.headers.get("cache-control") == "public, max-age=31536000, immutable"
                   and response.headers.get("content-encoding") == "gzip" and "Accept-Encoding" in response.headers.get("vary", "")
                   and len(gzip.decompress(response.raw)) > len(response.raw))
        missing = self.request("GET", "/assets/missing-12345678.js")
        self.check("missing assets return non-cacheable 404 rather than HTML", missing.status == 404
                   and missing.headers.get("cache-control") == "no-store")

    def wait_for(self, name, predicate, seconds=40):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if predicate():
                self.check(name, True)
                return
            time.sleep(1)
        self.check(name, False)

    def restart_backend(self):
        backend = self.containers["backend"]
        labels = json.loads(self.command(["docker", "inspect", "--format", "{{json .Config.Labels}}", backend]))
        self.check("restart target is exactly the verified project's backend", labels.get("com.docker.compose.project") == self.project
                   and labels.get("com.docker.compose.service") == "backend")
        self.command(["docker", "restart", "--time", "20", backend], timeout=60)
        def ready():
            try:
                response = self.request("GET", "/api/health")
                return response.status == 200 and response.json().get("status") == "ok"
            except (OSError, http.client.HTTPException, SafeFailure):
                return False
        self.wait_for("own backend becomes healthy after restart", ready, 60)

    def orders(self, session):
        response = self.request("GET", "/api/orders", session=session)
        if response.status != 200:
            raise SafeFailure("orders-read-failed")
        return response.json()["items"]

    def activity(self, session):
        response = self.request("GET", "/api/activity", session=session)
        if response.status != 200:
            raise SafeFailure("activity-read-failed")
        return response.json()

    def raw_activity(self, merchant):
        # URL-safe base64 encodings of the two fixed fictional merchant IDs.
        suffix = "QQ" if merchant == "A" else "Qg"
        key = "refundworkbench:server:v1:merchant:{" + suffix + "}:activity"
        return [json.loads(line) for line in self.compose("exec", "-T", "redis", "redis-cli", "--raw", "LRANGE", key, "0", "99").splitlines() if line]

    @staticmethod
    def tenant_snapshot(snapshot, merchant):
        return {"orders": [row for row in snapshot["orders"] if row["merchant_id"] == merchant],
                "refunds": [row for row in snapshot["refunds"] if row["merchant_id"] == merchant],
                "channel_requests": [row for row in snapshot["channel_requests"] if row["order_id"].startswith(merchant + "-")]}

    def verify_business(self):
        self.expect("anonymous business access is denied", self.request("GET", "/api/orders"), 401, "UNAUTHORIZED")
        self.expect("untrusted login origin is rejected", self.request("POST", "/api/auth/login", {"identity_id": "a-write"},
                    origin="https://untrusted.example.invalid"), 403, "CSRF_REJECTED")
        a, viewer, b = self.login("a-write"), self.login("a-read"), self.login("b-write")
        self.check("merchant views contain exactly A four and B one original orders",
                   [row["order_id"] for row in self.orders(a)] == [row[0] for row in FIXTURES[:4]]
                   and [row["order_id"] for row in self.orders(b)] == ["B-2001"] and len(self.orders(viewer)) == 4)
        create = "/api/orders/A-1001/refunds"
        reason = {"reason": "独立容器并发验收"}
        for label, options in (("missing CSRF", {"csrf": None}), ("invalid CSRF", {"csrf": "invalid"}),
                               ("untrusted Origin", {"origin": "https://untrusted.example.invalid"})):
            self.expect(label + " rejects financial mutation", self.request("POST", create, reason, session=a, key="guard", **options), 403, "CSRF_REJECTED")
        self.expect("read-only role cannot create refunds", self.request("POST", create, reason, session=viewer, key="readonly"), 403, "FORBIDDEN")
        for session, path, method in ((a, "/api/orders/B-2001", "GET"), (b, "/api/orders/A-1001", "GET"),
                                     (viewer, "/api/orders/B-2001/refunds", "POST"), (a, "/api/orders/B-2001/refunds", "POST")):
            self.expect("cross-merchant object remains indistinguishable from missing", self.request(method, path,
                        reason if method == "POST" else None, session=session, key="cross"), 404, "NOT_FOUND")
        for field, value in (("merchant_id", "B"), ("amount_cents", 1)):
            self.expect("client cannot forge " + field, self.request("POST", create, {**reason, field: value}, session=a, key="forged"), 400, "VALIDATION_ERROR")
        self.check("rejected requests produced no refunds or channel calls", self.inspect_business()["counts"]["refunds"] == 0
                   and self.inspect_business()["counts"]["channel_requests"] == 0)
        with ThreadPoolExecutor(max_workers=10) as pool:
            responses = list(pool.map(lambda _: self.request("POST", create, reason, session=a, key="deployment-same-key"), range(10)))
        payloads = [response.json() for response in responses]
        self.check("ten concurrent identical requests converge to one refund and one creation",
                   all(response.status in {200, 201} for response in responses)
                   and sum(response.status == 201 for response in responses) == 1
                   and len({payload.get("refund_id") for payload in payloads}) == 1
                   and len({payload.get("provider_request_id") for payload in payloads}) == 1)
        first_id = payloads[0]["refund_id"]
        snapshot = self.inspect_business()
        channel = [row for row in snapshot["channel_requests"] if row["order_id"] == "A-1001"]
        self.check("SQL confirms one full-amount refund and one channel effect",
                   len(snapshot["refunds"]) == len(channel) == 1 and snapshot["refunds"][0]["status"] == "SUCCEEDED"
                   and int(snapshot["refunds"][0]["amount_cents"]) == 19900
                   and int(channel[0]["amount_cents"]) == 19900 and channel[0]["status"] == "SUCCEEDED"
                   and channel[0]["provider_request_id"] == snapshot["refunds"][0]["provider_request_id"]
                   and int(channel[0]["submit_calls"]) == int(channel[0]["effect_count"]) == 1)
        self.expect("another key cannot refund the same order again", self.request("POST", create, reason, session=a, key="different-key"), 409, "REFUND_EXISTS")
        self.expect("same key with a changed reason conflicts", self.request("POST", create, {"reason": "不同原因"}, session=a, key="deployment-same-key"), 409, "IDEMPOTENCY_CONFLICT")
        self.expect("same key with a changed order conflicts", self.request("POST", "/api/orders/A-1002/refunds", reason, session=a, key="deployment-same-key"), 409, "IDEMPOTENCY_CONFLICT")
        replay = self.request("POST", create, reason, session=a, key="deployment-same-key")
        self.check("same key replay returns the original current successful refund", replay.status == 200
                   and replay.json()["refund_id"] == first_id and replay.json()["status"] == "SUCCEEDED")
        after_conflicts = self.inspect_business()
        self.check("replay and changed parameters have no extra financial effects",
                   all(after_conflicts[key] == snapshot[key] for key in ("refunds", "channel_requests")))
        # A fresh order, sixteen DISTINCT keys: not a replay of an already
        # refunded order and not merely sixteen clicks blocked by the UI.
        with ThreadPoolExecutor(max_workers=16) as pool:
            different = list(pool.map(lambda i: self.request("POST", "/api/orders/A-1002/refunds", reason,
                        session=a, key=f"deployment-distinct-{i}"), range(16)))
        self.check("sixteen concurrent distinct keys produce one creation and fifteen explained conflicts",
                   sum(r.status == 201 for r in different) == 1
                   and sum(r.status == 409 and r.json().get("error", {}).get("code") == "REFUND_EXISTS" for r in different) == 15)
        unknown = next(r for r in different if r.status == 201)
        self.expect("lost channel response creates an explainable unknown refund", unknown, 201)
        unknown_data = unknown.json()
        self.check("transport uncertainty is never treated as final failure", unknown_data["status"] == "UNKNOWN")
        before_unknown_restart = self.inspect_business()
        concurrent_refunds = [r for r in before_unknown_restart["refunds"] if r["order_id"] == "A-1002"]
        concurrent_channels = [r for r in before_unknown_restart["channel_requests"] if r["order_id"] == "A-1002"]
        self.check("distinct-key concurrency leaves one SQL refund and one channel acceptance",
                   len(concurrent_refunds) == len(concurrent_channels) == 1
                   and concurrent_channels[0]["submit_calls"] == concurrent_channels[0]["effect_count"] == 1
                   and concurrent_channels[0]["query_calls"] == 0)
        self.restart_backend()
        unknown_path = "/api/refunds/" + unknown_data["refund_id"]
        restored = self.request("GET", unknown_path, session=a)
        self.check("UNKNOWN and original identities survive a real backend restart before any query",
                   restored.status == 200 and restored.json()["status"] == "UNKNOWN"
                   and all(restored.json()[key] == unknown_data[key] for key in ("refund_id", "provider_request_id"))
                   and all(self.inspect_business()[key] == before_unknown_restart[key] for key in ("refunds", "channel_requests")))
        for session, path, method in ((b, unknown_path, "GET"), (b, unknown_path + "/reconcile", "POST")):
            self.expect("cross-merchant refund access is rejected", self.request(method, path, session=session), 404, "NOT_FOUND")
        self.expect("viewer cannot reconcile", self.request("POST", "/api/refunds/" + unknown_data["refund_id"] + "/reconcile", session=viewer), 403, "FORBIDDEN")
        reconciled = self.request("POST", "/api/refunds/" + unknown_data["refund_id"] + "/reconcile", session=a)
        self.expect("query resolves the original channel result", reconciled, 200)
        self.check("query preserves the original refund and provider identities", reconciled.json()["status"] == "SUCCEEDED"
                   and all(reconciled.json()[key] == unknown_data[key] for key in ("refund_id", "provider_request_id")))
        failed = self.request("POST", "/api/orders/A-1003/refunds", reason, session=a, key="rejected-key")
        self.expect("explicit channel rejection creates a failed refund", failed, 201)
        self.check("only explicit rejection produces a failed terminal state with a readable reason",
                   failed.json()["status"] == "FAILED" and bool(failed.json().get("message", "").strip()))
        self.expect("failed refund cannot be resubmitted with a new key", self.request("POST", "/api/orders/A-1003/refunds", reason, session=a, key="retry-failed"), 409, "REFUND_EXISTS")
        self.expect("unpaid order is ineligible", self.request("POST", "/api/orders/A-1004/refunds", reason, session=a, key="unpaid-key"), 409, "ORDER_NOT_PAID")
        br = self.request("POST", "/api/orders/B-2001/refunds", reason, session=b, key="deployment-same-key")
        self.expect("same idempotency key is independently scoped to B", br, 201)
        self.check("B receives its own full amount", br.json()["status"] == "SUCCEEDED" and br.json()["amount_cents"] == 25600)
        snapshot = self.inspect_business()
        unknown_channel = next(row for row in snapshot["channel_requests"] if row["order_id"] == "A-1002")
        self.check("reconcile only queried once and unpaid order never reached channel",
                   int(unknown_channel["submit_calls"]) == int(unknown_channel["query_calls"]) == int(unknown_channel["effect_count"]) == 1
                   and not any(row["order_id"] == "A-1004" for row in snapshot["channel_requests"]))
        old_a_ids = {first_id, unknown_data["refund_id"], failed.json()["refund_id"]}
        b_id = br.json()["refund_id"]
        for session in (a, viewer):
            for method, suffix in (("GET", ""), ("POST", "/reconcile")):
                self.expect("A writer and viewer cannot access B refund objects", self.request(method,
                            "/api/refunds/" + b_id + suffix, session=session), 404, "NOT_FOUND")
        def published_activity():
            aa, ba = self.activity(a), self.activity(b)
            return (aa.get("available") and ba.get("available") and old_a_ids.issubset({item["refund_id"] for item in aa["items"]})
                    and any(item["refund_id"] == b_id for item in ba["items"]))
        self.wait_for("real outbox to Kafka to Redis activity becomes visible", published_activity)
        self.check("SQL outbox is published and Redis holds the matching business receipts",
                   self.inspect_business()["counts"]["outbox_pending"] == 0
                   and old_a_ids.issubset({item["refund_id"] for item in self.raw_activity("A")})
                   and any(item["refund_id"] == b_id for item in self.raw_activity("B")))
        self.verify_reset(a, viewer, b, old_a_ids, b_id)
        self.read_baseline(a)

    def verify_reset(self, a, viewer, b, old_a_ids, b_id):
        before_b = self.tenant_snapshot(self.inspect_business(), "B")
        for name, body, options, status in (
            ("reset requires write permission", CONFIRM, {"session": viewer}, 403),
            ("reset requires CSRF", CONFIRM, {"session": a, "csrf": None}, 403),
            ("reset requires the exact confirmation", {"confirmation": "确认"}, {"session": a}, 400),
            ("reset cannot override the merchant", {**CONFIRM, "merchant_id": "B"}, {"session": a}, 400),
        ):
            self.expect(name, self.request("POST", "/api/maintenance/reset", body, **options), status)
        response = self.request("POST", "/api/maintenance/reset", CONFIRM, session=a)
        self.expect("current-merchant reset succeeds with exact confirmation and CSRF", response, 200)
        reset = response.json()
        next_version = self.baseline["version_a"] + 1
        self.check("reset advances the current merchant version and restores four orders",
                   reset.get("merchant_id") == "A" and reset.get("order_count") == 4 and reset.get("data_version") == next_version)
        reset_id = reset.get("reset_id", "")
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,40}", reset_id):
            raise SafeFailure("invalid-reset-identity")
        archive = self.sql("SELECT JSON_OBJECT('archives',(SELECT COUNT(*) FROM maintenance_resets),"
                           "'merchant',merchant_id,'orders',JSON_LENGTH(JSON_EXTRACT(payload,'$.orders')),"
                           "'refunds',JSON_LENGTH(JSON_EXTRACT(payload,'$.refunds')),"
                           "'channels',JSON_LENGTH(JSON_EXTRACT(payload,'$.channel_requests')),"
                           "'idempotency',JSON_LENGTH(JSON_EXTRACT(payload,'$.idempotency_keys')),"
                           "'contains_b',JSON_SEARCH(payload,'one','B-2001') IS NOT NULL) FROM maintenance_resets WHERE reset_id='" + reset_id + "'")
        self.check("atomic archive is durable, current-merchant only and complete",
                   archive == {"archives": self.baseline["archives"] + 1, "merchant": "A", "orders": 4,
                               "refunds": 3, "channels": 3, "idempotency": 3, "contains_b": 0})
        active = self.orders(a)
        self.check("A active refunds are cleared while B business state is unchanged",
                   all(row["refund"] is None and row["data_version"] == next_version for row in active)
                   and len(active) == 4 and self.tenant_snapshot(self.inspect_business(), "B") == before_b
                   and self.orders(b)[0]["data_version"] == self.baseline["version_b"])
        counts = self.sql("SELECT JSON_OBJECT('refunds',(SELECT COUNT(*) FROM refunds WHERE merchant_id='A'),"
                          "'channels',(SELECT COUNT(*) FROM channel_requests WHERE order_id LIKE 'A-%'),"
                          "'idempotency',(SELECT COUNT(*) FROM idempotency_keys WHERE merchant_id='A'),"
                          "'outbox',(SELECT COUNT(*) FROM outbox_events WHERE merchant_id='A'),"
                          "'version',(SELECT data_version FROM merchant_reset_watermarks WHERE merchant_id='A'))")
        self.check("SQL active financial tables are cleared only for A", counts == {"refunds": 0, "channels": 0, "idempotency": 0, "outbox": 0, "version": next_version})
        activity = self.activity(a)
        self.check("reset watermark hides old activity without flushing Redis",
                   activity.get("available") and activity.get("items") == [] and activity.get("data_version") == next_version
                   and old_a_ids.issubset({item["refund_id"] for item in self.raw_activity("A")})
                   and any(item["refund_id"] == b_id for item in self.raw_activity("B")))
        fresh = self.request("POST", "/api/orders/A-1001/refunds", {"reason": "恢复后的独立验收"}, session=a, key="deployment-same-key")
        self.expect("restored fixture accepts one new refund using its cleared key", fresh, 201)
        new_id = fresh.json()["refund_id"]
        self.check("new refund is distinct and successful", new_id not in old_a_ids and fresh.json()["status"] == "SUCCEEDED")
        self.wait_for("new activity passes the fence while old refunds remain filtered", lambda: (
            (current := self.activity(a)).get("available") and any(item["refund_id"] == new_id for item in current["items"])
            and not old_a_ids.intersection(item["refund_id"] for item in current["items"])))
        before_restart = self.inspect_business()
        self.restart_backend()
        after_restart = self.inspect_business()
        self.check("refund, channel and order identities survive backend restart",
                   all(before_restart[key] == after_restart[key] for key in ("refunds", "channel_requests", "orders")))
        for session in (a, viewer, b):
            restored = self.request("GET", "/api/auth/session", session=session)
            self.check("persistent session survives backend restart", restored.status == 200
                       and restored.json().get("csrf_token") == session.csrf and restored.json().get("user") == session.user)
        after = self.activity(a)
        self.check("restart preserves the durable reset fence and B data",
                   after.get("data_version") == next_version and not old_a_ids.intersection(item["refund_id"] for item in after.get("items", []))
                   and self.tenant_snapshot(after_restart, "B") == before_b)
        self.report["final_counts"] = after_restart["counts"]
        self.report["reset"] = {"archive_count_before": self.baseline["archives"], "archive_count_after": archive["archives"],
                                "data_version_before": self.baseline["version_a"], "data_version_after": next_version, "other_merchant_unchanged": True}

    def read_baseline(self, session):
        # Isolate measurements from the preceding functional checks. Waiting for a
        # fresh fixed minute avoids the 300/min identity limiter; no keys deleted.
        cooldown = 60 - time.time() % 60 + 0.3
        print("Waiting for a fresh limiter minute before the 160-request read baseline", flush=True)
        time.sleep(cooldown)
        def sample(_):
            start = time.perf_counter()
            try:
                response = self.request("GET", "/api/orders", session=session)
                valid = response.status == 200 and len(response.json().get("items", [])) == 4
                return (time.perf_counter() - start) * 1000, None if valid else "http-" + str(response.status)
            except Exception:
                return (time.perf_counter() - start) * 1000, "transport-or-response-error"
        started = time.perf_counter()
        with ThreadPoolExecutor(max_workers=8) as pool:
            values = list(pool.map(sample, range(160)))
        elapsed = time.perf_counter() - started
        times = sorted(value[0] for value in values)
        errors = {}
        for _, error in values:
            if error:
                errors[error] = errors.get(error, 0) + 1
        self.report["read_baseline"] = {"requests": 160, "concurrency": 8, "endpoint": "GET /api/orders", "percentile_method": "nearest-rank",
            "p50_ms": round(times[math.ceil(len(times) * .5) - 1], 3), "p95_ms": round(times[math.ceil(len(times) * .95) - 1], 3),
            "max_ms": round(max(times), 3), "duration_seconds": round(elapsed, 3), "errors": errors,
            "error_count": sum(errors.values()), "limiter_cooldown_seconds_excluded": round(cooldown, 3), "sla_claim": False}
        self.check("bounded read baseline has no HTTP or transport errors", not errors)

    def run(self):
        self.validate_target()
        self.verify_edge()
        self.verify_business()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", required=True)
    parser.add_argument("--env-file", required=True)
    parser.add_argument("--base-url", required=True)
    args = parser.parse_args(argv)
    suite = Acceptance(args.project, args.env_file, args.base_url)
    try:
        suite.run()
        suite.report["result"] = "PASS"
        exit_code = 0
    except (Exception, KeyboardInterrupt) as error:
        suite.report["result"] = "FAIL"
        suite.report["error_type"] = type(error).__name__
        # Never interpolate exception messages: HTTP/SQL/auth errors can contain
        # payloads. Fixed SafeFailure codes are the sole allowed error detail.
        if isinstance(error, SafeFailure) and re.fullmatch(r"[a-z-]{1,80}", str(error)):
            suite.report["error_code"] = str(error)
        print("FAIL deployment acceptance; see sanitized report", file=sys.stderr, flush=True)
        exit_code = 1
    finally:
        suite.report["finished_at"] = datetime.now(timezone.utc).isoformat()
        REPORT_PATH.parent.mkdir(exist_ok=True)
        REPORT_PATH.write_text(json.dumps(suite.report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print("Report: evidence/deployment-validation.json", flush=True)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
