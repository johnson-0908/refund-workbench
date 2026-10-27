"""Isolated, real-HTTP acceptance for ERP demo sessions and authorization.

Run only when no other acceptance run owns refund_acceptance. This script resets
that one local test schema, builds its own executables, and only stops child
processes it launched. It never changes refund_demo or stops Docker services.
Session secrets and CSRF tokens stay in memory and are excluded from evidence.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import http.client
from http.cookies import SimpleCookie
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import time
import uuid

if hasattr(sys.stdout, 'reconfigure'):
    sys.stdout.reconfigure(encoding='utf-8')
    sys.stderr.reconfigure(encoding='utf-8')

ROOT = Path(__file__).resolve().parent.parent
RUNTIME = ROOT / '.runtime'
BIN = ROOT / 'bin'
EXT = '.exe' if os.name == 'nt' else ''
SERVER = BIN / ('refund-auth-acceptance-server' + EXT)
ADMIN = BIN / ('refund-auth-acceptance-admin' + EXT)
DSN = 'demo:demo-refund-only@tcp(127.0.0.1:3307)/refund_acceptance?parseTime=true&loc=UTC'
ENV = {**os.environ, 'MYSQL_DSN': DSN, 'ENABLE_DEMO_BEARER': 'false', 'ENABLE_DATA_RESET': 'false'}
ORIGIN = 'http://127.0.0.1:5173'
AUTO = object()
REPORT: dict = {
    'started_at': datetime.now(timezone.utc).isoformat(),
    'database': 'refund_acceptance',
    'scope': 'Real local HTTP, process restart, MySQL session expiry, Redis/Kafka merchant activity; not production identity proof',
    'checks': [],
}


def check(name: str, condition: bool, details=None):
    REPORT['checks'].append({'name': name, 'passed': bool(condition), 'details': details})
    print(('PASS ' if condition else 'FAIL ') + name, flush=True)
    if not condition:
        raise AssertionError(f'{name}: {details}')


def command(args, *, cwd=ROOT, env=ENV, timeout=120):
    result = subprocess.run([str(arg) for arg in args], cwd=cwd, env=env,
                            text=True, encoding='utf-8', capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{args[0]} failed: {result.stdout}\n{result.stderr}')
    return result.stdout


def admin(*args):
    return json.loads(command([ADMIN, *args]))


@dataclass
class Response:
    status: int
    body: object
    headers: list[tuple[str, str]]

    def cookie(self):
        for name, value in self.headers:
            if name.lower() == 'set-cookie':
                parsed = SimpleCookie()
                parsed.load(value)
                if 'erp_session' in parsed:
                    return parsed['erp_session']
        return None

    def summary(self):
        # Never return a login/session body: it contains a live CSRF secret.
        error = self.body.get('error', {}) if isinstance(self.body, dict) else {}
        return {'status': self.status, 'error_code': error.get('code')}


@dataclass(repr=False)
class Session:
    cookie: str
    csrf: str
    user: dict
    expires_at: str


class Server:
    def __init__(self, namespace: str, legacy=False):
        with socket.socket() as reserved:
            reserved.bind(('127.0.0.1', 0))
            self.port = reserved.getsockname()[1]
        self.env = {
            **ENV, 'HTTP_ADDR': f'127.0.0.1:{self.port}',
            'ENABLE_DEMO_BEARER': 'true' if legacy else 'false',
            'REDIS_ADDR': '127.0.0.1:6379', 'KAFKA_BROKERS': '127.0.0.1:19092',
            'KAFKA_TOPIC': f'refund.auth.acceptance.{namespace}',
            'KAFKA_GROUP': f'refund-auth-acceptance-{namespace}',
            'REDIS_PREFIX': f'refund:auth:acceptance:{namespace}:',
        }
        self.log = open(RUNTIME / f'auth-acceptance-{namespace}-{self.port}.log', 'w', encoding='utf-8')
        self.proc = subprocess.Popen([str(SERVER)], cwd=ROOT, env=self.env, stdout=self.log,
                                     stderr=subprocess.STDOUT,
                                     creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
        try:
            for _ in range(100):
                if self.proc.poll() is not None:
                    raise RuntimeError('Auth acceptance server exited; inspect its .runtime log')
                try:
                    if self.request('GET', '/api/health').status == 200:
                        return
                except (OSError, http.client.HTTPException):
                    pass
                time.sleep(.1)
            raise TimeoutError('Auth acceptance server readiness timeout')
        except BaseException:
            self.stop()
            raise

    def request(self, method, path, body=None, *, session=None, cookie=None,
                csrf=AUTO, origin=ORIGIN, key=None, headers=None):
        request_headers = dict(headers or {})
        if session is not None:
            request_headers['Cookie'] = 'erp_session=' + session.cookie
        if cookie is not None:
            request_headers['Cookie'] = 'erp_session=' + cookie
        if origin is not None:
            request_headers['Origin'] = origin
        if csrf is AUTO:
            csrf = session.csrf if session is not None else None
        if csrf is not None:
            request_headers['X-CSRF-Token'] = csrf
        if key is not None:
            request_headers['Idempotency-Key'] = key
        encoded = None
        if body is not None:
            encoded = json.dumps(body, ensure_ascii=False).encode('utf-8')
            request_headers['Content-Type'] = 'application/json'
        # Direct loopback connections do not inherit system proxy settings.
        conn = http.client.HTTPConnection('127.0.0.1', self.port, timeout=25)
        try:
            conn.request(method, path, body=encoded, headers=request_headers)
            response = conn.getresponse()
            raw = response.read()
            payload = json.loads(raw) if raw else None
            return Response(response.status, payload, response.getheaders())
        finally:
            conn.close()

    def login(self, identity_id: str, old=None):
        response = self.request('POST', '/api/auth/login', {'identity_id': identity_id}, session=old)
        check(f'login {identity_id} returns a server-owned session', response.status == 200, response.summary())
        cookie = response.cookie()
        data = response.body
        if cookie is None or not isinstance(data, dict):
            raise AssertionError('Login did not return an ERP cookie/session object')
        user = data.get('user', {})
        expected_merchant = 'B' if identity_id == 'b-write' else 'A'
        expected_role = 'viewer' if identity_id == 'a-read' else 'operator'
        expected_permissions = {'orders:read', 'refunds:read'}
        if expected_role == 'operator':
            expected_permissions.update({'refunds:create', 'refunds:reconcile'})
        check(f'identity {identity_id} receives authoritative merchant and permissions',
              user.get('identity_id') == identity_id and user.get('merchant_id') == expected_merchant
              and user.get('role') == expected_role
              and set(user.get('permissions', [])) == expected_permissions
              and bool(user.get('merchant_name')) and bool(user.get('display_name')), user)
        expiry = datetime.fromisoformat(data['expires_at'].replace('Z', '+00:00'))
        lifetime = (expiry - datetime.now(timezone.utc)).total_seconds()
        check('session cookie is host-only HttpOnly SameSite=Strict with an eight-hour expiry',
              bool(cookie['httponly']) and cookie['samesite'].lower() == 'strict'
              and cookie['path'] == '/' and cookie['domain'] == ''
              and cookie['max-age'] == '28800' and 28500 < lifetime <= 28800
              and isinstance(data.get('csrf_token'), str) and len(data['csrf_token']) >= 32,
              {'httponly': bool(cookie['httponly']), 'same_site': cookie['samesite'],
               'path': cookie['path'], 'host_only': cookie['domain'] == '',
               'max_age': cookie['max-age'], 'secure': bool(cookie['secure'])})
        return Session(cookie.value, data['csrf_token'], user, data['expires_at'])

    def stop(self):
        if self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait(timeout=10)
        self.log.close()


def expire_exact_session(session: Session):
    digest = hashlib.sha256(session.cookie.encode('utf-8')).hexdigest()
    if not re.fullmatch('[0-9a-f]{64}', digest):
        raise AssertionError('Invalid exact session hash')
    # The project/service/schema and WHERE clause are fixed; no table-wide
    # expiry or unrelated Docker project is permitted by this helper.
    sql = ("SELECT DATABASE(); UPDATE auth_sessions SET expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND "
           f"WHERE session_hash='{digest}'; SELECT ROW_COUNT();")
    output = command([
        'docker', 'compose', '-f', ROOT / 'compose.yaml', '-p', 'yishuld-refund',
        'exec', '-T', '-e', 'MYSQL_PWD=demo-refund-only', 'mysql', 'mysql',
        '--user=demo', '--database=refund_acceptance', '--batch', '--skip-column-names', '--execute', sql,
    ])
    lines = [line.strip() for line in output.splitlines() if line.strip()]
    check('expiry injection touches exactly one session in refund_acceptance', lines == ['refund_acceptance', '1'],
          {'database': lines[0] if lines else None, 'affected_rows': lines[-1] if lines else None})


def code_matrix(server, requests, expected, title):
    responses = [server.request(*args, **kwargs) for args, kwargs in requests]
    check(title, [response.status for response in responses] == expected,
          [response.summary() for response in responses])
    return responses


def main():
    RUNTIME.mkdir(exist_ok=True)
    BIN.mkdir(exist_ok=True)
    command(['go', 'build', '-o', SERVER, './cmd/server'], cwd=ROOT / 'backend')
    command(['go', 'build', '-o', ADMIN, './cmd/admin'], cwd=ROOT / 'backend')
    admin('reset', '--confirm')
    namespace = uuid.uuid4().hex[:12]
    server = Server(namespace)
    try:
        unauthenticated = [
            (('GET', '/api/auth/session'), {}), (('GET', '/api/orders'), {}),
            (('GET', '/api/orders/A-1001'), {}), (('GET', '/api/refunds/rf_not_authenticated'), {}),
            (('GET', '/api/activity'), {}),
            (('POST', '/api/orders/A-1001/refunds', {'reason': '未登录'}), {'key': 'unauthenticated'}),
            (('POST', '/api/refunds/rf_not_authenticated/reconcile'), {}),
            (('POST', '/api/auth/logout'), {}),
            (('GET', '/api/orders'), {'cookie': 'forged-session'}),
            (('GET', '/api/orders'), {'headers': {'Authorization': 'Bearer demo-a-write'}}),
            (('GET', '/api/activity'), {'headers': {'Authorization': 'Bearer demo-b-write'}}),
            (('POST', '/api/orders/A-1001/refunds', {'reason': '伪造旧凭证'}),
             {'key': 'forged-bearer', 'headers': {'Authorization': 'Bearer demo-a-write'}}),
        ]
        code_matrix(server, unauthenticated, [401] * len(unauthenticated),
                    'unauthenticated business reads/writes/activity and disabled legacy Bearer all return 401')
        code_matrix(server, [
            (('POST', '/api/auth/login', {'identity_id': 'a-write'}), {'origin': None}),
            (('POST', '/api/auth/login', {'identity_id': 'a-write'}), {'origin': 'https://untrusted.example'}),
            (('POST', '/api/auth/login', {'identity_id': 'a-write'}), {'origin': 'null'}),
        ], [403, 403, 403], 'login rejects missing, foreign and null Origin')
        code_matrix(server, [
            (('POST', '/api/auth/login', {'identity_id': 'administrator'}), {}),
            (('POST', '/api/auth/login', {'identity_id': 'a-read', 'merchant_id': 'B',
                                        'role': 'admin', 'permissions': ['refunds:create']}), {}),
        ], [400, 400], 'unknown identities and client-owned login permissions are rejected')
        check('rejected authentication requests cause no refund or channel acceptance',
              admin('inspect')['counts']['refunds'] == 0 and admin('inspect')['counts']['channel_requests'] == 0)

        a = server.login('a-write')
        reader = server.login('a-read')
        b = server.login('b-write')
        response = server.request('GET', '/api/auth/session?merchant_id=B&role=admin', session=reader,
                                  headers={'X-Merchant-ID': 'B', 'X-Identity-ID': 'a-write', 'X-Role': 'admin'})
        check('session ignores forged query/header merchant and permission claims',
              response.status == 200 and response.body['user'] == reader.user,
              response.body.get('user') if isinstance(response.body, dict) else response.summary())
        code_matrix(server, [
            (('GET', '/api/orders'), {'session': a, 'headers': {'Authorization': 'Bearer demo-b-write'}}),
            (('GET', '/api/orders'), {'session': a, 'headers': {'Authorization': 'Bearer invalid'}}),
        ], [401, 401], 'disabled Authorization never overrides or silently falls back to a valid cookie')
        for session, merchant, size in [(a, 'A', 4), (reader, 'A', 4), (b, 'B', 1)]:
            response = server.request('GET', '/api/orders?merchant_id=' + ('B' if merchant == 'A' else 'A'), session=session)
            items = response.body.get('items', []) if isinstance(response.body, dict) else []
            check(f'{session.user["identity_id"]} sees only its {merchant} orders despite a query override',
                  response.status == 200 and len(items) == size and all(item['merchant_id'] == merchant for item in items))

        csrf_requests = []
        for index, override in enumerate([
            {'csrf': None}, {'csrf': 'incorrect-csrf'}, {'csrf': b.csrf},
            {'origin': None}, {'origin': 'https://untrusted.example'}, {'origin': 'null'},
        ]):
            csrf_requests.append((('POST', '/api/orders/A-1001/refunds', {'reason': 'CSRF 拒绝验收'}),
                                  {'session': a, 'key': f'csrf-denied-{index}', **override}))
        code_matrix(server, csrf_requests, [403] * len(csrf_requests),
                    'write requests reject absent/wrong/cross-session CSRF and absent/foreign/null Origin')
        check('CSRF-rejected writes never allocate refunds or invoke the channel',
              admin('inspect')['counts']['refunds'] == 0 and admin('inspect')['counts']['channel_requests'] == 0)

        ar = server.request('POST', '/api/orders/A-1001/refunds', {'reason': 'A 商户权限验收'}, session=a, key='auth-a')
        br = server.request('POST', '/api/orders/B-2001/refunds', {'reason': 'B 商户权限验收'}, session=b, key='auth-b')
        check('valid operator cookies plus CSRF can refund their own paid orders',
              ar.status == 201 and br.status == 201 and ar.body.get('status') == br.body.get('status') == 'SUCCEEDED',
              [ar.summary(), br.summary()])
        a_refund, b_refund = ar.body['refund_id'], br.body['refund_id']
        code_matrix(server, [
            (('POST', '/api/orders/A-1002/refunds', {'reason': '只读写入', 'permissions': ['refunds:create']}), {'session': reader, 'key': 'reader'}),
            (('POST', f'/api/refunds/{a_refund}/reconcile'), {'session': reader}),
            (('GET', '/api/orders/B-2001'), {'session': a}),
            (('POST', '/api/orders/B-2001/refunds', {'reason': '越权'}), {'session': a, 'key': 'cross'}),
            (('GET', f'/api/refunds/{b_refund}'), {'session': a}),
            (('POST', f'/api/refunds/{b_refund}/reconcile'), {'session': a}),
            (('POST', '/api/orders/B-2001/refunds', {'reason': '只读越权'}), {'session': reader, 'key': 'reader-cross'}),
            (('POST', f'/api/refunds/{b_refund}/reconcile'), {'session': reader}),
            (('GET', '/api/orders/A-1001'), {'session': b}),
            (('GET', f'/api/refunds/{a_refund}'), {'session': b}),
        ], [403, 403, 404, 404, 404, 404, 404, 404, 404, 404],
                    'server enforces read-only 403 and bidirectional cross-merchant object 404')
        code_matrix(server, [
            (('POST', '/api/orders/A-1002/refunds', {'reason': '篡改', 'merchant_id': 'B', 'amount_cents': 1}), {'session': a, 'key': 'body-tamper'}),
        ], [400], 'authenticated operator cannot supply merchant or refund amount')
        snapshot = admin('inspect')
        check('denied role/scope/body requests produce no extra financial effect',
              snapshot['counts']['refunds'] == 2 and snapshot['counts']['channel_requests'] == 2
              and all(item['submit_calls'] == item['effect_count'] == 1 for item in snapshot['channel_requests']),
              snapshot['counts'])

        deadline = time.monotonic() + 60
        a_activity = b_activity = None
        while time.monotonic() < deadline:
            a_activity = server.request('GET', '/api/activity?merchant_id=B', session=a)
            b_activity = server.request('GET', '/api/activity?merchant_id=A', session=b)
            if (a_activity.status == b_activity.status == 200
                    and a_activity.body.get('available') and b_activity.body.get('available')
                    and any(event['refund_id'] == a_refund for event in a_activity.body['items'])
                    and any(event['refund_id'] == b_refund for event in b_activity.body['items'])):
                break
            time.sleep(.5)
        check('authenticated activity uses session merchant rather than query parameters',
              a_activity is not None and b_activity is not None
              and a_activity.status == b_activity.status == 200
              and a_activity.body.get('available') and b_activity.body.get('available')
              and len(a_activity.body['items']) >= 1 and len(b_activity.body['items']) >= 1
              and all(event['merchant_id'] == 'A' for event in a_activity.body['items'])
              and all(event['merchant_id'] == 'B' for event in b_activity.body['items']),
              {'a_count': len(a_activity.body.get('items', [])), 'b_count': len(b_activity.body.get('items', []))})

        old_pid = server.proc.pid
        server.stop()
        server = Server(namespace)
        response = server.request('GET', '/api/auth/session', session=a)
        check('hard backend restart preserves the same durable session and CSRF binding',
              server.proc.pid != old_pid and response.status == 200
              and response.body['user'] == a.user and response.body['csrf_token'] == a.csrf,
              {'old_pid': old_pid, 'new_pid': server.proc.pid, 'status': response.status})

        renewed = server.login('a-write', old=a)
        check('login rotates session and CSRF secrets', renewed.cookie != a.cookie and renewed.csrf != a.csrf)
        code_matrix(server, [
            (('GET', '/api/auth/session'), {'session': a}),
            (('GET', '/api/activity'), {'session': a}),
            (('POST', '/api/orders/A-1002/refunds', {'reason': '旧会话'}), {'session': a, 'key': 'old-cookie'}),
            (('POST', '/api/orders/A-1002/refunds', {'reason': '旧CSRF'}), {'session': renewed, 'csrf': a.csrf, 'key': 'old-csrf'}),
        ], [401, 401, 401, 403], 'rotation revokes copied old cookies and rejects the old CSRF with the new cookie')
        stale_tab = server.request('GET', '/api/orders', session=renewed, csrf=a.csrf)
        check('a stale tab cannot read with its old CSRF binding after shared-cookie rotation',
              stale_tab.status == 403 and stale_tab.body.get('error', {}).get('code') == 'SESSION_CHANGED',
              stale_tab.summary())
        restored = server.request('GET', '/api/auth/session', session=renewed, csrf=None)
        check('bare session restore can recover the current cookie after a tab change',
              restored.status == 200 and restored.body.get('user') == renewed.user)
        code_matrix(server, [
            (('POST', '/api/auth/logout'), {'session': renewed, 'csrf': None}),
            (('POST', '/api/auth/logout'), {'session': renewed, 'origin': 'https://untrusted.example'}),
        ], [403, 403], 'logout itself requires valid CSRF and Origin')
        logout = server.request('POST', '/api/auth/logout', session=renewed)
        cleared = logout.cookie()
        check('logout revokes server session and clears the browser cookie',
              logout.status == 200 and cleared is not None and cleared.value == '' and cleared['max-age'] == '0', logout.summary())
        code_matrix(server, [
            (('GET', '/api/auth/session'), {'session': renewed}),
            (('GET', '/api/orders'), {'session': renewed}),
            (('GET', '/api/activity'), {'session': renewed}),
        ], [401, 401, 401], 'copied logout cookie cannot access session, orders or activity')

        expire_exact_session(b)
        code_matrix(server, [
            (('GET', '/api/auth/session'), {'session': b}),
            (('GET', '/api/activity'), {'session': b}),
            (('POST', f'/api/refunds/{b_refund}/reconcile'), {'session': b}),
        ], [401, 401, 401], 'expired session is rejected on reads, auxiliary view and writes')
        check('exact expiry does not revoke another identity session',
              server.request('GET', '/api/auth/session', session=reader).status == 200)
        REPORT['final_business_counts'] = admin('inspect')['counts']
    finally:
        server.stop()

    # Explicit, process-local opt-in is the only way to restore the original
    # recruitment Bearer contract. The ordinary ERP server never receives it.
    server = Server(namespace, legacy=True)
    try:
        code_matrix(server, [
            (('GET', '/api/orders'), {'headers': {'Authorization': 'Bearer demo-a-write'}}),
            (('GET', '/api/orders'), {'headers': {'Authorization': 'Bearer invalid'}}),
            (('GET', '/api/orders'), {}),
        ], [200, 401, 401], 'legacy Bearer works only in an explicitly opted-in child process')
    finally:
        server.stop()


if __name__ == '__main__':
    failed = False
    try:
        main()
        REPORT['result'] = 'PASS'
    except BaseException as exc:
        failed = True
        REPORT['result'] = 'FAIL'
        REPORT['error'] = f'{type(exc).__name__}: {exc}'
        print(REPORT['error'], file=sys.stderr)
    finally:
        REPORT['finished_at'] = datetime.now(timezone.utc).isoformat()
        target = ROOT / 'evidence' / 'auth-acceptance.json'
        target.parent.mkdir(exist_ok=True)
        target.write_text(json.dumps(REPORT, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        print(f'Evidence: {target}')
    sys.exit(1 if failed else 0)
