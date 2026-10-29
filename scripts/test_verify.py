"""Offline runner guard/report regression tests. Never starts Docker or writes business data."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from deployment_acceptance import Acceptance, SafeFailure
from verify import CHECKLIST, Failure, Runner, UI_REQUIRED, assert_plain_path, checklist, clean_env, parse_frontend, parse_go


def frontend_report():
    names = list(UI_REQUIRED) + ["loads orders on the first real login without a page refresh (randomUUID available: true)"]
    return {"success": True, "testResults": [{"assertionResults": [{"title": name, "status": "passed"} for name in names]}]}


def go_report():
    return [{"Action": "pass", "Package": "test/events", "Test": "TestKafkaRedisRealDuplicateDelivery"},
            {"Action": "pass", "Package": "test/events"}]


class ReportTests(unittest.TestCase):
    def test_go_pass_and_real_middleware_assertion_required(self):
        self.assertEqual(1, parse_go("\n".join(map(json.dumps, go_report())))["passed_including_subtests"])
        rows = go_report()
        rows[0]["Test"] = "TestFake"
        with self.assertRaises(Failure):
            parse_go("\n".join(map(json.dumps, rows)))

    def test_go_skip_failure_empty_or_incomplete_is_not_pass(self):
        for rows in ([], go_report()[:-1], [{**go_report()[0], "Action": "skip"}, go_report()[1]],
                     [go_report()[0], {**go_report()[1], "Action": "fail"}]):
            with self.subTest(rows=rows), self.assertRaises(Failure):
                parse_go("\n".join(map(json.dumps, rows)))

    def test_frontend_pass_is_component_not_browser(self):
        result = parse_frontend(json.dumps(frontend_report()))
        self.assertEqual(0, result["skipped"])
        self.assertIn("not live browser E2E", result["environment"])

    def test_frontend_failure_skip_empty_and_missing_cases_rejected(self):
        cases = [dict(success=False), dict(success=True, testResults=[])]
        for status in ("failed", "pending", "todo", "skipped"):
            row = frontend_report()
            row["testResults"][0]["assertionResults"][0]["status"] = status
            cases.append(row)
        missing = frontend_report()
        missing["testResults"][0]["assertionResults"].pop()
        cases.append(missing)
        for row in cases:
            with self.subTest(row=row), self.assertRaises(Failure):
                parse_frontend(json.dumps(row))

    def test_unexecuted_checklist_is_not_pass(self):
        self.assertTrue(all(r["status"] == "NOT_RUN" for r in checklist({})))

    def test_complete_checklist_requires_frontend_and_go(self):
        records = [{"name": key, "passed": True} for _, keys, _ in CHECKLIST for key in keys]
        report = {"deployment": {"checks": records}, "frontend": {"passed": 1}, "go": {"passed": 1}}
        self.assertEqual(["PASS"] * 9, [r["status"] for r in checklist(report)])
        report.pop("frontend")
        self.assertEqual("NOT_RUN", checklist(report)[0]["status"])
        self.assertEqual("NOT_RUN", checklist(report)[7]["status"])

    def test_repeated_assertion_failure_cannot_be_hidden_by_later_pass(self):
        name = CHECKLIST[0][1][0]
        report = {"deployment": {"checks": [{"name": name, "passed": False}, {"name": name, "passed": True}]}}
        self.assertEqual("FAIL", checklist(report)[0]["status"])

    def test_frontend_failed_phase_is_fail_not_not_run(self):
        self.assertEqual("FAIL", checklist({"steps": [{"name": "前端组件", "status": "FAIL"}]})[7]["status"])


class SafetyTests(unittest.TestCase):
    def test_inherited_compose_and_test_overrides_are_removed(self):
        with patch.dict(os.environ, {"APP_ORIGIN": "https://example.invalid", "COMPOSE_FILE": "other.yaml",
                                     "TEST_MYSQL_DSN": "not-a-test-db", "BUILD_CONTEXT": "other"}):
            env = clean_env()
        self.assertTrue(all(k not in env for k in ("APP_ORIGIN", "COMPOSE_FILE", "TEST_MYSQL_DSN", "BUILD_CONTEXT")))
        self.assertEqual("fixture", clean_env({"TEST_MYSQL_DSN": "fixture"})["TEST_MYSQL_DSN"])

    def test_generated_project_is_reserved_unique_and_has_no_target_argument(self):
        first, second = Runner(), Runner()
        self.assertRegex(first.project, r"^refund-deploy-test-[a-z0-9][a-z0-9-]{0,39}$")
        self.assertNotEqual(first.project, second.project)
        self.assertTrue(first.source.is_relative_to(first.source.parents[2] / ".runtime"))

    def test_linked_runtime_refused_without_following_it(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "runtime"
            with patch.object(Path, "is_symlink", lambda p: p == path):
                with self.assertRaises(Failure):
                    assert_plain_path(path / "child")

    def test_remote_docker_refused_before_info_or_any_resource_creation(self):
        runner = Runner()
        with patch("verify.shutil.which", return_value="fixture"), patch.dict(os.environ, {"DOCKER_HOST": ""}), \
                patch.object(runner, "command", side_effect=[("fixture\n", 0), ("ssh://example.invalid\n", 0)]) as command:
            with self.assertRaisesRegex(Failure, "remote-docker"):
                runner.preflight()
        self.assertEqual(2, command.call_count)

    def test_existing_project_refused(self):
        runner = Runner()
        with patch("verify.shutil.which", return_value="fixture"), patch.dict(os.environ, {"DOCKER_HOST": ""}), \
                patch.object(runner, "command", side_effect=[("fixture", 0), ("unix:///fixture", 0), ("1", 0), ("v2", 0), ("existing", 0)]):
            with self.assertRaisesRegex(Failure, "project-already-exists"):
                runner.preflight()

    def test_container_cleanup_refuses_wrong_owner(self):
        runner = Runner()
        with patch.object(runner, "command", return_value=("other-project", 0)) as command:
            with self.assertRaisesRegex(Failure, "owner-mismatch"):
                runner.owned_container("a" * 64)
        self.assertEqual(1, command.call_count)

    def test_stop_never_removes_volumes_or_stops_a_foreign_project(self):
        runner = Runner()
        with patch.object(runner, "command", side_effect=[("abc", 0), ("other-project", 0)]) as command:
            with self.assertRaisesRegex(Failure, "owner-mismatch"):
                runner.stop_own_stack()
        self.assertFalse(any("stop" in args[0] or "rm" in args[0] for args, _ in command.call_args_list))

    def test_sql_inspection_does_not_migrate_and_has_canonical_order(self):
        suite = Acceptance("refund-deploy-test-fixture", "unused.env", "http://127.0.0.1:1")
        result = {"orders": [{"order_id": "B"}, {"order_id": "A"}], "refunds": [], "channel_requests": []}
        with patch.object(suite, "sql", return_value=result) as sql:
            self.assertEqual("A", suite.inspect_business()["orders"][0]["order_id"])
        query = sql.call_args.args[0]
        self.assertTrue(query.startswith("SELECT JSON_OBJECT"))
        for forbidden in ("INSERT", "DELETE", "UPDATE", "CREATE", "DROP", "TRUNCATE"):
            self.assertNotIn(forbidden, query)

    def test_sql_refuses_unverified_target(self):
        suite = Acceptance("refund-deploy-test-fixture", "unused.env", "http://127.0.0.1:1")
        with self.assertRaisesRegex(SafeFailure, "topology-not-verified"):
            suite.inspect_business()

    def test_generated_passwords_are_redacted(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = Runner()
            runner.env_file = Path(directory) / "private.env"
            password = "synthetic-only-not-real"
            runner.env_file.write_text("MYSQL_PASSWORD=" + password + "\n", encoding="utf-8")
            self.assertNotIn(password, runner.redact("failure " + password))


if __name__ == "__main__":
    unittest.main(verbosity=2)
