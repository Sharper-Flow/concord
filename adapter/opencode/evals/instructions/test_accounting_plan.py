import copy
import unittest

from .accounting import AccountingError, aggregate
from .plan import pilot_plan


RATES = {"input": 1, "output": 2, "cache_read": 0.1, "cache_write": 0.5}


def assistant():
    return {"info": {"role": "assistant", "providerID": "example", "modelID": "model",
                     "tokens": {"input": 1000000, "output": 500000, "reasoning": 200000,
                                "cache": {"read": 300000, "write": 100000}}, "cost": 0.25}}


def snapshot(identity, concord_ref, host_ref="host-v1"):
    return {"snapshot_id": identity * 64, "provenance": {"concord_ref": concord_ref, "host_ref": host_ref}}


class AccountingTests(unittest.TestCase):
    def test_all_assistant_messages_including_summary_are_accounted(self):
        result = aggregate([assistant(), {"info": {"role": "user"}}, assistant()], "example", "model",
                           RATES, "published example rates")
        # Recorded output excludes reasoning, so reasoning adds 400000 tokens at the output rate.
        self.assertAlmostEqual(result["nominal_api_equivalent"], 4.96)
        self.assertEqual(result["totals"]["reasoning"], 400000)
        self.assertEqual(result["totals"]["host_cost"], 0.5)
        self.assertIsNone(result["subscription_billed_amount"])

    def test_fallback_errors_and_missing_usage_fail_closed(self):
        fallback = assistant()
        fallback["info"]["modelID"] = "different"
        missing = copy.deepcopy(assistant())
        del missing["info"]["tokens"]["cache"]["write"]
        errored = assistant()
        errored["info"]["error"] = {"name": "APIError", "data": {"message": "synthetic"}}
        for message in (fallback, missing, errored, {"info": None}):
            with self.subTest(message=message), self.assertRaises(AccountingError):
                aggregate([message], "example", "model", RATES, "source")


class PlanTests(unittest.TestCase):
    def test_96_sessions_paired_and_blinded(self):
        seed = 2**127 + 123
        baseline = snapshot("a", "concord-old", "host-old")
        candidate = snapshot("b", "concord-new", "host-new")
        schedule, key = pilot_plan(seed, baseline, candidate)
        self.assertEqual(schedule["sessions_per_model"], 96)
        self.assertEqual(len(schedule["rows"]), 96)
        self.assertEqual(len(key["arms"]), 96)
        self.assertNotIn("baseline", str(schedule))
        self.assertNotIn("candidate", str(schedule))
        self.assertNotIn("concord-", str(schedule))
        for index in range(0, 96, 2):
            first, second = schedule["rows"][index:index + 2]
            self.assertEqual(first["pair"], second["pair"])
            self.assertEqual({key["arms"][first["blind_id"]], key["arms"][second["blind_id"]]},
                             {"baseline", "candidate"})
        self.assertNotIn("seed", schedule)
        self.assertEqual(key["snapshots"]["candidate"]["concord_ref"], "concord-new")
        self.assertEqual(key["snapshots"]["candidate"]["host_ref"], "host-new")
        self.assertEqual((schedule, key), pilot_plan(seed, baseline, candidate))

    def test_rejects_arms_that_are_not_a_controlled_pair(self):
        seed = 2**127 + 123
        old = snapshot("a", "concord-old", "host-old")
        for candidate in (snapshot("a", "concord-new", "host-new"),
                          snapshot("b", "concord-old", "host-new"),
                          snapshot("b", "concord-new", "host-old"),
                          {"snapshot_id": "b" * 64}):
            with self.subTest(candidate=candidate), self.assertRaises(ValueError):
                pilot_plan(seed, old, candidate)
        with self.assertRaises(ValueError):
            pilot_plan(seed, {"snapshot_id": "a" * 64}, snapshot("b", "concord-new", "host-new"))
