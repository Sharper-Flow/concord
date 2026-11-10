import unittest

from .cases import CASES
from .estimate import estimate


def result(attempt_id, case_id, cost, seconds):
    return {"attempt_id": attempt_id, "case_id": case_id, "status": "completed",
            "elapsed_seconds": seconds,
            "usage": {"nominal_api_equivalent": cost, "totals": {"input": 100, "host_cost": 0},
                      "pricing_source": "https://example.org/rates", "currency": "USD"}}


def samples():
    entries = [result("a", "S01", 2, 20), result("b", "S02", 4, 40)]
    seen = {"REF"}
    for case_id, case in CASES.items():
        if case["family"] not in seen:
            seen.add(case["family"])
            entries.append(result(case_id, case_id, 1, 10))
    return entries


class EstimateTest(unittest.TestCase):
    def test_balanced_pilot_worked_example(self):
        output = estimate(samples(), proposed_cap=384)
        self.assertEqual(output["status"], "complete")
        self.assertEqual(output["expected_nominal"], 112)
        self.assertEqual(output["max_observed_nominal"], 384)
        self.assertEqual(output["expected_seconds"], 1120)
        self.assertEqual(output["max_observed_seconds"], 3840)
        self.assertEqual(output["families"]["REF"]["expected_nominal"], 24)
        self.assertEqual(output["families"]["REF"]["observed_cost_range"], [2, 4])
        self.assertEqual(output["cases"]["S04"]["sample_basis"], "family")
        self.assertEqual(output["cases"]["S04"]["expected_nominal"], 4)
        self.assertEqual(output["currency"], "USD")
        self.assertEqual(output["pricing_sources"], ["https://example.org/rates"])
        self.assertIn("not mechanically enforced", output["cap_notice"])
        self.assertIn("undo the responsible merge", output["regression_policy"])

    def test_failed_and_ledger_only_attempts_are_incomplete(self):
        failed = result("failed", "S01", 3, 10)
        failed["status"] = "failed"
        ledger = {"id": "pending", "case_id": "S02", "status": "pending"}
        output = estimate(samples() + [failed, ledger], proposed_cap=384)
        self.assertEqual(output["status"], "incomplete")
        self.assertEqual(output["missing_attempt_ids"], ["failed", "pending"])
        self.assertEqual(output["observed_known_nominal_subtotal"], 16)
        self.assertIsNone(output["expected_nominal"])
        self.assertEqual(output["max_observed_nominal"], 384)
        self.assertEqual(output["provisional_expected_nominal"], 112)

    def test_unsampled_families_are_inconclusive(self):
        output = estimate([result("one", "S01", 1, 3)], proposed_cap=96)
        self.assertEqual(output["status"], "incomplete")
        self.assertIn("ASK", output["unsampled_families"])
        self.assertEqual(output["families"]["ASK"]["status"], "inconclusive")
        self.assertIsNone(output["families"]["ASK"]["expected_nominal"])
        self.assertIsNone(output["expected_nominal"])
        self.assertEqual(output["provisional_expected_nominal"], 96)
        self.assertEqual(output["provisional_expected_seconds"], 288)
        self.assertEqual(output["cases"]["S18"]["sample_basis"], "unsampled_family_bound")
        self.assertEqual(output["max_observed_nominal"], 96)

    def test_invalid_caps_and_observed_maximum(self):
        for cap in (-1, 0, float("nan"), float("inf"), -float("inf"), 383, True):
            with self.subTest(cap=cap), self.assertRaises(ValueError):
                estimate(samples(), proposed_cap=cap)

    def test_missing_usage_and_time_are_incomplete(self):
        no_usage = result("no-usage", "S01", 1, 1)
        del no_usage["usage"]
        no_time = result("no-time", "S02", 2, 1)
        del no_time["elapsed_seconds"]
        output = estimate([no_usage, no_time], proposed_cap=1000)
        self.assertEqual(output["missing_attempt_ids"], ["no-usage", "no-time"])
        self.assertEqual(output["observed_known_nominal_subtotal"], 2)
        self.assertIsNone(output["expected_nominal"])

    def test_currency_cannot_be_mixed(self):
        entries = samples()
        entries[0]["usage"]["currency"] = "EUR"
        with self.assertRaises(ValueError):
            estimate(entries, proposed_cap=1000)


if __name__ == "__main__":
    unittest.main()
