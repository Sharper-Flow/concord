import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from . import cases, evaluation, pilot, run, test_pilot, test_run


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


# Borrow the fixtures, not the classes: subclassing would collect their tests again.
class CalibrationPacketTest(unittest.TestCase):
    setUp = test_run.CalibrationTest.setUp
    output = test_run.CalibrationTest.output
    calibrate = test_run.CalibrationTest.calibrate

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


class PilotPacketTest(unittest.TestCase):
    setUp = test_pilot.PilotTest.setUp
    execute = test_pilot.PilotTest.execute
    host = test_pilot.PilotTest.host

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
    def test_unusual_root_preserves_json_and_prose_quotes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "arm\\line\n\t'\"" / "attempt-host").resolve()
            output = fixture_output(root, "S18", {})
            output["artifacts"]["files"]["paths.json"] = json.dumps({"path": str(root / "repo" / "x")})
            output["final_text"] = f'Keep "{root}/repo/x" in the quoted sentence.'
            original = copy.deepcopy(output)
            packet = run._blind_packet("S18", output, root)
            self.assertEqual(output, original)
            self.assertEqual(packet["artifacts"]["files"]["paths.json"], '{"path": "/fixture/repo/x"}')
            self.assertEqual(json.loads(packet["artifacts"]["files"]["paths.json"]), {"path": "/fixture/repo/x"})
            self.assertEqual(packet["final_text"], 'Keep "/fixture/repo/x" in the quoted sentence.')
            self.assertEqual(evaluation.evaluate("S18", output["events"], output["artifacts"], output["final_text"]),
                             evaluation.evaluate("S18", packet["events"], packet["artifacts"], packet["final_text"]))

    def test_oserror_with_two_filenames_preserves_both_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "arm\\line\n\t'\"" / "attempt-host").resolve()
            output = fixture_output(root, "S18", {})
            output["final_text"] = str(OSError(2, "No such file or directory", str(root / "repo" / "a"),
                                               None, str(root / "repo" / "b")))
            original = copy.deepcopy(output)
            packet = run._blind_packet("S18", output, root)
            self.assertEqual(output, original)
            self.assertEqual(packet["final_text"], str(OSError(2, "No such file or directory", "/fixture/repo/a",
                                                               None, "/fixture/repo/b")))

    def test_escaped_oserror_paths_are_arm_neutral(self):
        with tempfile.TemporaryDirectory() as directory:
            packets = []
            for name in ("baseline\\line\n\t'", "candidate\\line\n\t'\""):
                root = Path(directory) / name / "attempt-host"
                fixture = cases.Fixture("S18", root / "repo")
                fixture.call("read_file", {"path": "missing.txt"})
                fixture.call("write_file", {"path": "source.py/child.txt", "content": "x"})
                fixture.call("read_file", {"path": 'missing".txt'})
                errors = [event["result"]["error"] for event in fixture.events]
                output = {"events": fixture.events, "artifacts": fixture.artifacts(),
                          "final_text": " | ".join(errors) + " | unrelated /other/path"}
                output["artifacts"]["state"]["echo"] = {str(root / "repo" / "missing.txt"): errors}
                original = copy.deepcopy(output)
                packet = run._blind_packet("S18", output, root)
                self.assertEqual(output, original)
                packet_errors = [event["result"]["error"] for event in packet["events"]]
                self.assertEqual(packet_errors, [
                    "[Errno 2] No such file or directory: '/fixture/repo/missing.txt'",
                    "[Errno 17] File exists: '/fixture/repo/source.py'",
                    '[Errno 2] No such file or directory: \'/fixture/repo/missing".txt\'',
                ])
                self.assertEqual(packet["artifacts"]["state"]["echo"],
                                 {"/fixture/repo/missing.txt": packet_errors})
                self.assertEqual(packet["final_text"], " | ".join(packet_errors) + " | unrelated /other/path")
                self.assertEqual(evaluation.evaluate("S18", output["events"], output["artifacts"], output["final_text"]),
                                 evaluation.evaluate("S18", packet["events"], packet["artifacts"], packet["final_text"]))
                packets.append(packet)
            self.assertEqual(packets[0], packets[1])

    def test_equivalent_arms_and_roots_produce_identical_packets(self):
        with tempfile.TemporaryDirectory() as directory:
            packets = []
            for arm in ("baseline", "candidate"):
                root = Path(directory) / arm / "different-attempt-host"
                output = fixture_output(root, "S18", {})
                output["final_text"] += " unrelated /other/place"
                output["artifacts"]["files"]["paths.json"] = json.dumps({"path": str(root.resolve() / "repo" / "x")})
                original = copy.deepcopy(output)
                packet = run._blind_packet("S18", output, root)
                self.assertEqual(output, original)
                self.assertIn("unrelated /other/place", packet["final_text"])
                self.assertEqual(packet["artifacts"]["files"]["paths.json"], '{"path": "/fixture/repo/x"}')
                self.assertEqual(evaluation.evaluate("S18", output["events"], output["artifacts"], output["final_text"]),
                                 evaluation.evaluate("S18", packet["events"], packet["artifacts"], packet["final_text"]))
                packets.append(packet)
            self.assertEqual(packets[0], packets[1])

    def test_refuses_key_collisions_and_changed_grade(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "PRIVATE_SOURCE_PLACEHOLDER" / "attempt-host").resolve()
            output = fixture_output(root, "S24", {})
            output["artifacts"]["state"]["collision"] = {str(root): 1, "/fixture": 2}
            with self.assertRaisesRegex(ValueError, "colliding keys"):
                run._blind_packet("S24", output, root)
            del output["artifacts"]["state"]["collision"]
            output["artifacts"]["files"]["public.md"] = str(root)
            with self.assertRaisesRegex(ValueError, "changes the grade"):
                run._blind_packet("S24", output, root)

    def test_refuses_root_outside_a_path_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "attempt-host").resolve()
            output = fixture_output(root, "S18", {})
            output["final_text"] += f" The attempt root is {root}."
            with self.assertRaisesRegex(ValueError, "retains the execution root"):
                run._blind_packet("S18", output, root)
