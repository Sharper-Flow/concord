#!/usr/bin/env python3
"""Exercise census resource admission with synthetic commands only."""

import importlib.util
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("census", Path(__file__).with_name("release-restore-census.py"))
census = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(census)
GROUP = "/user.slice/user-1000.slice/user@1000.service/app.slice/oc-run-" + "a" * 64 + ".service"
UNIT = GROUP.rsplit("/", 1)[1]


class CensusTest(unittest.TestCase):
    def test_refuses_outside_original_run_before_starting_anything(self):
        with patch.object(Path, "read_text", return_value="0::/user.slice/session-1.scope\n"), patch.object(census.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "oc-run"):
                census.bound_job()
            run.assert_not_called()

    def test_refuses_mismatched_unit_before_changing_it(self):
        with patch.object(Path, "read_text", return_value="0::" + GROUP + "\n"), patch.object(census.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout="/other\n")) as run:
            with self.assertRaisesRegex(ValueError, "cgroup"):
                census.bound_job()
            self.assertEqual(run.call_count, 1)

    def test_bounds_original_unit_and_checks_kernel_limits(self):
        values = iter(["0::" + GROUP + "\n", str(3 << 30), str(4 << 30), "0"])
        with patch.object(Path, "read_text", side_effect=lambda **_: next(values)), patch.object(census.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout=GROUP + "\n")) as run:
            census.bound_job()
            args = run.call_args_list[1].args[0]
            self.assertEqual(args[:5], ["systemctl", "--user", "set-property", "--runtime", UNIT])
            for setting in ("MemoryHigh=3G", "MemoryMax=4G", "MemorySwapMax=0"):
                self.assertIn(setting, args)

    def test_unenforced_limit_refuses_work(self):
        values = iter(["0::" + GROUP + "\n", "max"])
        with patch.object(Path, "read_text", side_effect=lambda **_: next(values)), patch.object(census.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout=GROUP + "\n")):
            with self.assertRaisesRegex(ValueError, "memory.high"):
                census.bound_job()

    def test_failed_systemd_admission_does_not_launch_command(self):
        with patch.object(census, "bound_job", side_effect=subprocess.CalledProcessError(1, "systemctl")), patch.object(census.subprocess, "run") as run:
            with self.assertRaises(subprocess.CalledProcessError):
                census.run_census(["fixture-inventory"])
            run.assert_not_called()

    def test_retries_only_incomplete_batches_under_heavy_admission(self):
        with patch.object(census, "bound_job") as bound, patch.object(census.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)) as run:
            self.assertEqual(census.run_census(["fixture-inventory", "-batch", "1"]), 0)
            bound.assert_called_once()
            self.assertEqual(run.call_args.args[0][:6], ["oc-test-gate", "full", "--timeout", "20m", "--", census.sys.executable])
            self.assertEqual(run.call_args.args[0][7:], ["--batch-loop", "fixture-inventory", "-batch", "1"])
        with patch.object(census.subprocess, "run", side_effect=[subprocess.CompletedProcess([], n) for n in (3, 3, 0)]) as run:
            self.assertEqual(census.run_batches(["fixture-inventory", "-batch", "1"]), 0)
            self.assertEqual(run.call_count, 3)
            for call in run.call_args_list:
                self.assertEqual(call.args[0], ["fixture-inventory", "-batch", "1"])

    def test_failure_timeout_and_signal_are_not_success(self):
        for status in (1, 3, 75, 124, 137, -9):
            with self.subTest(status=status), patch.object(census, "bound_job"), patch.object(census.subprocess, "run", return_value=subprocess.CompletedProcess([], status)) as run:
                self.assertEqual(census.run_census(["fixture-inventory"]), 128 - status if status < 0 else status)
                run.assert_called_once()

    def test_batch_failure_is_final(self):
        for status in (1, 75, 124, 137, -9):
            with self.subTest(status=status), patch.object(census.subprocess, "run", return_value=subprocess.CompletedProcess([], status)) as run:
                self.assertEqual(census.run_batches(["fixture-inventory"]), census.exit_status(status))
                run.assert_called_once()


if __name__ == "__main__":
    unittest.main()
