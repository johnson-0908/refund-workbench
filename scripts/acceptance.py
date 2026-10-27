"""Real HTTP/process acceptance. Only resets the isolated refund_acceptance schema.

Requires the project Compose stack. Uses only Python's standard library.
It never stops Docker services or a server it did not launch.
"""
from __future__ import annotations

import concurrent.futures
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone

if hasattr(sys.stdout, 'reconfigure'):
    sys.stdout.reconfigure(encoding='utf-8')
    sys.stderr.reconfigure(encoding='utf-8')

ROOT = Path(__file__).resolve().parent.parent
RUNTIME = ROOT / '.runtime'
BIN = ROOT / 'bin'
DSN = 'demo:demo-refund-only@tcp(127.0.0.1:3307)/refund_acceptance?parseTime=true&loc=UTC'
EXT = '.exe' if os.name == 'nt' else ''
SERVER = BIN / ('refund-acceptance-server' + EXT)
ADMIN = BIN / ('refund-acceptance-admin' + EXT)
# Original exercise compatibility is explicit and isolated from normal ERP login.
ENV = {**os.environ, 'MYSQL_DSN': DSN, 'ENABLE_DEMO_BEARER': 'true', 'ENABLE_DATA_RESET': 'false'}
REPORT: dict = {'started_at': datetime.now(timezone.utc).isoformat(), 'database': 'refund_acceptance', 'checks': []}


def check(name: str, condition: bool, details=None):
    REPORT['checks'].append({'name': name, 'passed': bool(condition), 'details': details})
    print(('PASS ' if condition else 'FAIL ') + name, flush=True)
    if not condition:
        raise AssertionError(f'{name}: {details}')


def command(args, *, env=ENV, cwd=ROOT, timeout=120):
    done = subprocess.run([str(x) for x in args], env=env, cwd=cwd, text=True, encoding='utf-8', capture_output=True, timeout=timeout)
    if done.returncode:
        raise RuntimeError(f'{args[0]} failed: {done.stdout}\n{done.stderr}')
    return done.stdout


def admin(*args):
    return json.loads(command([ADMIN, *args]))


class Server:
    def __init__(self, namespace: str, degraded=False):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            self.port = sock.getsockname()[1]
        self.base = f'http://127.0.0.1:{self.port}'
        self.env = {**ENV, 'HTTP_ADDR': f'127.0.0.1:{self.port}',
                    'REDIS_ADDR': '127.0.0.1:1' if degraded else '127.0.0.1:6379',
                    'KAFKA_BROKERS': '127.0.0.1:1' if degraded else '127.0.0.1:19092',
                    'KAFKA_TOPIC': f'refund.acceptance.{namespace}',
                    'KAFKA_GROUP': f'refund-acceptance-{namespace}',
                    'REDIS_PREFIX': f'refund:acceptance:{namespace}:'}
        self.log = open(RUNTIME / f'acceptance-{namespace}-{self.port}.log', 'w', encoding='utf-8')
        self.proc = subprocess.Popen([str(SERVER)], cwd=ROOT, env=self.env, stdout=self.log, stderr=subprocess.STDOUT,
                                     creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
        try:
            for _ in range(100):
                if self.proc.poll() is not None:
                    raise RuntimeError('Acceptance server exited; see .runtime acceptance logs')
                try:
                    if self.request('GET', '/api/health')[0] == 200:
                        return
                except (OSError, urllib.error.URLError):
                    pass
                time.sleep(.1)
            raise TimeoutError('Acceptance server readiness timeout')
        except BaseException:
            self.stop()
            raise

    def request(self, method, path, body=None, token='demo-a-write', key=None):
        headers = {'Authorization': f'Bearer {token}'}
        if key is not None:
            headers['Idempotency-Key'] = key
        data = None
        if body is not None:
            data = json.dumps(body, ensure_ascii=False).encode('utf-8')
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=25) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as error:
            return error.code, json.load(error)

    def stop(self):
        if self.proc.poll() is None:
            self.proc.kill()  # actual hard process restart, not just a new DB connection
            self.proc.wait(timeout=10)
        self.log.close()


def channel(snapshot, order):
    return [r for r in snapshot['channel_requests'] if r['order_id'] == order]


def main():
    RUNTIME.mkdir(exist_ok=True)
    BIN.mkdir(exist_ok=True)
    command(['go', 'build', '-o', SERVER, './cmd/server'], cwd=ROOT / 'backend')
    command(['go', 'build', '-o', ADMIN, './cmd/admin'], cwd=ROOT / 'backend')
    # The schema name and host are literal and cannot be redirected by environment.
    admin('reset', '--confirm')
    namespace = uuid.uuid4().hex[:12]
    server = Server(namespace)
    try:
        status, orders = server.request('GET', '/api/orders')
        check('merchant A list contains only four A orders', status == 200 and len(orders['items']) == 4 and all(x['merchant_id'] == 'A' for x in orders['items']))
        status, normal = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '用户取消服务'}, key='normal')
        snap = admin('inspect')
        c = channel(snap, 'A-1001')
        check('normal refund agrees with persisted channel and integer amount', status == 201 and normal['status'] == 'SUCCEEDED' and normal['amount_cents'] == 19900 and len(c) == 1 and c[0]['status'] == 'SUCCEEDED' and c[0]['amount_cents'] == 19900 and c[0]['effect_count'] == 1, normal)
        replay_status, replay = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '用户取消服务'}, key='normal')
        changed = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '不同原因'}, key='normal')
        other_order = server.request('POST', '/api/orders/A-1002/refunds', {'reason': '用户取消服务'}, key='normal')
        new_key = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '用户取消服务'}, key='different-key')
        check('same key replays; changed reason/order and replacement key conflict', replay_status == 200 and replay['refund_id'] == normal['refund_id'] and changed[0] == other_order[0] == new_key[0] == 409 and channel(admin('inspect'), 'A-1001')[0]['submit_calls'] == 1)

        def attempt(i):
            return server.request('POST', '/api/orders/B-2001/refunds', {'reason': '并发全额退款'}, token='demo-b-write', key=f'parallel-{i}')

        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
            results = list(pool.map(attempt, range(16)))
        codes = [r[0] for r in results]
        snap = admin('inspect')
        c = channel(snap, 'B-2001')
        check('16 concurrent different keys create exactly one refund and one acceptance', codes.count(201) == 1 and codes.count(409) == 15 and len([r for r in snap['refunds'] if r['order_id'] == 'B-2001']) == 1 and len(c) == 1 and c[0]['submit_calls'] == c[0]['effect_count'] == 1, {'http_codes': codes, 'channel': c})
        b_refund = next(r[1] for r in results if r[0] == 201)

        denied = [server.request('GET', '/api/orders', token='invalid')[0],
                  server.request('GET', '/api/orders/B-2001')[0],
                  server.request('POST', '/api/orders/B-2001/refunds', {'reason': '越权'}, key='cross')[0],
                  server.request('GET', '/api/refunds/' + b_refund['refund_id'])[0],
                  server.request('POST', '/api/refunds/' + b_refund['refund_id'] + '/reconcile')[0],
                  server.request('POST', '/api/orders/A-1002/refunds', {'reason': '只读'}, token='demo-a-read', key='readonly')[0],
                  server.request('POST', '/api/refunds/' + normal['refund_id'] + '/reconcile', token='demo-a-read')[0],
                  server.request('GET', '/api/orders/A-1001', token='demo-a-read')[0],
                  server.request('POST', '/api/orders/B-2001/refunds', {'reason': '只读越权'}, token='demo-a-read', key='reader-cross')[0],
                  server.request('POST', '/api/refunds/' + b_refund['refund_id'] + '/reconcile', token='demo-a-read')[0]]
        check('401 / cross-merchant 404 / read-only writes 403 enforced server-side', denied == [401,404,404,404,404,403,403,200,404,404], denied)

        status, unknown = server.request('POST', '/api/orders/A-1002/refunds', {'reason': '响应丢失演示'}, key='unknown')
        check('accepted but lost response remains UNKNOWN', status == 201 and unknown['status'] == 'UNKNOWN' and channel(admin('inspect'), 'A-1002')[0]['status'] == 'SUCCEEDED', unknown)
        before_restart = admin('inspect')
        admin('seed')
        check('repeated seed preserves existing refunds and channel records', admin('inspect')['refunds'] == before_restart['refunds'] and admin('inspect')['channel_requests'] == before_restart['channel_requests'])
        old_pid = server.proc.pid
        server.stop()
        server = Server(namespace)
        status, retained = server.request('GET', '/api/refunds/' + unknown['refund_id'])
        query_status, resolved = server.request('POST', '/api/refunds/' + unknown['refund_id'] + '/reconcile')
        c = channel(admin('inspect'), 'A-1002')
        check('hard restart of backend + simulator recovers original request without resubmit', status == 200 and retained['status'] == 'UNKNOWN' and query_status == 200 and resolved['status'] == 'SUCCEEDED' and resolved['provider_request_id'] == unknown['provider_request_id'] and len(c) == 1 and c[0]['submit_calls'] == c[0]['effect_count'] == 1 and c[0]['query_calls'] == 1, {'old_pid': old_pid, 'new_pid': server.proc.pid, 'before': unknown, 'after': resolved, 'channel': c})
        status, failed = server.request('POST', '/api/orders/A-1003/refunds', {'reason': '明确拒绝演示'}, key='failed')
        invalid = server.request('POST', '/api/orders/A-1004/refunds', {'reason': '未支付'}, key='unpaid')
        forbidden_retry = server.request('POST', '/api/orders/A-1003/refunds', {'reason': '再次申请'}, key='retry-failed')
        check('explicit rejection and unpaid guard do not create extra acceptance', status == 201 and failed['status'] == 'FAILED' and bool(failed['message']) and invalid[0] == forbidden_retry[0] == 409 and channel(admin('inspect'), 'A-1004') == [])
        bad = server.request('POST', '/api/orders/A-1004/refunds', {'reason': '篡改金额', 'amount_cents': 1, 'merchant_id': 'B'}, key='tamper')
        check('client amount / merchant override fields rejected', bad[0] == 400)
        REPORT['business_snapshot'] = admin('inspect')
    finally:
        server.stop()

    # Deliberately wrong endpoints only in our own child process. No shared
    # Docker services are stopped to test dependency failure.
    admin('reset', '--confirm')
    namespace = uuid.uuid4().hex[:12] + '-outage'
    server = Server(namespace, degraded=True)
    try:
        status, refund = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '中间件故障隔离'}, key='outage')
        activity_status, activity = server.request('GET', '/api/activity')
        snap = admin('inspect')
        check('Redis/Kafka outage does not fail refund; outbox is retained', status == 201 and refund['status'] == 'SUCCEEDED' and activity_status == 200 and activity['available'] is False and snap['counts']['outbox_pending'] == 2, snap['counts'])
    finally:
        server.stop()
    server = Server(namespace)
    try:
        deadline = time.monotonic() + 90
        activity = {}
        while time.monotonic() < deadline:
            _, activity = server.request('GET', '/api/activity')
            if activity.get('available') and any(e['refund_id'] == refund['refund_id'] and e['status'] == 'SUCCEEDED' for e in activity['items']) and admin('inspect')['counts']['outbox_pending'] == 0:
                break
            time.sleep(.5)
        check('restored middleware drains durable outbox through Kafka into Redis', activity.get('available') is True and any(e['refund_id'] == refund['refund_id'] and e['status'] == 'SUCCEEDED' for e in activity.get('items', [])) and admin('inspect')['counts']['outbox_pending'] == 0, activity)
        _, b_activity = server.request('GET', '/api/activity', token='demo-b-write')
        check('activity projection is merchant-isolated and deduplicated', b_activity['items'] == [] and len({e['event_id'] for e in activity['items']}) == len(activity['items']))
        REPORT['middleware_snapshot'] = admin('inspect')
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
        target = ROOT / 'evidence' / 'acceptance.json'
        target.parent.mkdir(exist_ok=True)
        target.write_text(json.dumps(REPORT, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        print(f'Evidence: {target}')
    sys.exit(1 if failed else 0)
