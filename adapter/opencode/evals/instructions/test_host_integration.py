"""Opt-in OpenCode 2.3.2 test using a synthetic loopback provider only."""

import json
import os
import tempfile
import threading
import time
import unittest
from contextlib import nullcontext
from decimal import Decimal
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from . import budget, host, pilot


@unittest.skipUnless(os.environ.get("CONCORD_EVAL_OPENCODE_BIN"), "set CONCORD_EVAL_OPENCODE_BIN to the pinned binary")
class SyntheticProviderIntegrationTest(unittest.TestCase):
    def test_http_admission_steps_and_compaction_readback(self):
        class Provider(BaseHTTPRequestHandler):
            hits = []
            current_kind = None

            def log_message(self, *args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                Provider.hits.append({"path": self.path, "kind": Provider.current_kind})
                if len(Provider.hits) == 1:
                    self.send_response(429)
                    self.send_header("Retry-After", "0")
                    self.end_headers()
                    self.wfile.write(b'{"error":{"message":"synthetic rate limit","type":"rate_limit_error"}}')
                    return
                tool_step = len(Provider.hits) == 2
                delta = ({"role": "assistant", "tool_calls": [{"index": 0, "id": "call_synthetic", "type": "function",
                          "function": {"name": "fixture", "arguments": json.dumps({"action": "read_work", "arguments": {}})}}]}
                         if tool_step else {"role": "assistant", "content":
                                            "## Objective\n- Synthetic summary" if Provider.current_kind == "compaction"
                                            else "synthetic response"})
                chunks = [
                    {"id": "synthetic", "object": "chat.completion.chunk", "model": "synthetic",
                     "choices": [{"index": 0, "delta": delta,
                                  "finish_reason": None}]},
                    {"id": "synthetic", "object": "chat.completion.chunk", "model": "synthetic",
                     "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls" if tool_step else "stop"}],
                     "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}},
                ]
                data = ("".join("data: " + json.dumps(chunk) + "\n\n" for chunk in chunks) + "data: [DONE]\n\n").encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        class RecordingGuard(pilot.Guard):
            def __init__(self, *args):
                super().__init__(*args)
                self.firings = []
                self.settlements = []
                self.failed_steps = []

            def reserve(self, event):
                self.firings.append(event["kind"])
                Provider.current_kind = event["kind"]
                return super().reserve(event)

            def settle(self, message):
                self.settlements.append(message)
                return super().settle(message)

            def fail_step(self, session, subject):
                self.failed_steps.append({"sessionID": session, "assistantMessageID": subject})
                return super().fail_step(session, subject)

        class RefusingGuard(RecordingGuard):
            def reserve(self, event):
                self.firings.append(event["kind"])
                self.failed = True
                raise ValueError("synthetic refusal")

        server = ThreadingHTTPServer((host.LOOPBACK, 0), Provider)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        with nullcontext(tempfile.mkdtemp(prefix="concord-eval-diagnostic-")) as directory:
            print("diagnostic_artifacts=" + directory, flush=True)
            root = Path(directory)
            limits = {"context": 10000, "input": 9000, "output": 1000}
            rates = {"input": 2, "output": 10, "cache_read": 0.1, "cache_write": 2.5}
            profile = root / "profile.json"
            profile.write_text(json.dumps({"model": "synthetic/synthetic", "plugins": [], "ro_binds": [], "env": {},
                "providers": {"synthetic": {"name": "Synthetic", "package": "@opencode/ai/providers/openai-compatible",
                    "settings": {"baseURL": f"http://{host.LOOPBACK}:{server.server_port}/v1", "apiKey": "synthetic"},
                    "models": {"synthetic": {"name": "Synthetic", "limit": limits}}}}}))

            def new_guard(cls, name):
                location = root / name
                location.mkdir()
                journal = budget.Journal(location)
                journal.create()
                journal.update(lambda data: data["attempts"].append({"id": "attempt", "status": "pending"}))
                return cls(journal, "attempt", "synthetic/synthetic", limits, rates)

            guard = new_guard(RecordingGuard, "journal")
            snapshot = {"role": "Respond briefly.", "instructions": [], "catalog": "", "provenance": {}}
            try:
                output = host.run_session(root / "run", snapshot, "synthetic/synthetic", "S19",
                                          provider_profile=profile, guard=guard, timeout_seconds=180)
            except Exception:
                print(json.dumps({"http_request_firings": guard.firings, "provider_hits": len(Provider.hits),
                                  "provider_hit_kinds": [hit["kind"] for hit in Provider.hits],
                                  "settlements": guard.settlements, "failed_steps": guard.failed_steps,
                                  "guard_failed": guard.failed}, sort_keys=True), flush=True)
                raise
            guard.verify()
            entries = guard.journal.update(lambda data: list(data["requests"]))
            primary = sum(kind == "primary" for kind in guard.firings)
            compact = sum(kind == "compaction" for kind in guard.firings)
            step_ids = [m["id"] for m in guard.settlements if m.get("type") == "assistant"]
            compact_ids = [m["id"] for m in guard.settlements if m.get("type") == "compaction"]
            step_events = output["guard_observations"]["step_events"]
            event_ids = [event["assistantMessageID"] for event in step_events if event["type"] == "session.step.ended"]
            print(json.dumps({"http_request_firings": guard.firings, "provider_hits": len(Provider.hits),
                              "provider_hit_kinds": [hit["kind"] for hit in Provider.hits],
                              "step_events": step_events, "step_ended_subjects": event_ids,
                              "retry_events": output["guard_observations"]["retry_events"],
                              "compaction_readback_subjects": compact_ids,
                              "title_requests": sum(kind == "title" for kind in guard.firings)}, sort_keys=True), flush=True)
            self.assertTrue(output["compacted"])
            self.assertGreaterEqual(primary, 2)
            self.assertEqual(len(output["guard_observations"]["retry_events"]), 1)
            error = output["guard_observations"]["retry_events"][0]["error"]
            self.assertIsInstance(error.get("type"), str)
            self.assertEqual(error.get("status"), 429)
            self.assertLessEqual(set(error), {"type", "status"})
            self.assertEqual(entries[0]["group_id"], entries[1]["group_id"])
            self.assertEqual(Decimal(entries[0]["charge"]), budget.reservation(limits, rates))
            self.assertEqual(Decimal(entries[1]["charge"]), Decimal("0.000092"))
            self.assertGreaterEqual(compact, 1)
            self.assertEqual(len(guard.firings), len(Provider.hits))
            self.assertEqual(guard.firings, [hit["kind"] for hit in Provider.hits])
            self.assertGreaterEqual(len(step_ids), 2)
            self.assertEqual(len(step_ids), len(set(step_ids)))
            self.assertEqual(sorted(event_ids), sorted(step_ids))
            self.assertEqual(len(compact_ids), 1)
            self.assertTrue(all(entry["status"] == "settled" for entry in entries))
            self.assertEqual({entry["subject_id"] for entry in entries}, set(step_ids + compact_ids))
            self.assertGreater(sum(Decimal(entry["charge"]) for entry in entries), 0)

            blocked = new_guard(RefusingGuard, "blocked-journal")
            before = len(Provider.hits)
            started = time.monotonic()
            with self.assertRaises((ValueError, RuntimeError, OSError)):
                host.run_session(root / "blocked", snapshot, "synthetic/synthetic", "S18",
                                 provider_profile=profile, guard=blocked, timeout_seconds=180)
            print(json.dumps({"refused_http_request_firings": blocked.firings,
                              "provider_hits_before_refusal": before, "provider_hits_after_refusal": len(Provider.hits)}, sort_keys=True))
            self.assertTrue(blocked.firings)
            self.assertTrue(blocked.failed)
            self.assertLess(time.monotonic() - started, 15)
            self.assertEqual(len(Provider.hits), before)


if __name__ == "__main__":
    unittest.main()
