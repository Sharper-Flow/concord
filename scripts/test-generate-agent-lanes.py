#!/usr/bin/env python3
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("lane_generator", ROOT / "scripts/generate-agent-lanes.py")
if spec is None or spec.loader is None:
    raise RuntimeError("unable to load lane generator")
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)


class AgentProjectionTests(unittest.TestCase):
    LANE = {
        "id": "review",
        "purpose": "Review a bounded change against its contract.",
        "evidence_obligations": ["findings", "verdict"],
    }

    def test_projection_hides_the_lane_from_the_operator_agent_cycle(self):
        # CD-0070 D1. The host cycles every agent whose mode is not subagent
        # and whose hidden flag is unset, so mode alone cannot keep a worker
        # lane out of the operator's session-agent cycle.
        self.assertIn("\nhidden: true\n", generator.agent_projection(self.LANE))

    def test_projection_stays_selectable_by_run_mode(self):
        # CD-0070 D2. Run mode refuses a subagent-mode target and substitutes
        # the default agent, so CD-0064 D1's mode survives the hidden flag.
        self.assertIn("\nmode: all\n", generator.agent_projection(self.LANE))

    def test_projection_denies_task_dispatch(self):
        # CD-0070 Invariant 3, carrying CD-0064 Invariant 3 forward.
        self.assertIn('"*": deny', generator.agent_projection(self.LANE))


class EvalPacketProjectionTests(unittest.TestCase):
    def test_projection_replaces_lane_digest_without_manual_edit(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "packet.json"
            path.write_text(json.dumps({"lane_id": "review", "lane_digest": "old"}, indent=2) + "\n", encoding="utf-8")
            projected = generator.eval_packet_projection(path, {"review": "sha256:" + "a" * 64})
            self.assertIn('"lane_digest": "sha256:' + "a" * 64 + '"', projected)

    def test_projection_rejects_unknown_lane(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "packet.json"
            path.write_text('{"lane_id":"missing"}\n', encoding="utf-8")
            with self.assertRaises(ValueError):
                generator.eval_packet_projection(path, {})


if __name__ == "__main__":
    unittest.main()
