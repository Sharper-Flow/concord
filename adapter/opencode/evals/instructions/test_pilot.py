import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from . import budget, pilot, snapshots
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

    def execute(self):
        return pilot.execute(self.baseline, self.candidate, self.artifacts, "openai/synthetic",
                             self.limits, self.rates, repository=self.repo)

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
            second = self.execute()
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

    def test_resume_rejects_missing_journal(self):
        with patch.object(pilot.host, "run_session", side_effect=RuntimeError("synthetic")):
            self.execute()
            (self.artifacts / "pilot-journal.json").unlink()
            with self.assertRaises(FileNotFoundError):
                self.execute()

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
