"""Pure collector regressions; never invoke Go or the hosted shard."""

from contextlib import ExitStack
import errno
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


HELPER = Path(__file__).resolve().parents[1] / ".github/diagnostics/con-1610.py"
SPEC = importlib.util.spec_from_file_location("wal_diagnosis", HELPER)
DIAGNOSIS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DIAGNOSIS)
V2 = "/sys/fs/cgroup/cpu.max"
V1_QUOTA = "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"
V1_PERIOD = "/sys/fs/cgroup/cpu/cpu.cfs_period_us"


class RuntimeCollectorTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "diagnosis").mkdir()
        self.files = {}
        self.job = {
            "id": 2, "html_url": "https://example.invalid/job/2", "run_id": 1,
            "run_attempt": 1, "head_sha": "a" * 40, "name": "wal-diagnosis",
            "status": "in_progress", "started_at": "1970-01-01T00:16:00Z",
        }
        stack = ExitStack()
        self.addCleanup(stack.close)
        environment = dict(DIAGNOSIS.POISON, GITHUB_RUN_ID="1", GITHUB_SHA="a" * 40)
        stack.enter_context(patch.dict(DIAGNOSIS.os.environ, environment, clear=True))
        self.command = stack.enter_context(patch.object(DIAGNOSIS, "command", side_effect=self.fake_command))
        stack.enter_context(patch.object(DIAGNOSIS, "urlopen", side_effect=self.fake_jobs))
        stack.enter_context(patch.object(DIAGNOSIS.time, "time", return_value=1000))
        stack.enter_context(patch.object(DIAGNOSIS.os, "cpu_count", return_value=2))
        self.affinity = stack.enter_context(patch.object(DIAGNOSIS.os, "sched_getaffinity", return_value={0, 1}))

    def fake_command(self, cwd, *argv):
        if argv[:2] == ("go", "env"):
            return json.dumps({"GOVERSION": "go1.26.10", "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1"}).encode()
        return {
            ("bun", "--version"): b"1.4.0\n",
            ("go", "version"): b"go version go1.26.10 linux/amd64\n",
            ("go", "clean", "-testcache"): b"",
        }[argv]

    def fake_jobs(self, request, timeout):
        self.assertEqual(request.full_url, "https://api.github.com/repos/Sharper-Flow/concord/actions/runs/1/attempts/1/jobs")
        self.assertEqual(timeout, 15)
        return io.BytesIO(json.dumps({"total_count": 1, "jobs": [self.job]}).encode())

    def read_probe(self, path, *args, **kwargs):
        value = self.files.get(str(path), FileNotFoundError(errno.ENOENT, "missing"))
        if isinstance(value, Exception):
            raise value
        return value

    def collect(self):
        with patch.object(Path, "read_text", lambda path: self.read_probe(path)), patch("builtins.print"):
            DIAGNOSIS.record_runtime(self.root)
        return json.loads((self.root / "diagnosis/runtime.json").read_text())

    def test_missing_cpu_max_does_not_prevent_runtime_record(self):
        result = self.collect()
        self.assertEqual(result["cpu_max"]["status"], "unavailable")
        self.assertIn("FileNotFoundError", result["cpu_max"]["reason"])
        self.assertNotIn("value", result["cpu_max"])
        self.assertEqual(result["test_cache_expiration_command"], ["go", "clean", "-testcache"])

    def test_v1_only_records_raw_observation_not_v2_unlimited(self):
        self.files = {V1_QUOTA: "200000\n", V1_PERIOD: "100000\n"}
        result = self.collect()
        self.assertEqual(result["cpu_max"]["status"], "unavailable")
        self.assertEqual(result["cpu_v1_quota_us"], {"status": "observed", "value": "200000"})
        self.assertEqual(result["cpu_v1_period_us"], {"status": "observed", "value": "100000"})

    def test_denied_optional_data_is_explicitly_unavailable(self):
        self.files = {path: PermissionError(errno.EACCES, "denied") for path in (V2, V1_QUOTA, V1_PERIOD)}
        self.affinity.side_effect = PermissionError(errno.EPERM, "denied")
        result = self.collect()
        for key in ("cpu_max", "cpu_v1_quota_us", "cpu_v1_period_us", "cpu_affinity"):
            self.assertEqual(result[key]["status"], "unavailable")
            self.assertIn("PermissionError", result[key]["reason"])
            self.assertNotIn("value", result[key])

    def test_v2_raw_value_does_not_claim_effective_cpu_capacity(self):
        self.files = {V2: "max 100000\n"}
        result = self.collect()
        self.assertEqual(result["cpu_max"], {"status": "observed", "value": "max 100000"})
        self.assertEqual(result["cpu_affinity"], {"status": "observed", "value": [0, 1]})

    def test_absent_or_empty_observation_is_not_zero_or_unlimited(self):
        self.files = {V2: "\n"}
        with patch.object(DIAGNOSIS.os, "cpu_count", return_value=None):
            result = self.collect()
        for key in ("cpu_count", "cpu_max"):
            self.assertEqual(result[key]["status"], "unavailable")
            self.assertNotIn("value", result[key])

    def test_wrong_poison_remains_mandatory_refusal(self):
        with patch.dict(DIAGNOSIS.os.environ, {"CONCORD_DB_PATH": "wrong"}):
            with self.assertRaisesRegex(SystemExit, "wrong poison declaration"):
                self.collect()
        self.command.assert_not_called()
        self.assertFalse((self.root / "diagnosis/runtime.json").exists())

    def test_expired_budget_remains_mandatory_refusal(self):
        self.job["started_at"] = "1970-01-01T00:00:00Z"
        with self.assertRaisesRegex(SystemExit, "setup exceeded 90s"):
            self.collect()
        self.assertFalse((self.root / "diagnosis/runtime.json").exists())

    def test_source_selection_hash_error_remains_mandatory_refusal(self):
        with patch.object(Path, "read_bytes", return_value=b"not frozen\n"):
            with self.assertRaisesRegex(SystemExit, "frozen names hash mismatch"):
                DIAGNOSIS.inputs(self.root)
        self.command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
