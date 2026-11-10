import json
import io
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch

from . import cases, run, snapshots


class CalibrationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        role = self.root / "role.txt"
        role.write_text("Role instructions", encoding="utf-8")
        self.snapshot = self.root / "snapshot.json"
        snapshots.freeze(self.snapshot, self.repo, role_file=role, instruction_files=[],
                         concord_ref="concord-ref", host_ref="host-ref")
        self.rates = self.root / "rates.json"
        self.rates.write_text(json.dumps({"source": "published rates", "rates": {
            "input": 1, "output": 2, "cache_read": 0.5, "cache_write": 1.5}}))
        self.artifacts = self.root / "artifacts"

    def output(self):
        def message(input_tokens, output_tokens, summary=False):
            return {"info": {"role": "assistant", "providerID": "provider", "modelID": "model",
                             "summary": summary, "tokens": {"input": input_tokens, "output": output_tokens,
                             "reasoning": 2, "cache": {"read": 4, "write": 6}}, "cost": 0.1}}
        return {"messages": [message(100, 20), message(30, 10, True)], "events": [],
                "artifacts": {"files": dict(cases.FILES), "state": {}}, "final_text": "3 seconds",
                "host_version": "1.18.35", "identity": {"providerID": "provider", "modelID": "model"},
                "capture_paths": ["capture"], "capture_stage": "pre-provider-serialization",
                "compacted": False, "compaction_index": None}

    def calibrate(self, case_ids=("S18",), **kwargs):
        return run.calibrate(self.snapshot, self.artifacts, "provider/model", list(case_ids), self.rates,
                             repository=self.repo, **kwargs)

    def test_six_attempt_limit_across_calls_and_blind_packet(self):
        with patch.object(run.host, "run_session", return_value=self.output()) as session:
            first = self.calibrate(["S18"] * 4)
            self.calibrate(["S18"] * 2)
            with self.assertRaises(ValueError):
                self.calibrate()
            self.assertEqual(session.call_count, 6)
        packet = json.loads((self.artifacts / (first[0]["attempt_id"] + "-blind.json")).read_text())
        self.assertEqual(set(packet), {"case", "events", "artifacts", "final_text", "semantic_checks"})
        self.assertEqual(packet["semantic_checks"], ["correct_comparison_and_units"])
        self.assertEqual(first[0]["grade"]["semantic_pending"], packet["semantic_checks"])

    def test_failure_charges_slot_without_retry(self):
        with patch.object(run.host, "run_session", side_effect=RuntimeError("private secret")) as session:
            result = self.calibrate()[0]
            self.assertEqual(session.call_count, 1)
        self.assertEqual(result["status"], "failed")
        self.assertNotIn("private secret", json.dumps(result))
        ledger = json.loads((self.artifacts / "calibration-ledger.json").read_text())
        self.assertEqual([item["status"] for item in ledger["attempts"]], ["failed"])
        with patch.object(run.host, "run_session", return_value=self.output()):
            with self.assertRaises(ValueError):
                self.calibrate(["S18"] * 6)

    def test_all_messages_accounted_and_missing_usage_fails(self):
        with patch.object(run.host, "run_session", return_value=self.output()):
            result = self.calibrate()[0]
        self.assertEqual(result["usage"]["assistant_messages"], 2)
        for key in ("host_version", "identity", "capture_paths", "capture_stage", "compacted", "compaction_index"):
            self.assertEqual(result[key], self.output()[key])
        self.assertEqual(set(result["hashes_before"]), {"evaluator", "cases", "host", "accounting", "snapshots", "run", "plan"})
        self.assertEqual(result["usage"]["totals"]["input"], 130)
        self.assertAlmostEqual(result["usage"]["nominal_api_equivalent"], 0.00022)
        output = self.output()
        del output["messages"][1]["info"]["tokens"]
        with patch.object(run.host, "run_session", return_value=output):
            result = self.calibrate()[0]
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["failure"]["type"], "AccountingError")

    def test_changed_snapshot_invalidates_attempt(self):
        def change(*args, **kwargs):
            self.snapshot.chmod(0o600)
            self.snapshot.write_text("{}")
            return self.output()
        with patch.object(run.host, "run_session", side_effect=change):
            result = self.calibrate()[0]
        self.assertEqual(result["status"], "failed")
        self.assertIsNone(result["snapshot_id_after"])

    def test_changed_code_invalidates_attempt(self):
        hashes = {"evaluator": "a", "cases": "b", "host": "c"}
        changed = {**hashes, "host": "changed"}
        with patch.object(run, "_hashes", side_effect=[hashes, changed, changed]), \
             patch.object(run.host, "run_session", return_value=self.output()):
            result = self.calibrate()[0]
        self.assertEqual(result["status"], "failed")
        self.assertNotEqual(result["hashes_before"], result["hashes_after"])

    def test_invalid_requests_never_claim_or_call_host(self):
        with patch.object(run.host, "run_session") as session:
            with self.assertRaises(ValueError):
                run.calibrate(self.snapshot, self.repo / "artifacts", "provider/model", ["S18"],
                              self.rates, repository=self.repo)
            self.rates.write_text('{"rates": {}, "source": "source"}')
            with self.assertRaises(ValueError):
                self.calibrate()
            self.assertFalse(self.artifacts.exists())
            session.assert_not_called()

    def test_estimate_cli_reconciles_completed_and_pending_claims(self):
        with patch.object(run.host, "run_session", return_value=self.output()):
            completed = self.calibrate()[0]
        pending = run._claim(self.artifacts, "S18")
        output = io.StringIO()
        with redirect_stdout(output):
            status = run.main(["estimate", "--artifacts", str(self.artifacts), "--proposed-cap", "1"])
        projection = json.loads(output.getvalue())
        self.assertEqual(status, 0)
        self.assertEqual(projection["status"], "incomplete")
        self.assertEqual(projection["missing_attempt_ids"], [pending])
        self.assertAlmostEqual(projection["observed_known_nominal_subtotal"], completed["usage"]["nominal_api_equivalent"])
        self.assertTrue(projection["unsampled_families"])

    def test_estimate_cli_rejects_invalid_cap(self):
        self.artifacts.mkdir()
        (self.artifacts / "calibration-ledger.json").write_text('{"version": 1, "attempts": []}')
        stderr = io.StringIO()
        with redirect_stderr(stderr), self.assertRaises(SystemExit) as error:
            run.main(["estimate", "--artifacts", str(self.artifacts), "--proposed-cap", "0"])
        self.assertEqual(error.exception.code, 2)
        self.assertIn("proposed_cap must be finite and positive", stderr.getvalue())

    def test_estimate_rejects_mixed_identity_and_pricing(self):
        with patch.object(run.host, "run_session", return_value=self.output()):
            first, second = self.calibrate(["S18", "S18"])
        path = self.artifacts / (second["attempt_id"] + "-result.json")
        projection = run.estimate_artifacts(self.artifacts, 1, repository=self.repo)
        self.assertAlmostEqual(projection["observed_known_nominal_subtotal"], 0.00044)
        for field, value in (("providerID", "other-provider"), ("modelID", "other-model"),
                             ("pricing_source", "other rates")):
            changed = json.loads(json.dumps(second))
            target = changed["usage"] if field == "pricing_source" else changed["identity"]
            target[field] = value
            path.write_text(json.dumps(changed))
            with self.subTest(field=field), self.assertRaises(ValueError):
                run.estimate_artifacts(self.artifacts, 1, repository=self.repo)

    def test_estimate_cli_does_not_expose_json_content_or_paths(self):
        self.artifacts.mkdir()
        (self.artifacts / "calibration-ledger.json").write_text('{"private secret":')
        stderr = io.StringIO()
        with redirect_stderr(stderr), self.assertRaises(SystemExit):
            run.main(["estimate", "--artifacts", str(self.artifacts), "--proposed-cap", "1"])
        self.assertNotIn("private secret", stderr.getvalue())
        self.assertNotIn(str(self.artifacts), stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
