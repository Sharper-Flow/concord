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

    def test_encoded_fixture_error_refuses_without_packet(self):
        self.artifacts = self.root / "calibration\\path"
        outputs = []
        def host(root, snapshot, model, case_id, **kwargs):
            output = fixture_output(root, case_id, self.output())
            outputs.append((output, copy.deepcopy(output)))
            return output
        with patch.object(run.host, "run_session", side_effect=host):
            result = self.calibrate()[0]
        self.assertEqual(outputs[0][0], outputs[0][1])
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["failure"]["type"], "ValueError")
        self.assertIn("normalize", [frame["function"] for frame in result["failure"]["traceback"]])
        self.assertFalse((self.artifacts / (result["attempt_id"] + "-blind.json")).exists())


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

    def test_encoded_fixture_error_refuses_without_packet(self):
        self.artifacts = self.root / "pilot\\path"
        outputs = []
        def host(root, snapshot, model, case_id, *, guard, **kwargs):
            output = self.host(root, snapshot, model, case_id, guard=guard, **kwargs)
            output = fixture_output(root, case_id, output)
            outputs.append((output, copy.deepcopy(output)))
            return output
        with patch.object(pilot.host, "run_session", side_effect=host), patch.object(run, "_blind_packet", wraps=run._blind_packet) as build:
            result = self.execute(max_new_attempts=1)[0]
        build.assert_called_once()
        self.assertEqual(outputs[0][0], outputs[0][1])
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["failure"], "ValueError")
        self.assertFalse((self.artifacts / (result["attempt_id"] + "-blind.json")).exists())


class PacketNeutralityTest(unittest.TestCase):
    def test_unusual_root_raw_prose_preserves_quotes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "arm\\line\n\t'\"" / "0123456789abcdef0123456789abcdef-host").resolve()
            output = {"events": [], "artifacts": {}, "final_text": f'Keep "{root}/repo/x" in the quoted sentence.'}
            original = copy.deepcopy(output)
            packet = run._blind_packet("S18", output, root)
            self.assertEqual(output, original)
            self.assertEqual(packet["final_text"], 'Keep "/fixture/repo/x" in the quoted sentence.')
            self.assertEqual(evaluation.evaluate("S18", output["events"], output["artifacts"], output["final_text"]),
                             evaluation.evaluate("S18", packet["events"], packet["artifacts"], packet["final_text"]))

    def test_encoded_oserror_with_two_filenames_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "arm\\line\n\t'\"" / "0123456789abcdef0123456789abcdef-host").resolve()
            output = {"events": [], "artifacts": {}}
            output["final_text"] = str(OSError(2, "No such file or directory", str(root / "repo" / "a"),
                                               None, str(root / "repo" / "b")))
            original = copy.deepcopy(output)
            with self.assertRaisesRegex(ValueError, "retains the execution root"):
                run._blind_packet("S18", output, root)
            self.assertEqual(output, original)

    def test_escaped_oserror_paths_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            for name in ("baseline\\line\n\t'", "candidate\\line\n\t'\""):
                root = Path(directory) / name / "0123456789abcdef0123456789abcdef-host"
                output = fixture_output(root, "S18", {})
                original = copy.deepcopy(output)
                with self.subTest(name=name), self.assertRaisesRegex(ValueError, "retains the execution root"):
                    run._blind_packet("S18", output, root)
                self.assertEqual(output, original)

    def test_json_of_repr_with_attempt_token_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "arm\\path" / "0123456789abcdef0123456789abcdef-host").resolve()
            output = {"events": [], "artifacts": {"state": {"echo": json.dumps(repr(str(root / "repo" / "x")))}},
                      "final_text": "done"}
            original = copy.deepcopy(output)
            with self.assertRaisesRegex(ValueError, "retains the execution root"):
                run._blind_packet("S18", output, root)
            self.assertEqual(output, original)

    def test_json_encoded_oserror_with_quoted_filename_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / 'arm"path' / "0123456789abcdef0123456789abcdef-host").resolve()
            error = str(OSError(2, "No such file or directory", str(root / "repo" / "file's.txt")))
            encoded = json.dumps({"error": error})
            output = {"events": [], "artifacts": {"files": {"error.json": encoded}}, "final_text": "done"}
            original = copy.deepcopy(output)
            with self.assertRaisesRegex(ValueError, "retains the execution root"):
                run._blind_packet("S18", output, root)
            self.assertEqual(output, original)
            self.assertEqual(json.loads(output["artifacts"]["files"]["error.json"]), {"error": error})

    def test_standalone_attempt_token_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "0123456789abcdef0123456789abcdef-host").resolve()
            output = {"events": [], "artifacts": {}, "final_text": "attempt " + root.name}
            with self.assertRaisesRegex(ValueError, "retains the execution root"):
                run._blind_packet("S18", output, root)

    def test_json_encoded_oserror_preserves_quotes_for_raw_root(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "0123456789abcdef0123456789abcdef-host").resolve()
            error = str(OSError(2, "No such file or directory", str(root / "repo" / "file's.txt")))
            output = {"events": [], "artifacts": {"files": {"error.json": json.dumps({"error": error})}},
                      "final_text": "done"}
            original = copy.deepcopy(output)
            packet = run._blind_packet("S18", output, root)
            self.assertEqual(output, original)
            self.assertEqual(json.loads(packet["artifacts"]["files"]["error.json"]),
                             {"error": str(OSError(2, "No such file or directory", "/fixture/repo/file's.txt"))})

    def test_equivalent_arms_and_roots_produce_identical_packets(self):
        with tempfile.TemporaryDirectory() as directory:
            packets = []
            for arm, token in (("baseline", "0123456789abcdef0123456789abcdef-host"),
                               ("candidate", "fedcba9876543210fedcba9876543210-host")):
                root = Path(directory) / arm / token
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
            root = (Path(directory) / "PRIVATE_SOURCE_PLACEHOLDER" / "0123456789abcdef0123456789abcdef-host").resolve()
            output = fixture_output(root, "S24", {})
            output["artifacts"]["state"]["collision"] = {str(root): 1, "/fixture": 2}
            with self.assertRaisesRegex(ValueError, "colliding keys"):
                run._blind_packet("S24", output, root)
            del output["artifacts"]["state"]["collision"]
            output["artifacts"]["files"]["public.md"] = str(root)
            with self.assertRaisesRegex(ValueError, "changes the grade"):
                run._blind_packet("S24", output, root)

    def test_normalizes_root_followed_by_punctuation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = (Path(directory) / "0123456789abcdef0123456789abcdef-host").resolve()
            output = fixture_output(root, "S18", {})
            output["final_text"] += f" The attempt root is {root}."
            packet = run._blind_packet("S18", output, root)
            self.assertTrue(packet["final_text"].endswith(" The attempt root is /fixture."))
