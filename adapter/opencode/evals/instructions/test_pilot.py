import json
import io
import tempfile
import unittest
from contextlib import redirect_stdout
from decimal import Decimal
from pathlib import Path
from unittest.mock import patch

from . import budget, pilot, run, snapshots
from .test_budget import LIMITS, RATES, assistant


class PilotTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.baseline = self.root / "baseline.json"
        self.candidate = self.root / "candidate.json"
        for name, path in (("old", self.baseline), ("new", self.candidate)):
            source = self.root / (name + ".md")
            source.write_text(name)
            snapshots.freeze(path, self.repo, role_file=source, instruction_files=[], concord_ref=name, host_ref=name)
        self.rates = self.root / "rates.json"
        self.rates.write_text(json.dumps({"source": "synthetic", "rates": RATES}))
        self.limits = self.root / "limits.json"
        self.limits.write_text(json.dumps({"model": "openai/synthetic", "source": "synthetic", "limits": LIMITS}))
        self.artifacts = self.root / "pilot"

    def execute(self, **kwargs):
        return pilot.execute(self.baseline, self.candidate, self.artifacts, "openai/synthetic",
                             self.limits, self.rates, repository=self.repo, **kwargs)

    def host(self, root, snapshot, model, case_id, *, guard, **kwargs):
        if (self.artifacts / "one-success").exists():
            raise RuntimeError("synthetic failure")
        (self.artifacts / "one-success").touch()
        guard.reserve({"sessionID": "session", "message": {"id": "user"},
                       "assistant": {"id": "assistant", "sessionID": "session", "parentID": "user",
                                     "providerID": "openai", "modelID": "synthetic", "time": {}},
                       "model": {"providerID": "openai", "id": "synthetic", "limit": LIMITS}})
        guard.settle(assistant())
        return {"events": [], "artifacts": {}, "final_text": "synthetic", "identity": {"modelID": "synthetic"}}

    def test_schedule_claims_and_resume_do_not_repeat_rows(self):
        with patch.object(pilot.host, "run_session", side_effect=self.host), patch.object(pilot.evaluation, "evaluate", return_value={}):
            first = self.execute()
            second = self.execute(max_new_attempts=2)
        self.assertEqual([r["status"] for r in first], ["completed", "failed"])
        self.assertEqual(second, [])
        self.assertEqual(len({r["blind_id"] for r in first}), 2)
        schedule = json.loads((self.artifacts / "pilot-schedule.json").read_text())
        key = json.loads((self.artifacts / "pilot-key.json").read_text())
        journal = json.loads((self.artifacts / "pilot-journal.json").read_text())
        self.assertEqual(len(schedule["rows"]), 96)
        self.assertEqual(len(key["arms"]), 96)
        self.assertEqual(len(journal["attempts"]), 2)
        self.assertEqual(journal["requests"][0]["status"], "settled")

    def test_pending_claim_stops_a_later_batch(self):
        with patch.object(pilot.host, "run_session", side_effect=RuntimeError("synthetic")):
            self.execute(max_new_attempts=1)
        journal = budget.Journal(self.artifacts)
        journal.update(lambda data: data["attempts"][0].update(status="pending"))
        with patch.object(pilot.host, "run_session") as session:
            self.assertEqual(self.execute(max_new_attempts=2), [])
            session.assert_not_called()
        self.assertEqual(len(journal.update(lambda data: data["attempts"])), 1)

    def test_resume_rejects_missing_journal(self):
        with patch.object(pilot.host, "run_session", side_effect=RuntimeError("synthetic")):
            self.execute()
            (self.artifacts / "pilot-journal.json").unlink()
            with self.assertRaises(FileNotFoundError):
                self.execute()

    def test_batches_resume_without_changing_pilot_identity(self):
        def successful_host(root, snapshot, model, case_id, *, guard, **kwargs):
            guard.reserve({"sessionID": "session", "message": {"id": "user"},
                           "assistant": {"id": "assistant", "sessionID": "session", "parentID": "user",
                                         "providerID": "openai", "modelID": "synthetic", "time": {}},
                           "model": {"providerID": "openai", "id": "synthetic", "limit": LIMITS}})
            guard.settle(assistant())
            return {"events": [], "artifacts": {}, "final_text": "synthetic", "identity": {"modelID": "synthetic"}}

        def batch(size):
            return pilot.execute(self.baseline, self.candidate, self.artifacts, "openai/synthetic",
                                 self.limits, self.rates, max_new_attempts=size, repository=self.repo)
        with patch.object(pilot.host, "run_session", side_effect=successful_host), patch.object(pilot.evaluation, "evaluate", return_value={}):
            first = batch(2)
            paths = [self.artifacts / name for name in ("pilot-metadata.json", "pilot-schedule.json", "pilot-key.json")]
            originals = [path.read_bytes() for path in paths]
            first_requests = json.loads((self.artifacts / "pilot-journal.json").read_text())["requests"]
            second = batch(1)
            third = batch(2)
        self.assertEqual([len(first), len(second), len(third)], [2, 1, 2])
        self.assertEqual([path.read_bytes() for path in paths], originals)
        rows = json.loads(paths[1].read_text())["rows"]
        journal = json.loads((self.artifacts / "pilot-journal.json").read_text())
        attempts = journal["attempts"]
        self.assertEqual([entry["blind_id"] for entry in attempts], [row["blind_id"] for row in rows[:5]])
        self.assertTrue(all(entry["status"] == "completed" for entry in attempts))
        self.assertEqual(len({entry["id"] for entry in attempts}), 5)
        self.assertEqual(journal["requests"][:2], first_requests)
        self.assertEqual(len(journal["requests"]), 5)

    def test_isolated_model_limits_reserve_full_context_and_output(self):
        limits = {"context": 1050000, "input": 922000, "output": 128000}
        self.assertEqual(budget.reservation(limits, RATES), Decimal("3.905"))

    def test_invalid_batch_limit_refused_before_pilot_creation(self):
        for value in (0, -1, True, 1.5, "2"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                pilot.execute(self.baseline, self.candidate, self.artifacts, "openai/synthetic",
                              self.limits, self.rates, max_new_attempts=value, repository=self.repo)
        self.assertFalse(self.artifacts.exists())

    def test_cli_batch_reports_pilot_incomplete(self):
        def successful_host(root, snapshot, model, case_id, *, guard, **kwargs):
            guard.reserve({"sessionID": "session", "message": {"id": "user"},
                           "assistant": {"id": "assistant", "sessionID": "session", "parentID": "user",
                                         "providerID": "openai", "modelID": "synthetic", "time": {}},
                           "model": {"providerID": "openai", "id": "synthetic", "limit": LIMITS}})
            guard.settle(assistant())
            return {"events": [], "artifacts": {}, "final_text": "synthetic", "identity": {"modelID": "synthetic"}}
        output = io.StringIO()
        with patch.object(pilot.host, "run_session", side_effect=successful_host), patch.object(pilot.evaluation, "evaluate", return_value={}), redirect_stdout(output):
            status = run.main(["pilot", "--baseline", str(self.baseline), "--candidate", str(self.candidate),
                               "--artifacts", str(self.artifacts), "--model", "openai/synthetic",
                               "--limits-json", str(self.limits), "--rates-json", str(self.rates), "--max-new-attempts", "2"])
        result = json.loads(output.getvalue())
        self.assertEqual(status, 1)
        self.assertFalse(result["complete"])
        self.assertEqual([entry["status"] for entry in result["attempts"]], ["completed", "completed"])

    def test_model_mismatch_latches_guard_closed(self):
        journal = budget.Journal(self.root)
        journal.create()
        journal.update(lambda data: data["attempts"].append({"id": "attempt", "status": "pending"}))
        guard = pilot.Guard(journal, "attempt", "openai/synthetic", LIMITS, RATES)
        request = {"sessionID": "session", "message": {"id": "user"},
                   "assistant": {"id": "assistant", "sessionID": "session", "parentID": "user",
                                 "providerID": "openai", "modelID": "synthetic", "time": {}},
                   "model": {"providerID": "openai", "id": "other", "limit": LIMITS}}
        with self.assertRaises(ValueError):
            guard.reserve(request)
        request["model"]["id"] = "synthetic"
        with self.assertRaises(ValueError):
            guard.reserve(request)
        self.assertEqual(journal.update(lambda data: data["requests"]), [])


if __name__ == "__main__":
    unittest.main()
