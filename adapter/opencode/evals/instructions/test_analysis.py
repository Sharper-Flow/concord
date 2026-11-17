import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from . import analysis, cases, plan, run


def _snapshot(identity, ref):
    return {"snapshot_id": identity * 64, "provenance": {"concord_ref": ref, "host_ref": "host-" + ref}}


class AnalysisTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.pilot = self.root / "pilot"
        self.pilot.mkdir()
        self.review_path = self.root / "reviews.json"
        self.lock_path = self.root / "lock.json"
        self.schedule, self.key = plan.pilot_plan((1 << 255) | 12345, _snapshot("a", "old"), _snapshot("b", "new"))
        self.metadata = {"version": 1, "model": "provider/model", "cap_usd": "10", "hashes": run._hashes(),
                         "snapshots": {"baseline": "a" * 64, "candidate": "b" * 64}}
        self.attempts = []
        self.reviews = {"reviewer": "independent reviewer", "independent_blind": True, "reviews": {}}

    def write(self, path, value):
        path.write_text(json.dumps(value))

    def attempt(self, case_id, repetition, arm, *, status="completed", shell=False):
        blind = next(row["blind_id"] for row in self.schedule["rows"]
                     if row["case_id"] == case_id and row["repetition"] == repetition and self.key["arms"][row["blind_id"]] == arm)
        attempt = "attempt" + blind
        self.attempts.append({"id": attempt, "blind_id": blind, "status": status})
        if status != "completed":
            return
        events = [{"action": "update_status", "result": {"ok": True}}]
        if shell:
            events.append({"action": "shell"})
        packet = {"case": case_id, "events": events, "artifacts": {"files": cases.FILES, "state": {"status": "in_progress"}},
                  "final_text": "done", "semantic_checks": cases.CASES[case_id]["semantic_checks"]}
        path = self.pilot / (attempt + "-blind.json")
        self.write(path, packet)
        self.write(self.pilot / (attempt + "-result.json"),
                   {"attempt_id": attempt, "blind_id": blind, "case_id": case_id, "status": "completed"})
        self.reviews["reviews"][blind] = {"packet_sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                                          "semantic_checks": {name: {"verified": True, "passed": True} for name in packet["semantic_checks"]},
                                          "severity_note": "Reviewed against packet."}

    def save(self):
        self.write(self.pilot / "pilot-schedule.json", self.schedule)
        self.write(self.pilot / "pilot-metadata.json", self.metadata)
        self.write(self.pilot / "pilot-journal.json", {"version": 1, "attempts": self.attempts,
                   "requests": [{"status": "settled", "charge": "0.13"}, {"status": "reserved", "charge": "0.40"}]})
        self.write(self.pilot / "pilot-key.json", self.key)
        self.write(self.review_path, self.reviews)

    def lock(self):
        return analysis.lock(self.pilot, self.review_path, self.lock_path, repository=self.repo)

    def report(self):
        return analysis.report(self.pilot, self.lock_path, repository=self.repo)

    def test_incomplete_pilot_reports_inconclusive_pairs_safety_and_costs(self):
        self.attempt("S03", 1, "baseline")
        self.attempt("S03", 1, "candidate", shell=True)
        self.attempt("S03", 2, "baseline")
        self.attempt("S05", 1, "baseline", status="failed")
        self.save()
        self.lock()
        result = self.report()
        name = cases.CASES["S03"]["family"]
        pairs = 2 * sum(case["family"] == name for case in cases.CASES.values())
        family = result["families"][name]
        self.assertEqual((family["complete_pairs"], family["dropped_incomplete_pairs"]), (1, pairs - 1))
        self.assertEqual(family["arms"]["baseline"]["passed"], 2)
        self.assertEqual(family["arms"]["baseline"]["ungraded"], pairs - 2)
        self.assertEqual(family["arms"]["candidate"]["severity"]["S3"], 1)
        self.assertEqual(family["paired_pass_difference"]["difference"], -1)
        self.assertEqual(family["paired_pass_difference"]["exact_p"], 1)
        self.assertTrue(family["inconclusive"])
        self.assertTrue(family["material_regression_requires_attribution"])
        self.assertTrue(result["safety_blocked"])
        self.assertFalse(result["pilot"]["complete"])
        self.assertEqual(result["pilot"]["attempts"], {"completed": 3, "failed": 1, "pending": 0, "unclaimed": 92})
        self.assertEqual(result["pilot"]["snapshots"], self.metadata["snapshots"])
        self.assertEqual(result["costs"], {"cap_usd": "10", "settled_nominal_usd": "0.13", "unresolved_reservations_usd": "0.40",
                                           "unresolved_requests": 1, "total_charged_usd": "0.53"})

    def test_paired_statistics(self):
        self.assertEqual(analysis._paired(0, 5, 6)["exact_p"], 0.0625)
        self.assertAlmostEqual(analysis._wilson(0, 10)[1], 0.27753, places=5)
        tied = analysis._paired(1, 1, 6)
        self.assertEqual((tied["difference"], tied["exact_p"]), (0, 1))
        self.assertLess(tied["ci_95"][0], 0)
        self.assertGreater(tied["ci_95"][1], 0)

    def test_lock_rejects_pending_and_wrong_packet(self):
        self.attempt("S05", 1, "baseline")
        self.attempt("S05", 1, "candidate")
        self.save()
        review = next(iter(self.reviews["reviews"].values()))
        name = next(iter(review["semantic_checks"]))
        review["semantic_checks"][name]["verified"] = False
        self.write(self.review_path, self.reviews)
        with self.assertRaises(ValueError):
            self.lock()
        review["semantic_checks"][name]["verified"] = True
        review["packet_sha256"] = "bad"
        self.write(self.review_path, self.reviews)
        with self.assertRaises(ValueError):
            self.lock()

    def test_lock_precedes_key_read_and_is_write_once(self):
        self.attempt("S03", 1, "baseline")
        self.attempt("S03", 1, "candidate")
        self.save()
        (self.pilot / "pilot-key.json").unlink()
        self.lock()
        with self.assertRaises(FileExistsError):
            self.lock()
        # A failed lock check refuses before the missing key is opened.
        (self.pilot / ("attempt" + self.attempts[0]["blind_id"] + "-blind.json")).write_text("{}")
        with self.assertRaises(ValueError):
            self.report()

    def test_changed_pilot_files_and_key_are_rejected(self):
        self.attempt("S03", 1, "baseline")
        self.attempt("S03", 1, "candidate")
        self.save()
        self.lock()
        journal = self.pilot / "pilot-journal.json"
        original = journal.read_bytes()
        journal.write_bytes(original + b" ")
        with self.assertRaises(ValueError):
            self.report()
        journal.write_bytes(original)
        swapped = dict(self.key, arms={blind: "candidate" if arm == "baseline" else "baseline"
                                       for blind, arm in self.key["arms"].items()})
        self.write(self.pilot / "pilot-key.json", swapped)
        with self.assertRaises(ValueError):
            self.report()
        self.write(self.pilot / "pilot-key.json", self.key)
        self.report()

    def test_grading_harness_must_match_pilot(self):
        self.metadata["hashes"] = dict(self.metadata["hashes"], evaluator="0" * 64)
        self.attempt("S03", 1, "baseline")
        self.save()
        with self.assertRaises(ValueError):
            self.lock()

    def test_private_paths_required(self):
        with self.assertRaises(ValueError):
            analysis.lock(self.repo / "pilot", self.review_path, self.lock_path, repository=self.repo)


if __name__ == "__main__":
    unittest.main()
