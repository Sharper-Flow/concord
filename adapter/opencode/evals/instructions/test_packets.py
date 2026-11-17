import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from . import cases, evaluation, pilot, run
from .test_pilot import PilotTest
from .test_run import CalibrationTest


def fixture_output(root, case_id, output):
    fixture = cases.Fixture(case_id, Path(root).resolve() / "repo")
    if cases.CASES[case_id]["family"] == "HOLD":
        fixture.mark_compacted(cases.CASES[case_id]["continuation_prompt"])
    fixture.call("read_file", {"path": "missing.txt"})
    fixture.call("write_file", {"path": "source.py/child.txt", "content": "x"})
    errors = [event["result"]["error"] for event in fixture.events if "action" in event]
    output.update(events=fixture.events, artifacts=fixture.artifacts(), final_text="; ".join(errors))
    output["artifacts"]["state"]["nested"] = {str(Path(root).resolve() / "repo" / "missing.txt"): [errors[0], 3, None, True]}
    output["artifacts"]["files"]["echo.txt"] = errors[1]
    return output


def assert_packet(test, result, raw, root, directory):
    test.assertEqual(result["status"], "completed")
    packet = json.loads((directory / (result["attempt_id"] + "-blind.json")).read_text())
    test.assertNotIn(str(Path(root).resolve()), json.dumps(packet))
    events = [event for event in packet["events"] if "action" in event]
    test.assertEqual(events[0]["arguments"], {"path": "missing.txt"})
    test.assertEqual(events[0]["result"], {"ok": False, "error": "[Errno 2] No such file or directory: '/fixture/repo/missing.txt'"})
    test.assertEqual(events[1]["result"], {"ok": False, "error": "[Errno 17] File exists: '/fixture/repo/source.py'"})
    test.assertEqual(packet["artifacts"]["state"]["nested"]["/fixture/repo/missing.txt"],
                     [events[0]["result"]["error"], 3, None, True])
    test.assertEqual(packet["artifacts"]["files"]["echo.txt"], events[1]["result"]["error"])
    test.assertIn(events[0]["result"]["error"], packet["final_text"])
    test.assertEqual(result["grade"], evaluation.evaluate(packet["case"], packet["events"], packet["artifacts"], packet["final_text"]))
    test.assertEqual(result["grade"], evaluation.evaluate(packet["case"], raw["events"], raw["artifacts"], raw["final_text"]))
    return [event["result"] for event in events]


class CalibrationPacketTest(CalibrationTest):
    def test_packet_normalizes_real_fixture_errors_without_mutating_host_output(self):
        outputs = []
        def host(root, snapshot, model, case_id, **kwargs):
            output = fixture_output(root, case_id, self.output())
            outputs.append((root, output, copy.deepcopy(output)))
            return output
        with patch.object(run.host, "run_session", side_effect=host):
            results = self.calibrate(["S18", "S18"])
        packets = []
        for result, (root, output, original) in zip(results, outputs):
            self.assertEqual(output, original)
            packets.append(assert_packet(self, result, original, root, self.artifacts))
        self.assertEqual(packets[0], packets[1])


class PilotPacketTest(PilotTest):
    def test_packet_normalizes_real_fixture_errors_without_mutating_host_output(self):
        outputs = []
        def host(root, snapshot, model, case_id, *, guard, **kwargs):
            output = self.host(root, snapshot, model, case_id, guard=guard, **kwargs)
            output = fixture_output(root, case_id, output)
            outputs.append((root, output, copy.deepcopy(output)))
            return output
        with patch.object(pilot.host, "run_session", side_effect=host):
            results = self.execute(max_new_attempts=1)
        root, output, original = outputs[0]
        self.assertEqual(output, original)
        assert_packet(self, results[0], original, root, self.artifacts)


class PacketNeutralityTest(unittest.TestCase):
    def test_equivalent_arms_and_roots_produce_identical_packets(self):
        with tempfile.TemporaryDirectory() as directory:
            packets = []
            for arm in ("baseline", "candidate"):
                root = Path(directory) / arm / "different-attempt-host"
                output = fixture_output(root, "S18", {})
                output["final_text"] += " unrelated /other/place"
                original = copy.deepcopy(output)
                packet = run._blind_packet("S18", output, root)
                self.assertEqual(output, original)
                self.assertIn("unrelated /other/place", packet["final_text"])
                self.assertEqual(evaluation.evaluate("S18", output["events"], output["artifacts"], output["final_text"]),
                                 evaluation.evaluate("S18", packet["events"], packet["artifacts"], packet["final_text"]))
                packets.append(packet)
            self.assertEqual(packets[0], packets[1])

    def test_refuses_key_collisions_and_changed_grade(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "PRIVATE_SOURCE_PLACEHOLDER" / "attempt-host"
            output = fixture_output(root, "S24", {})
            output["artifacts"]["state"]["collision"] = {str(root): 1, "/fixture": 2}
            with self.assertRaisesRegex(ValueError, "colliding keys"):
                run._blind_packet("S24", output, root)
            del output["artifacts"]["state"]["collision"]
            output["artifacts"]["files"]["public.md"] = str(root)
            with self.assertRaisesRegex(ValueError, "changes the grade"):
                run._blind_packet("S24", output, root)
