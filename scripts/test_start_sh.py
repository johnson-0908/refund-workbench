"""Bash launcher regressions with isolated files and stubbed Docker/open."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(Path('/bin/bash').exists(), 'Requires Bash')
class StartTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='refund-start-test-')
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / 'source with spaces'
        (self.root / 'scripts').mkdir(parents=True)
        shutil.copy2(ROOT / 'scripts/start.sh', self.root / 'scripts/start.sh')
        shutil.copy2(ROOT / 'start.command', self.root / 'start.command')
        shutil.copy2(ROOT / 'stop.command', self.root / 'stop.command')
        self.bin = self.base / 'bin'
        self.bin.mkdir()
        self.calls = self.base / 'calls'
        self.config = self.root / '.runtime/start-refund-deploy-test-shell.env'
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        TEST_CALLS=str(self.calls))
        self.stub('docker', '''import os, sys
from pathlib import Path
args = sys.argv[1:]
with open(os.environ['TEST_CALLS'], 'a') as log:
    log.write('docker ' + ' '.join(args) + '\\n')
if args[0] == 'info' and os.environ.get('TEST_ENGINE_DOWN'):
    sys.exit(1)
if args[:2] == ['volume', 'ls'] and os.environ.get('TEST_EXISTING_VOLUME'):
    print('synthetic-volume')
if 'config' in args:
    p = Path(args[args.index('--env-file') + 1])
    saved = dict(line.split('=', 1) for line in p.read_text().splitlines() if line and not line.startswith('#'))
    for key in ('MYSQL_PASSWORD', 'APP_ORIGIN'):
        assert os.environ[key] == saved[key]
    assert 'BUILD_CONTEXT' not in os.environ
if 'up' in args and os.environ.get('TEST_UP_FAIL'):
    sys.exit(7)
if 'stop' in args and os.environ.get('TEST_STOP_FAIL'):
    sys.exit(8)
''')
        self.stub('open', '''import os, sys
with open(os.environ['TEST_CALLS'], 'a') as log:
    log.write('open ' + ' '.join(sys.argv[1:]) + '\\n')
sys.exit(1 if os.environ.get('TEST_OPEN_FAIL') else 0)
''')
        self.stub('ss', '''import os
if os.environ.get('TEST_PORT_BUSY'):
    print('LISTEN synthetic')
''')

    def stub(self, name, body):
        target = self.bin / name
        target.write_text('#!' + sys.executable + '\n' + body)
        target.chmod(0o755)

    def run_start(self, *args, wrapper=False, success=True):
        entry = 'start.command' if wrapper else 'scripts/start.sh'
        result = subprocess.run(['/bin/bash', str(self.root / entry), '--project',
                                 'refund-deploy-test-shell', *args], cwd=self.base,
                                env=self.env, capture_output=True, text=True, timeout=20)
        if success:
            self.assertEqual(0, result.returncode, result.stderr)
        else:
            self.assertNotEqual(0, result.returncode)
        if self.config.exists():
            for line in self.config.read_text().splitlines():
                if line.startswith(('MYSQL_PASSWORD=', 'MYSQL_ROOT_PASSWORD=')):
                    self.assertNotIn(line.split('=', 1)[1], result.stdout + result.stderr)
        return result

    def saved(self):
        return self.config.read_bytes()

    def run_stop(self, *args, success=True):
        result = subprocess.run(['/bin/bash', str(self.root / 'stop.command'), *args],
                                cwd=self.base, env=self.env, capture_output=True,
                                text=True, timeout=20)
        if success:
            self.assertEqual(0, result.returncode, result.stderr)
        else:
            self.assertNotEqual(0, result.returncode)
        return result

    def test_stop_scopes_project_and_preserves_config_on_repeat(self):
        self.run_start('--check')
        initial = self.saved()
        self.calls.write_text('')
        for _ in range(2):
            self.run_stop('--project', 'refund-deploy-test-shell')
        calls = self.calls.read_text().splitlines()
        stops = [line for line in calls if line.endswith(' stop')]
        self.assertEqual(2, len(stops))
        for line in stops:
            self.assertIn('--project-name refund-deploy-test-shell --env-file ' + str(self.config), line)
        self.assertFalse(any(' down' in line or ' rm' in line or 'open ' in line for line in calls))
        self.assertEqual(initial, self.saved())

    def test_stop_default_project(self):
        self.run_start('--check')
        default = self.config.with_name('start-yishuld-refund-app.env')
        default.write_text(self.config.read_text().replace('refund-deploy-test-shell', 'yishuld-refund-app'))
        self.run_stop()
        self.assertIn('--project-name yishuld-refund-app --env-file ' + str(default), self.calls.read_text())

    def test_stop_missing_config_and_invalid_args_do_not_call_docker(self):
        for args in ((), ('--project',), ('--project', '../bad'), ('--all',)):
            self.run_stop(*args, success=False)
        self.assertFalse(self.calls.exists())
        self.assertFalse(self.config.exists())

    def test_stop_mismatched_or_duplicate_project_refused(self):
        self.run_start('--check')
        initial = self.saved()
        for broken in (initial.replace(b'DEPLOY_NAMESPACE=refund-deploy-test-shell', b'DEPLOY_NAMESPACE=another'),
                       initial + b'DEPLOY_NAMESPACE=refund-deploy-test-shell\n'):
            self.config.write_bytes(broken)
            self.calls.write_text('')
            self.run_stop('--project', 'refund-deploy-test-shell', success=False)
            self.assertEqual('', self.calls.read_text())
            self.assertEqual(broken, self.saved())

    def test_stop_engine_unavailable_does_not_open_docker(self):
        self.run_start('--check')
        self.calls.write_text('')
        self.env['TEST_ENGINE_DOWN'] = '1'
        self.run_stop('--project', 'refund-deploy-test-shell', success=False)
        self.assertNotIn('open ', self.calls.read_text())
        self.assertNotIn(' stop', self.calls.read_text())

    def test_stop_failure_does_not_claim_success(self):
        self.run_start('--check')
        self.env['TEST_STOP_FAIL'] = '1'
        result = self.run_stop('--project', 'refund-deploy-test-shell', success=False)
        self.assertEqual(8, result.returncode)
        self.assertNotIn('已停止项目', result.stdout)

    def test_generation_permissions_and_repeat_preserve_config(self):
        self.run_start('--check')
        initial = self.saved()
        self.assertEqual(0o600, self.config.stat().st_mode & 0o777)
        settings = dict(line.split('=', 1) for line in initial.decode().splitlines() if line and not line.startswith('#'))
        for key in ('MYSQL_PASSWORD', 'MYSQL_ROOT_PASSWORD'):
            self.assertRegex(settings[key], r'^[0-9a-f]{64}$')
        self.assertNotEqual(settings['MYSQL_PASSWORD'], settings['MYSQL_ROOT_PASSWORD'])
        self.env.update(MYSQL_PASSWORD='inherited-invalid', APP_ORIGIN='https://wrong.example', BUILD_CONTEXT='wrong')
        self.run_start('--check')
        self.run_start('--check', '--pull')
        self.assertEqual(initial, self.saved())
        self.assertNotIn(' up ', self.calls.read_text())

    def test_existing_volume_without_config_refuses_generation(self):
        self.env['TEST_EXISTING_VOLUME'] = '1'
        self.assertIn('volumes exist', self.run_start('--check', success=False).stderr)
        self.assertFalse(self.config.exists())

    def test_duplicate_and_missing_keys_refused_without_rewrite(self):
        self.run_start('--check')
        initial = self.saved()
        for broken in (initial + b'WEB_PORT=8080\n',
                       b'\n'.join(line for line in initial.splitlines() if not line.startswith(b'MYSQL_PASSWORD='))):
            self.config.write_bytes(broken)
            self.run_start('--check', success=False)
            self.assertEqual(broken, self.saved())

    def test_invalid_saved_values_are_refused_without_rewrite(self):
        self.run_start('--check')
        initial = self.saved()
        for before, after in (
            (b'BIND_IP=127.0.0.1', b'BIND_IP=999.0.0.1'),
            (b'WEB_PORT=8080', b'WEB_PORT=0'),
            (b'APP_ORIGIN=http://127.0.0.1:8080', b'APP_ORIGIN=https://example.test'),
            (b'APP_ORIGIN=http://127.0.0.1:8080', b'APP_ORIGIN=http://example.test/path'),
            (b'ENABLE_DATA_RESET=true', b'ENABLE_DATA_RESET=TRUE'),
        ):
            with self.subTest(value=after.decode()):
                broken = initial.replace(before, after)
                self.config.write_bytes(broken)
                self.run_start('--check', success=False)
                self.assertEqual(broken, self.saved())

    def test_shell_syntax_in_values_is_never_executed(self):
        self.run_start('--check')
        marker = self.base / 'must-not-exist'
        with self.config.open('a') as config:
            config.write(f'UNUSED=$(touch "{marker}")\n')
        self.run_start('--check')
        self.assertFalse(marker.exists())

    def test_changed_port_and_busy_port_refused(self):
        self.run_start('--check')
        initial = self.saved()
        self.assertIn('differs', self.run_start('--check', '--port', '18088', success=False).stderr)
        self.env['TEST_PORT_BUSY'] = '1'
        self.assertIn('occupied', self.run_start('--check', success=False).stderr)
        self.assertEqual(initial, self.saved())

    def test_remote_address_requires_origin(self):
        self.run_start('--check', '--host', '0.0.0.0', success=False)
        self.assertFalse(self.config.exists())

    def test_double_click_opens_saved_address_after_success(self):
        self.run_start('--port', '18088', wrapper=True)
        calls = self.calls.read_text()
        self.assertIn('up -d --build --wait', calls)
        self.assertIn('open http://127.0.0.1:18088', calls)
        self.assertLess(calls.index('up -d'), calls.index('open http'))

    def test_check_and_help_do_not_open_browser(self):
        self.run_start('--check', wrapper=True)
        self.run_start('--help', wrapper=True)
        self.assertNotIn('open ', self.calls.read_text())

    def test_start_failure_does_not_open_browser(self):
        self.env['TEST_UP_FAIL'] = '1'
        self.assertEqual(7, self.run_start(wrapper=True, success=False).returncode)
        self.assertNotIn('open ', self.calls.read_text())

    def test_docker_app_failure_does_not_start_services(self):
        self.env.update(TEST_ENGINE_DOWN='1', TEST_OPEN_FAIL='1')
        self.run_start(wrapper=True, success=False)
        calls = self.calls.read_text()
        self.assertIn('open -a Docker', calls)
        self.assertNotIn(' up ', calls)
        self.assertFalse(self.config.exists())

    def test_browser_failure_keeps_successful_start(self):
        self.env['TEST_OPEN_FAIL'] = '1'
        result = self.run_start(wrapper=True)
        self.assertIn('http://127.0.0.1:8080', result.stdout)


if __name__ == '__main__':
    unittest.main()
