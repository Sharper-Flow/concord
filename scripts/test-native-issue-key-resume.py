#!/usr/bin/env python3
"""Offline safety and result-integrity checks; no host or store is started."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "native_resume", Path(__file__).with_name("native-issue-key-resume.py"))
fixture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture)


class FixtureTests(unittest.TestCase):
    def test_real_candidate_agents_and_synthetic_primary_share_one_isolated_home(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "config/opencode").mkdir(parents=True)
            (root / "home").mkdir()
            fixture.stage_agents(root)
            names = {"Concord.md", "Concord-RTE.md", "Concord-Planner.md",
                     "concord-explore.md", "concord-lookup.md", "concord-advisor.md", "concord-review.md", "concord-verify.md", "concord-implement.md", "concord-research.md", "concord-design.md"}
            self.assertEqual({p.name for p in (root / "config/opencode/agents").iterdir()},
                             names | {"fixture.md"})
            for name in names:
                self.assertEqual((root / "home/.config/opencode/agents" / name).read_bytes(),
                                 (fixture.REPO / ".opencode/agents" / name).read_bytes())
            self.assertEqual((root / "home/.config/opencode/agents/fixture.md").read_bytes(),
                             (fixture.SOURCES / "fixture.md").read_bytes())

    def test_metadata_move_alone_never_authorizes_replay(self):
        value = {"outcome": "error", "error": {"kind": "session_directory_mismatch", "retry_safe": True, "recovery_action": {"kind": "retry_same_request"}}}
        self.assertFalse(fixture.can_replay(value, "ses-real", "/target", "/origin", {"directory": "/target", "sessionID": "ses-real"}))
        self.assertFalse(fixture.can_replay(value, "ses-real", "/target", "/target", {"directory": "/origin", "sessionID": "ses-real"}))
        self.assertTrue(fixture.can_replay(value, "ses-real", "/target", "/target", {"directory": "/target", "sessionID": "ses-real"}))
        self.assertFalse(fixture.can_replay({"outcome": "error", "error": {"kind": "unreachable"}}, "ses-real", "/target", "/target", {"directory": "/target", "sessionID": "ses-real"}))
        value["error"]["recovery_action"]["kind"] = "contact_operator"
        self.assertFalse(fixture.can_replay(value, "ses-real", "/target", "/target", {"directory": "/target", "sessionID": "ses-real"}))

    def test_snapshot_requires_current_claim_and_effect_tables_and_is_read_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "synthetic.db"
            tables = ("work_items", "worktree_entries", "worktree_occupancy", "linear_issue_links", "effect_receipts", "store_sequences")
            with fixture.sqlite3.connect(db) as conn:
                for name in tables:
                    conn.execute(f'CREATE TABLE "{name}" (value TEXT)')
                    conn.execute(f'INSERT INTO "{name}" VALUES (?)', (name,))
            snapshot = fixture.snapshot(db)
            self.assertEqual(set(snapshot), set(tables))
            self.assertEqual(snapshot["effect_receipts"], [("effect_receipts",)])
            self.assertEqual(snapshot["store_sequences"], [("store_sequences",)])
            self.assertEqual(fixture.snapshot(db), snapshot)
            with patch.object(fixture.sqlite3, "connect", wraps=fixture.sqlite3.connect) as connect:
                self.assertEqual(fixture.snapshot(db), snapshot)
            connect.assert_called_once_with(db.as_uri() + "?mode=ro", uri=True)
            for table in tables:
                with self.subTest(missing=table), fixture.sqlite3.connect(db) as conn:
                    conn.execute(f'DROP TABLE "{table}"')
                    with self.assertRaises(fixture.sqlite3.OperationalError):
                        fixture.snapshot(db)
                    conn.execute(f'CREATE TABLE "{table}" (value TEXT)')

    def test_disposable_stamp_calls_real_installer_function(self):
        with tempfile.TemporaryDirectory() as tmp:
            adapter = Path(tmp) / "adapter"
            adapter.mkdir()
            constants = adapter / "generated-release.ts"
            constants.write_text("placeholder")
            fixture.stamp_adapter(adapter, Path(tmp) / "release")
            self.assertIn(str(Path(tmp) / "release/bin/concord"), constants.read_text())
            self.assertIn("DO NOT EDIT", constants.read_text())

    def test_environment_does_not_inherit_credentials_or_services(self):
        with patch.dict(os.environ, {
            "TOKEN": "private", "OPENAI_API_KEY": "private", "HTTP_PROXY": "private",
            "CONCORD_DB_PATH": "/live/store", "OPENCODE_CONFIG": "/live/config",
            "SSH_AUTH_SOCK": "/live/agent", "DBUS_SESSION_BUS_ADDRESS": "live",
        }):
            env = fixture.isolated_env(Path("/synthetic/run"), Path("/bin/opencode"))
        self.assertNotIn("private", json.dumps(env))
        self.assertNotIn("/live", json.dumps(env))
        self.assertEqual(env["OPENCODE_AUTH_CONTENT"], "{}")
        self.assertEqual(env["OPENCODE_DISABLE_PROJECT_CONFIG"], "1")
        self.assertEqual(env["CONCORD_DB_PATH"], "/synthetic/run/data/concord/concord.db")
        for key in ("HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"):
            self.assertTrue(env[key].startswith("/synthetic/run/"))

    def test_only_empty_new_nongit_root_is_accepted(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp)
            target = parent / "fresh"
            fixture.create_root(target)
            with self.assertRaises(FileExistsError):
                fixture.create_root(target)
            (parent / ".git").mkdir()
            with self.assertRaises(ValueError):
                fixture.create_root(parent / "nested")
            self.assertFalse((parent / "nested").exists())

    def test_provider_emits_native_selector_once_and_no_substitute(self):
        body = {"messages": [{"role": "user", "content": "RESUME_KEY"}]}
        self.assertEqual(fixture.provider_calls(body), [("concord_work_start", {"issue_key": "EX-3"})])
        body["messages"].append({"role": "tool", "content": "refusal"})
        self.assertEqual(fixture.provider_calls(body), [])
        self.assertEqual(fixture.provider_calls({"messages": [{"role": "user", "content": "UNKNOWN_KEY"}]}),
                         [("concord_work_start", {"issue_key": "EX-404"})])

    def test_tcp_listening_does_not_skip_application_readiness(self):
        with tempfile.TemporaryDirectory() as tmp:
            response = unittest.mock.MagicMock()
            response.__enter__.return_value.read.return_value = b'{"healthy":true,"version":"1.18.35"}'
            opener = unittest.mock.Mock()
            opener.open.side_effect = [TimeoutError("first request has no handler"), response]
            process = unittest.mock.Mock()
            process.poll.return_value = None
            fixture.wait_for_host(Path(tmp), opener, 12345, process, budget=2)
            self.assertEqual(opener.open.call_count, 2)
            self.assertTrue(all(call.args[0].full_url.endswith("/global/health") for call in opener.open.call_args_list))
            self.assertIn("first request has no handler", (Path(tmp) / "readiness.jsonl").read_text())

    def test_application_readiness_refuses_dead_host(self):
        with tempfile.TemporaryDirectory() as tmp:
            process = unittest.mock.Mock()
            process.poll.return_value = 1
            opener = unittest.mock.Mock()
            with self.assertRaisesRegex(RuntimeError, "host exited"):
                fixture.wait_for_host(Path(tmp), opener, 12345, process, budget=2)
            opener.open.assert_not_called()

    def test_refused_or_unlanded_result_cannot_be_a_pass(self):
        good = {"outcome": "ok", "work_id": "work-test", "session_id": "ses-real", "worktree_path": "/target"}
        self.assertTrue(fixture.landing_matches(good, "work-test", "ses-real", "/target", "/target", "/target", True))
        for changed in ({"outcome": "error"}, {"work_id": "other"}, {"session_id": "other"}, {"worktree_path": "/other"}):
            self.assertFalse(fixture.landing_matches({**good, **changed}, "work-test", "ses-real", "/target", "/target", "/target", True))
        self.assertFalse(fixture.landing_matches(good, "work-test", "ses-real", "/target", "/target", "/origin", True))
        self.assertFalse(fixture.landing_matches(good, "work-test", "ses-real", "/target", "/origin", "/target", True))
        self.assertFalse(fixture.landing_matches(good, "work-test", "ses-real", "/target", "/target", "/target", False))

    def test_blocked_is_not_success_and_arbitrary_error_is_not_host_blocker(self):
        self.assertEqual(fixture.classify({"outcome": "error", "error": {"kind": "unreachable", "message": "the OpenCode move-session route is absent from this host (host version 1.18.35)"}}), "BLOCKED")
        self.assertEqual(fixture.classify({"outcome": "error", "error": {"kind": "unreachable", "message": "unexplained"}}), "FAIL")
        self.assertEqual(fixture.classify({"outcome": "ok"}), "UNVERIFIED")

    def test_prompt_reads_tools_from_all_assistants_of_only_its_own_turn(self):
        final = {"info": {"parentID": "user-current", "sessionID": "session-current"}, "parts": []}
        tool = {"type": "tool", "tool": "concord_work_start", "state": {"status": "completed", "output": '{"outcome":"error"}'}}
        current = {"info": final["info"], "parts": [tool]}
        old = {"info": {"parentID": "user-old", "sessionID": "session-current"}, "parts": [tool]}
        foreign = {"info": {"parentID": "user-current", "sessionID": "session-other"}, "parts": [tool]}
        turn = fixture.turn_message(final, [old, foreign, current, final], "session-current")
        self.assertEqual(fixture.native_envelope(turn), {"outcome": "error"})
        self.assertEqual(len(turn["parts"]), 1)

    def test_native_card_without_envelope_cannot_be_proof(self):
        with self.assertRaises(RuntimeError):
            fixture.native_envelope({"parts": []})


if __name__ == "__main__":
    unittest.main()
