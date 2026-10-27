"""Real HTTP acceptance of current-merchant reset, archival and event fencing.

Only refund_acceptance is reset or locked. Run exclusively, never alongside
acceptance.py/auth_acceptance.py. Redis/Kafka use this run's unique namespace;
no shared keys, topics, containers, or refund_demo records are cleared.
"""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from datetime import datetime, timezone
import base64
import hashlib
import http.client
import json
import os
import queue
import re
import socket
import subprocess
import sys
import threading
import time
import uuid

import auth_acceptance as auth

ROOT, RUNTIME, BIN = auth.ROOT, auth.RUNTIME, auth.BIN
SERVER = BIN / ('refund-reset-acceptance-server' + auth.EXT)
ADMIN = BIN / ('refund-reset-acceptance-admin' + auth.EXT)
ENV = {**auth.ENV, 'ENABLE_DATA_RESET': 'false'}
CONFIRM = {'confirmation': '恢复初始数据'}
MERCHANT_A_LOCK = 'refund-workbench:' + hashlib.sha256(b'refund_acceptance').hexdigest()[:16] + ':A'
REPORT: dict = {
    'started_at': datetime.now(timezone.utc).isoformat(), 'database': 'refund_acceptance',
    'scope': 'Local current-merchant reset only, durable SQL archive and activity watermark, real HTTP/process/concurrency checks',
    'checks': [],
}


def check(name, condition, details=None):
    REPORT['checks'].append({'name': name, 'passed': bool(condition), 'details': details})
    print(('PASS ' if condition else 'FAIL ') + name, flush=True)
    if not condition:
        raise AssertionError(f'{name}: {details}')


def command(args, *, input_text=None, timeout=120, cwd=ROOT, env=ENV):
    result = subprocess.run([str(arg) for arg in args], input=input_text, text=True, encoding='utf-8',
                            capture_output=True, timeout=timeout, cwd=cwd, env=env)
    if result.returncode:
        raise RuntimeError(f'{args[0]} failed: {result.stdout}\n{result.stderr}')
    return result.stdout


def admin(*args):
    return json.loads(command([ADMIN, *args]))


def docker_args(service, *args):
    return ['docker', 'compose', '-f', str(ROOT / 'compose.yaml'), '-p', 'yishuld-refund',
            'exec', '-T', service, *args]


def mysql_args(query, unbuffered=False):
    args = ['docker', 'compose', '-f', str(ROOT / 'compose.yaml'), '-p', 'yishuld-refund',
            'exec', '-T', '-e', 'MYSQL_PWD=demo-refund-only', 'mysql', 'mysql',
            '--user=demo', '--database=refund_acceptance', '--batch', '--raw', '--skip-column-names']
    if unbuffered:
        args.append('--unbuffered')
    return [*args, '--execute', query]


def sql(query):
    return command(mysql_args(query))


def scalar(query):
    return sql(query).strip()


def safe_id(value):
    if not isinstance(value, str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', value):
        raise AssertionError('Invalid server-generated identifier')
    return value


def snapshot_b():
    snapshot = admin('inspect')
    return {
        'orders': [item for item in snapshot['orders'] if item['merchant_id'] == 'B'],
        'refunds': [item for item in snapshot['refunds'] if item['merchant_id'] == 'B'],
        'channel_requests': [item for item in snapshot['channel_requests'] if item['order_id'] == 'B-2001'],
        'idempotency': scalar("SELECT JSON_ARRAYAGG(JSON_OBJECT('key',idempotency_key,'order',order_id,'reason',reason,'refund',refund_id)) FROM idempotency_keys WHERE merchant_id='B'"),
    }


def archive(reset_id):
    reset_id = safe_id(reset_id)
    value = scalar(f"SELECT JSON_OBJECT('reset_id',reset_id,'merchant_id',merchant_id,'payload',payload,'created_at',created_at) FROM maintenance_resets WHERE reset_id='{reset_id}'")
    return json.loads(value)


def assert_a_initial(server, session, title, version):
    response = server.request('GET', '/api/orders', session=session)
    expected = {'A-1001': (19900, 'PAID'), 'A-1002': (8800, 'PAID'),
                'A-1003': (5600, 'PAID'), 'A-1004': (12900, 'UNPAID')}
    items = response.body.get('items', [])
    correct = response.status == 200 and len(items) == 4
    correct = correct and {item['order_id'] for item in items} == set(expected)
    correct = correct and all(item['merchant_id'] == 'A' and item['refund'] is None
                              and item.get('data_version') == version
                              and (item['amount_cents'], item['status']) == expected[item['order_id']] for item in items)
    counts = json.loads(scalar("SELECT JSON_OBJECT('refunds',(SELECT COUNT(*) FROM refunds WHERE merchant_id='A'),"
                              "'idempotency',(SELECT COUNT(*) FROM idempotency_keys WHERE merchant_id='A'),"
                              "'channel',(SELECT COUNT(*) FROM channel_requests WHERE order_id IN ('A-1001','A-1002','A-1003','A-1004')))") or '{}')
    check(title, correct and counts == {'refunds': 0, 'idempotency': 0, 'channel': 0}, counts)


class Server(auth.Server):
    """Use auth's HTTP client and process-stop helper, but isolated reset settings."""
    def __init__(self, namespace, enabled=False):
        self.enabled = enabled
        with socket.socket() as reserved:
            reserved.bind(('127.0.0.1', 0))
            self.port = reserved.getsockname()[1]
        self.topic = f'refund.reset.acceptance.{namespace}'
        self.prefix = f'refund:reset:acceptance:{namespace}:'
        self.env = {
            **ENV, 'HTTP_ADDR': f'127.0.0.1:{self.port}', 'ENABLE_DEMO_BEARER': 'false',
            'ENABLE_DATA_RESET': 'true' if enabled else 'false',
            'REDIS_ADDR': '127.0.0.1:6379', 'KAFKA_BROKERS': '127.0.0.1:19092',
            'KAFKA_TOPIC': self.topic, 'KAFKA_GROUP': f'refund-reset-acceptance-{namespace}',
            'REDIS_PREFIX': self.prefix,
        }
        if not enabled:
            self.env.pop('ENABLE_DATA_RESET', None)
        self.log = open(RUNTIME / f'reset-acceptance-{namespace}-{self.port}.log', 'w', encoding='utf-8')
        self.proc = subprocess.Popen([str(SERVER)], cwd=ROOT, env=self.env, stdout=self.log,
                                     stderr=subprocess.STDOUT,
                                     creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
        try:
            for _ in range(100):
                if self.proc.poll() is not None:
                    raise RuntimeError('Reset acceptance server exited; inspect its .runtime log')
                try:
                    if self.request('GET', '/api/health').status == 200:
                        return
                except (OSError, http.client.HTTPException):
                    pass
                time.sleep(.1)
            raise TimeoutError('Reset acceptance readiness timeout')
        except BaseException:
            self.stop()
            raise

    def login(self, identity_id):
        response = self.request('POST', '/api/auth/login', {'identity_id': identity_id})
        cookie = response.cookie()
        check(f'login {identity_id} succeeds for reset acceptance', response.status == 200 and cookie is not None, response.summary())
        data = response.body
        session = auth.Session(cookie.value, data['csrf_token'], data['user'], data['expires_at'])
        can_reset = self.enabled and identity_id != 'a-read'
        check(f'{identity_id} data:reset capability follows explicit server flag and role',
              ('data:reset' in session.user['permissions']) == can_reset,
              {'enabled': self.enabled, 'identity': identity_id, 'permissions': session.user['permissions']})
        return session


def wait_for(predicate, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(.1)
    return False


def raw_activity(server, merchant='A'):
    encoded = base64.urlsafe_b64encode(merchant.encode()).decode().rstrip('=')
    key = server.prefix + 'merchant:{' + encoded + '}:activity'
    return [json.loads(line) for line in command(docker_args('redis', 'redis-cli', '--raw', 'LRANGE', key, '0', '99')).splitlines() if line.strip()]


def replay_old_event(server, payload):
    # A fresh test event_id bypasses normal de-duplication, while the pre-reset
    # refund identity/timestamp remain unchanged. This tests the reset fence,
    # not merely the already-tested event_id deduplication.
    old = dict(payload)
    old['event_id'] = 'ev_replay_' + uuid.uuid4().hex[:20]
    command(docker_args('kafka', '/opt/kafka/bin/kafka-console-producer.sh',
                        '--bootstrap-server', 'kafka:9092', '--topic', server.topic,
                        '--producer-property', 'acks=all'), input_text=json.dumps(old, ensure_ascii=False) + '\n')
    check('old Kafka payload is actually consumed into this test Redis namespace',
          wait_for(lambda: any(event['event_id'] == old['event_id'] for event in raw_activity(server)), 30))
    return old['event_id']


@contextmanager
def held_mysql_lock(query):
    """The only SQL lock targets are tables/rows in refund_acceptance."""
    proc = subprocess.Popen(mysql_args(query, unbuffered=True), cwd=ROOT, env=ENV,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8',
                            creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
    try:
        lines = queue.Queue()
        threading.Thread(target=lambda: lines.put(proc.stdout.readline()), daemon=True).start()
        ready = lines.get(timeout=10).strip()
        if ready != 'reset_acceptance_lock_ready':
            raise RuntimeError('Could not acquire isolated SQL test lock: ' + ready + proc.stderr.read())
        yield proc
    finally:
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
        if proc.returncode:
            raise RuntimeError('Isolated lock holder failed: ' + proc.stderr.read())
        proc.stdout.close()
        proc.stderr.close()


def reset(server, session, title):
    response = server.request('POST', '/api/maintenance/reset', CONFIRM, session=session)
    body = response.body
    check(title, response.status == 200 and body.get('merchant_id') == 'A' and body.get('order_count') == 4
          and bool(body.get('reset_id')) and bool(body.get('reset_at')) and bool(body.get('message')),
          body if response.status == 200 else response.summary())
    return body


def main():
    RUNTIME.mkdir(exist_ok=True)
    BIN.mkdir(exist_ok=True)
    command(['go', 'build', '-o', SERVER, './cmd/server'], cwd=ROOT / 'backend')
    command(['go', 'build', '-o', ADMIN, './cmd/admin'], cwd=ROOT / 'backend')
    admin('reset', '--confirm')
    check('test SQL connection is pinned to refund_acceptance', scalar('SELECT DATABASE()') == 'refund_acceptance')
    archive_baseline = int(scalar('SELECT COUNT(*) FROM maintenance_resets'))
    namespace = uuid.uuid4().hex[:12]
    server = Server(namespace)
    try:
        off = server.login('a-write')
        response = server.request('POST', '/api/maintenance/reset', CONFIRM, session=off)
        check('reset is disabled by default even for an operator',
              response.status == 403 and response.body.get('error', {}).get('code') == 'RESET_DISABLED', response.summary())
    finally:
        server.stop()

    server = Server(namespace, enabled=True)
    peer = None
    try:
        a, reader, b = server.login('a-write'), server.login('a-read'), server.login('b-write')
        cases = [
            ({}, CONFIRM, 401), ({'session': reader}, CONFIRM, 403),
            ({'session': a, 'csrf': None}, CONFIRM, 403),
            ({'session': a, 'csrf': 'invalid'}, CONFIRM, 403),
            ({'session': a, 'origin': 'https://untrusted.example'}, CONFIRM, 403),
            ({'session': a}, {'confirmation': '确认'}, 400),
            ({'session': a}, {**CONFIRM, 'merchant_id': 'B'}, 400),
        ]
        responses = [server.request('POST', '/api/maintenance/reset', body, **kwargs) for kwargs, body, _ in cases]
        check('reset requires authentication, write permission, CSRF, Origin, exact confirmation and no merchant override',
              [response.status for response in responses] == [expected for _, _, expected in cases],
              [response.summary() for response in responses])

        ar = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '恢复前A数据'}, session=a, key='reset-reusable-key')
        au = server.request('POST', '/api/orders/A-1002/refunds', {'reason': '恢复前未知结果'}, session=a, key='reset-unknown')
        br = server.request('POST', '/api/orders/B-2001/refunds', {'reason': '必须保留B数据'}, session=b, key='reset-b')
        check('A and B have independent durable refund/channel fixtures before reset',
              ar.status == au.status == br.status == 201 and ar.body['status'] == br.body['status'] == 'SUCCEEDED'
              and au.body['status'] == 'UNKNOWN')
        before_b = snapshot_b()
        old_a_ids = {ar.body['refund_id'], au.body['refund_id']}
        check('pre-reset activity reaches Redis through Kafka', wait_for(lambda: len(raw_activity(server)) >= 4, 30))
        old_payload = next(event for event in raw_activity(server) if event['refund_id'] in old_a_ids)
        first = reset(server, a, 'current-merchant reset returns its durable reset identity and fixture count')
        check('first reset increments the durable data version from zero to one', first.get('data_version') == 1)
        assert_a_initial(server, a, 'A reset restores exactly its four fixtures and removes its active financial records', 1)
        check('A reset leaves B orders, refund, channel receipt and idempotency unchanged', snapshot_b() == before_b)
        saved = archive(first['reset_id'])
        payload = saved['payload']
        serialized = json.dumps(payload, ensure_ascii=False)
        forbidden_values = [session.cookie for session in (a, reader, b)] + [session.csrf for session in (a, reader, b)]
        forbidden_values += [hashlib.sha256(session.cookie.encode()).hexdigest() for session in (a, reader, b)]
        check('atomic archive contains only the six merchant business tables, never session secrets',
              saved['merchant_id'] == 'A'
              and set(payload) == {'orders', 'refunds', 'idempotency_keys', 'channel_scenarios', 'channel_requests', 'outbox_events'}
              and len(payload['orders']) == 4 and len(payload['refunds']) == 2
              and len(payload['idempotency_keys']) == 2 and len(payload['channel_requests']) == 2
              and all(old_id in serialized for old_id in old_a_ids)
              and br.body['refund_id'] not in serialized
              and all(value not in serialized for value in forbidden_values)
              and all(key not in serialized for key in ('auth_sessions', 'session_hash', 'csrf_token', 'erp_session')),
              {'tables': sorted(payload), 'refunds': len(payload.get('refunds', [])), 'orders': len(payload.get('orders', []))})
        check('A reset preserves all three valid login sessions',
              all(server.request('GET', '/api/auth/session', session=current).status == 200 for current in (a, reader, b)))
        response = server.request('GET', '/api/activity', session=a)
        check('old Redis activity is hidden without flushing the shared Redis service',
              response.status == 200 and response.body.get('available') and response.body.get('items') == []
              and response.body.get('data_version') == 1
              and len(raw_activity(server)) >= 4)
        replay_id = replay_old_event(server, old_payload)
        response = server.request('GET', '/api/activity', session=a)
        check('persisted reset watermark excludes replayed old Kafka activity',
              response.status == 200 and response.body.get('available') and response.body.get('items') == [])
        watermark = json.loads(scalar("SELECT JSON_OBJECT('reset_id',reset_id,'reset_at',reset_at,'data_version',data_version) FROM merchant_reset_watermarks WHERE merchant_id='A'"))
        check('watermark is durable and belongs to the committed reset',
              watermark['reset_id'] == first['reset_id'] and watermark['data_version'] == 1, watermark)

        old_pid = server.proc.pid
        server.stop()
        server = Server(namespace, enabled=True)
        response = server.request('GET', '/api/activity', session=a)
        check('hard restart still filters old cached/replayed events and preserves the session',
              server.proc.pid != old_pid and response.status == 200 and response.body.get('available')
              and response.body.get('data_version') == 1
              and response.body.get('items') == [] and any(event['event_id'] == replay_id for event in raw_activity(server)),
              {'old_pid': old_pid, 'new_pid': server.proc.pid})

        fresh = server.request('POST', '/api/orders/A-1001/refunds', {'reason': '恢复后重新开始'}, session=a, key='reset-reusable-key')
        fresh_snapshot = admin('inspect')
        fresh_channel = [row for row in fresh_snapshot['channel_requests'] if row['order_id'] == 'A-1001']
        check('restored A can reuse its old key for one new refund and exactly one channel effect',
              fresh.status == 201 and fresh.body['refund_id'] not in old_a_ids and fresh.body['status'] == 'SUCCEEDED'
              and len(fresh_channel) == 1 and fresh_channel[0]['submit_calls'] == fresh_channel[0]['effect_count'] == 1)
        check('new A events pass the reset fence while every old A refund stays hidden', wait_for(lambda: (
            (activity := server.request('GET', '/api/activity', session=a)).status == 200
            and any(event['refund_id'] == fresh.body['refund_id'] for event in activity.body.get('items', []))
            and all(event['refund_id'] not in old_a_ids for event in activity.body.get('items', []))), 30))
        check('B activity and business state survive A reset and restart',
              snapshot_b() == before_b and any(event['refund_id'] == br.body['refund_id']
                                              for event in server.request('GET', '/api/activity', session=b).body.get('items', [])))

        # A different HTTP process must obey the SQL advisory lock too; using
        # only the first process would exercise only its in-memory mutex.
        peer = Server(namespace, enabled=True)

        # Force a real QueryRefund row-lock wait, then request reset while that
        # business operation owns the merchant lock. The archive must reflect
        # the completed query, never delete underneath it.
        unknown = server.request('POST', '/api/orders/A-1002/refunds', {'reason': '查证与恢复互斥'}, session=a, key='reset-inflight-query')
        check('inflight query fixture starts UNKNOWN', unknown.status == 201 and unknown.body['status'] == 'UNKNOWN')
        request_id = safe_id(unknown.body['provider_request_id'])
        lock_query = ("START TRANSACTION; SELECT provider_request_id INTO @held_request FROM channel_requests "
                      f"WHERE provider_request_id='{request_id}' FOR UPDATE; "
                      "SELECT 'reset_acceptance_lock_ready'; DO SLEEP(3); COMMIT;")
        with held_mysql_lock(lock_query), ThreadPoolExecutor(max_workers=2) as pool:
            reconcile = pool.submit(server.request, 'POST', '/api/refunds/' + unknown.body['refund_id'] + '/reconcile', session=a)
            lock_held = wait_for(lambda: scalar(f"SELECT IS_USED_LOCK('{MERCHANT_A_LOCK}')") != 'NULL', 1)
            check('inflight reconcile owns the merchant SQL lock while held at the real channel row', lock_held and not reconcile.done())
            resetting = pool.submit(peer.request, 'POST', '/api/maintenance/reset', CONFIRM, session=a)
            time.sleep(.2)
            check('a second process cannot reset while reconcile owns the merchant operation', not resetting.done())
            reconciled, reset_query = reconcile.result(timeout=10), resetting.result(timeout=10)
        check('inflight reconcile completes before reset atomically archives it',
              reconciled.status == 200 and reconciled.body.get('status') == 'SUCCEEDED' and reset_query.status == 200,
              [reconciled.summary(), reset_query.summary()])
        query_archive = archive(reset_query.body['reset_id'])['payload']
        query_refund = [row for row in query_archive['refunds'] if row.get('id', row.get('refund_id')) == unknown.body['refund_id']]
        query_channel = [row for row in query_archive['channel_requests'] if row['provider_request_id'] == request_id]
        check('query/reset archive records final channel-backed result and no resubmission',
              len(query_refund) == len(query_channel) == 1 and query_refund[0]['status'] == 'SUCCEEDED'
              and query_channel[0]['query_calls'] == 1 and query_channel[0]['submit_calls'] == 1)
        check('query/reset atomically advances the data version', reset_query.body.get('data_version') == 2)
        assert_a_initial(server, a, 'no active A refund/channel record reappears after the in-flight query finishes', 2)

        # A short lock is scoped to this isolated schema's simulator table.
        # The business refund row commits before SubmitRefund blocks here.
        table_lock = "LOCK TABLES channel_requests WRITE; SELECT 'reset_acceptance_lock_ready'; DO SLEEP(3); UNLOCK TABLES;"
        with held_mysql_lock(table_lock), ThreadPoolExecutor(max_workers=2) as pool:
            submitting = pool.submit(server.request, 'POST', '/api/orders/A-1001/refunds', {'reason': '提交与恢复互斥'}, session=a, key='reset-inflight-submit')
            check('inflight submit reserves its business row before the blocked channel call',
                  wait_for(lambda: scalar("SELECT COUNT(*) FROM refunds WHERE order_id='A-1001'") == '1', 1))
            check('inflight submit holds the merchant SQL advisory lock',
                  scalar(f"SELECT IS_USED_LOCK('{MERCHANT_A_LOCK}')") != 'NULL' and not submitting.done())
            resetting = pool.submit(peer.request, 'POST', '/api/maintenance/reset', CONFIRM, session=a)
            time.sleep(.2)
            check('reset cannot delete a refund while its SubmitRefund is in flight', not resetting.done())
            submitted, reset_submit = submitting.result(timeout=10), resetting.result(timeout=10)
        check('inflight submit and reset return explainable final responses',
              submitted.status == 201 and submitted.body.get('status') == 'SUCCEEDED' and reset_submit.status == 200,
              [submitted.summary(), reset_submit.summary()])
        submit_archive = archive(reset_submit.body['reset_id'])['payload']
        check('submit/reset archive contains one completed refund and one channel effect',
              len(submit_archive['refunds']) == len(submit_archive['channel_requests']) == 1
              and submit_archive['refunds'][0]['status'] == 'SUCCEEDED'
              and submit_archive['channel_requests'][0]['submit_calls'] == submit_archive['channel_requests'][0]['effect_count'] == 1)
        check('submit/reset atomically advances the data version', reset_submit.body.get('data_version') == 3)
        assert_a_initial(server, a, 'inflight submit leaves no orphan state after reset', 3)

        repeated = reset(server, a, 'repeating reset on initial fixtures remains valid and auditable')
        repeated_archive = archive(repeated['reset_id'])['payload']
        check('each reset gets a distinct archive; repeated initial-state reset has no phantom refunds',
              len({first['reset_id'], reset_query.body['reset_id'], reset_submit.body['reset_id'], repeated['reset_id']}) == 4
              and len(repeated_archive['orders']) == 4 and repeated_archive['refunds'] == []
              and repeated_archive['channel_requests'] == [] and repeated.get('data_version') == 4)
        check('reset archives append without deleting historical evidence',
              int(scalar('SELECT COUNT(*) FROM maintenance_resets')) == archive_baseline + 4,
              {'archive_count_before': archive_baseline, 'archive_count_after': archive_baseline + 4})
        check('all reset/race tests leave merchant B financial data unchanged', snapshot_b() == before_b)
        REPORT['final_counts'] = admin('inspect')['counts']
        REPORT['reset_ids'] = [first['reset_id'], reset_query.body['reset_id'], reset_submit.body['reset_id'], repeated['reset_id']]
    finally:
        if peer is not None:
            peer.stop()
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
        target = ROOT / 'evidence' / 'reset-acceptance.json'
        target.parent.mkdir(exist_ok=True)
        target.write_text(json.dumps(REPORT, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        print(f'Evidence: {target}')
    sys.exit(1 if failed else 0)
