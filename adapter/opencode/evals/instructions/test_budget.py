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


if __name__ == "__main__":
    unittest.main()
