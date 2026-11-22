import json
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from decimal import Decimal
from pathlib import Path

from . import budget


LIMITS = {"context": 350000, "input": 338000, "output": 128000}
RATES = {"input": 2, "output": 10, "cache_read": 0.1, "cache_write": 2.5}


def assistant(**changes):
    info = {"id": "assistant", "parentID": "user", "sessionID": "session", "role": "assistant", "providerID": "openai",
            "modelID": "synthetic", "time": {"completed": 1}, "finish": "stop",
            "tokens": {"input": 100, "output": 20, "reasoning": 10, "cache": {"read": 40, "write": 50}}}
    info.update(changes)
    return info


class BudgetTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.journal = budget.Journal(Path(self.tmp.name))
        self.journal.create()
        self.journal.update(lambda data: data["attempts"].append({"id": "attempt", "status": "pending"}))

    def test_reservation_is_full_context_and_output(self):
        self.assertEqual(budget.reservation(LIMITS, RATES), Decimal("2.155"))

    def test_failed_retry_reservation_remains_after_success(self):
        amount = budget.reservation(LIMITS, RATES)
        self.journal.reserve("attempt", "assistant", "user", "session", amount)
        self.journal.reserve("attempt", "assistant", "user", "session", amount)
        self.journal.settle("attempt", assistant(), "openai", "synthetic", LIMITS, RATES)
        requests = self.journal.update(lambda data: list(data["requests"]))
        self.assertEqual([r["status"] for r in requests], ["reserved", "settled"])
        self.assertEqual(requests[0]["charge"], "2.155")
        self.assertEqual(Decimal(requests[1]["charge"]), Decimal("0.000629"))

    def test_cap_cannot_be_reopened_by_reinstantiating_journal(self):
        amount = budget.reservation(LIMITS, RATES)
        for _ in range(4):
            self.journal.reserve("attempt", "assistant", "user", "session", amount)
        with self.assertRaises(ValueError):
            budget.Journal(self.journal.root).reserve("attempt", "assistant", "user", "session", amount)
        self.assertEqual(len(self.journal.update(lambda data: list(data["requests"]))), 4)

    def test_anomalous_usage_cannot_release_reservation(self):
        amount = budget.reservation(LIMITS, RATES)
        self.journal.reserve("attempt", "assistant", "user", "session", amount)
        for info in (assistant(error={"message": "error"}), assistant(error={}), assistant(modelID="other"),
                     assistant(tokens={"input": 0, "output": 1, "reasoning": 0, "cache": {"read": 0, "write": 0}}),
                     assistant(tokens={"input": 1, "output": 0, "reasoning": 0, "cache": {"read": 0, "write": 0}})):
            with self.subTest(info=info), self.assertRaises(ValueError):
                self.journal.settle("attempt", info, "openai", "synthetic", LIMITS, RATES)
        self.assertEqual(self.journal.update(lambda data: data["requests"][0]["charge"]), "2.155")

    def test_concurrent_reservations_cannot_cross_ceiling(self):
        def reserve(_):
            try:
                self.journal.reserve("attempt", "assistant", "user", "session", budget.reservation(LIMITS, RATES))
                return True
            except ValueError:
                return False
        with ThreadPoolExecutor(max_workers=8) as pool:
            admitted = list(pool.map(reserve, range(8)))
        self.assertEqual(sum(admitted), 4)
        requests = self.journal.update(lambda data: data["requests"])
        self.assertEqual(sum(Decimal(request["charge"]) for request in requests), Decimal("8.620"))


class GroupedJournalTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.journal = budget.Journal(self.root)
        self.journal.create()
        self.journal.update(lambda data: data["attempts"].append({"id": "attempt", "status": "pending"}))
        self.amount = budget.reservation(LIMITS, RATES)

    def reserve(self, group="group-1", kind="context"):
        return self.journal.reserve("attempt", "session", kind, group, self.amount)

    def settle(self, group="group-1", kind="context", info=None):
        completed = {"id": "assistant", "type": "assistant", "model": {"providerID": "openai", "id": "synthetic"},
                     "time": {"created": 1, "completed": 2}, "finish": "stop", "content": [],
                     "tokens": {"input": 100, "output": 20, "reasoning": 10, "cache": {"read": 40, "write": 50}}}
        return self.journal.settle("attempt", "session", kind, group, info or completed,
                                   "openai", "synthetic", LIMITS, RATES)

    def requests(self):
        return self.journal.update(lambda data: list(data["requests"]))

    def test_retries_settle_one_group_at_aggregate_nominal_charge(self):
        self.reserve()
        self.reserve()
        self.settle()
        data = json.loads((self.root / "pilot-journal.json").read_text())
        self.assertEqual(data["version"], 2)
        requests = data["requests"]
        self.assertEqual([r["ordinal"] for r in requests], [1, 2])
        self.assertEqual({r["group_id"] for r in requests}, {"group-1"})
        self.assertEqual({r["session_id"] for r in requests}, {"session"})
        self.assertEqual({r["kind"] for r in requests}, {"context"})
        self.assertEqual({r["subject_id"] for r in requests}, {"assistant"})
        self.assertTrue(all(r["status"] == "settled" for r in requests))
        self.assertEqual(sum(Decimal(r["charge"]) for r in requests), Decimal("0.000629"))

    def test_no_mixed_kind_or_new_group_before_settlement(self):
        self.reserve()
        for group, kind in (("group-1", "compaction"), ("group-2", "context")):
            with self.subTest(group=group, kind=kind), self.assertRaises(ValueError):
                self.reserve(group, kind)
        self.assertEqual(len(self.requests()), 1)
        self.settle()
        self.reserve("group-2", "compaction")

    def test_settlement_without_matching_group_fails_closed(self):
        with self.assertRaises(ValueError):
            self.settle()
        self.reserve()
        for group, kind in (("group-2", "context"), ("group-1", "compaction")):
            with self.subTest(group=group, kind=kind), self.assertRaises(ValueError):
                self.settle(group, kind)
        self.assertEqual(self.requests()[0]["status"], "reserved")

    def test_charge_above_group_reservations_is_refused(self):
        self.journal.reserve("attempt", "session", "context", "group-1", Decimal("0.000001"))
        with self.assertRaises(ValueError):
            self.settle()
        self.assertEqual(self.requests()[0]["charge"], "0.000001")

    def test_version_one_journal_is_refused(self):
        path = self.root / "pilot-journal.json"
        data = json.loads(path.read_text())
        data["version"] = 1
        path.write_text(json.dumps(data))
        with self.assertRaises(ValueError):
            self.journal.update(lambda data: None)


if __name__ == "__main__":
    unittest.main()
