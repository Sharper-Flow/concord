#!/usr/bin/env python3
import copy
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
REPORT_SCHEMA = json.loads((ROOT / "contracts/agent-lane-report.schema.json").read_text(encoding="utf-8"))


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
        self.assertIn("\nhidden: true\n", generator.agent_projection(self.LANE, REPORT_SCHEMA))

    def test_projection_stays_selectable_by_run_mode(self):
        # CD-0070 D2. Run mode refuses a subagent-mode target and substitutes
        # the default agent, so CD-0064 D1's mode survives the hidden flag.
        self.assertIn("\nmode: all\n", generator.agent_projection(self.LANE, REPORT_SCHEMA))

    def test_projection_denies_task_dispatch(self):
        # CD-0070 Invariant 3, carrying CD-0064 Invariant 3 forward.
        self.assertIn('"*": deny', generator.agent_projection(self.LANE, REPORT_SCHEMA))

    def test_projection_reads_report_bounds_from_schema(self):
        changed = copy.deepcopy(REPORT_SCHEMA)
        changed["properties"]["readback_model"]["maxLength"] = 77
        changed["properties"]["evidence"]["maxItems"] = 11
        changed["$defs"]["evidence_entry"]["properties"]["detail"]["maxLength"] = 23
        projection = generator.agent_projection(self.LANE, changed)
        self.assertIn("maxLength=77", projection)
        self.assertIn("maxItems=11", projection)
        self.assertIn("maxLength=23", projection)

    def test_projection_instructs_the_lane_to_refuse_a_non_packet_first_message(self):
        # CD-0102 heuristic control: with no adapter plugin nothing
        # adapter-side runs, so every generated lane definition must itself
        # refuse a first message that is not a well-formed packet and return
        # the report with status failed.
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        self.assertIn("verify the first message you received", projection)
        self.assertIn("`agent-lane-packet.v1` packet", projection)
        for field in ("schema_version", "attempt_id", "lane_id", "lane_version", "lane_digest", "work_id", "step_id", "inputs"):
            self.assertIn(f"`{field}`", projection)
        self.assertIn("`status` `failed`", projection)

    def test_projection_does_not_require_a_worker_cwd_readback(self):
        # The dispatch window pins the worker directory, and the adapter
        # composes the admitted report from the authorized packet, so the
        # lane definition must not ask the worker for a `pwd` readback.
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        self.assertNotIn("pwd", projection)
        self.assertNotIn("cwd", projection)

    def test_utility_projection_projects_declared_tools_and_permissions(self):
        utility = {
            "id": "ci-wait",
            "purpose": "Wait for CI.",
            "allowed_tools": ["bash"],
            "allowed_commands": ["concord ci-wait", "concord ci-wait *"],
            "time_seconds_max": 1800,
        }
        projection = generator.utility_projection(utility)
        self.assertIn("mode: all", projection)
        self.assertIn("  bash: true", projection)
        self.assertIn("  read: false", projection)
        self.assertIn("  task: false", projection)
        self.assertIn('"*": deny', projection)
        self.assertIn('"concord ci-wait": allow', projection)
        self.assertIn('"concord ci-wait *": allow', projection)
        self.assertIn("30 minutes", projection)

    def test_ci_wait_projection_delegates_the_wait_to_the_verb(self):
        # CD-0160. The wait is enforced by the concord ci-wait verb, not by
        # this prompt: the body must not ask the model to poll, sleep, or
        # count iterations.
        utility = {
            "id": "ci-wait",
            "purpose": "Wait for CI.",
            "allowed_tools": ["bash"],
            "allowed_commands": ["concord ci-wait"],
            "time_seconds_max": 1800,
        }
        projection = generator.utility_projection(utility)
        self.assertIn("concord ci-wait <<'EOF'", projection)
        self.assertIn("`state_file`", projection)
        self.assertNotIn("sleep 15", projection)
        self.assertNotIn("Count your iterations", projection)

    def test_exploration_projection_uses_read_only_tools_and_body(self):
        utility = {
            "id": "explore",
            "purpose": "Inspect a repository.",
            "allowed_tools": ["bash", "read", "glob", "grep", "execute"],
            "allowed_commands": ["git status *"],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        self.assertIn("  bash: true", projection)
        self.assertIn("  read: true", projection)
        self.assertIn("  glob: true", projection)
        self.assertIn("  grep: true", projection)
        self.assertIn("  execute: true", projection)
        self.assertIn("  edit: false", projection)
        self.assertIn("# concord-explore", projection)
        self.assertIn("Do not edit files", projection)
        self.assertIn("Object.keys(tools)", projection)
        self.assertIn("tools.lgrep.search_semantic", projection)
        self.assertIn("MCP access depends on host connections", projection)
        self.assertIn("Use read-only tools only", projection)

    def test_advisory_projection_uses_collaborative_body_with_model_statement(self):
        # CD-0157 D2-D4. The adviser receives a problem, never a proposed
        # answer; it reaches the repository tools; it may report no concerns;
        # it names the model that served it; and each finding carries a path,
        # a line range, or a command.
        utility = {
            "id": "advisor",
            "purpose": "Give an independent reasoned opinion.",
            "allowed_tools": ["bash", "read", "glob", "grep", "execute"],
            "allowed_commands": ["git status *"],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        self.assertIn("# concord-advisor", projection)
        self.assertIn("  execute: true", projection)
        self.assertIn("  webfetch: false", projection)
        self.assertIn("  edit: false", projection)
        self.assertIn("preferred option", projection)
        self.assertIn("No concerns", projection)
        self.assertIn("model that served this opinion", projection)
        self.assertIn("line range", projection)
        self.assertIn("10 minutes", projection)


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


class WorkerScopeProjectionTests(unittest.TestCase):
    # The projection fixture carries only the fields worker_scope_assignments
    # reads, so the test states the resolution rule rather than the registry.
    MANIFEST = {
        "lanes": [
            {"id": "research", "evidence_obligations": ["source_citations", "bounded_findings"]},
            {"id": "implement", "evidence_obligations": ["files_touched"]},
        ]
    }

    @staticmethod
    def contract(*entries):
        return {"assignments": list(entries)}

    def test_assignments_resolve_one_result_per_registered_lane(self):
        assignments = generator.worker_scope_assignments(self.contract(
            {"lane_id": "research", "result": "bounded_findings"},
            {"lane_id": "implement", "result": "files_touched"},
        ), self.MANIFEST)
        self.assertEqual(assignments, {"research": "bounded_findings", "implement": "files_touched"})

    def test_assignment_of_unregistered_lane_fails_generation(self):
        with self.assertRaises(ValueError):
            generator.worker_scope_assignments(self.contract({"lane_id": "ghost", "result": "files_touched"}), self.MANIFEST)

    def test_assignment_of_undeclared_result_fails_generation(self):
        with self.assertRaises(ValueError):
            generator.worker_scope_assignments(self.contract({"lane_id": "implement", "result": "severity"}), self.MANIFEST)

    def test_missing_lane_assignment_fails_generation(self):
        with self.assertRaises(ValueError):
            generator.worker_scope_assignments(self.contract({"lane_id": "implement", "result": "files_touched"}), self.MANIFEST)

    def test_duplicate_lane_assignment_fails_generation(self):
        with self.assertRaises(ValueError):
            generator.worker_scope_assignments(self.contract(
                {"lane_id": "implement", "result": "files_touched"},
                {"lane_id": "implement", "result": "files_touched"},
            ), self.MANIFEST)

    def test_ts_projection_emits_the_assigned_result_surface(self):
        projection = generator.ts_projection(
            {"lanes": self.MANIFEST["lanes"], "utilities": []},
            "sha256:" + "0" * 64,
            {},
            {},
            {"research": "bounded_findings", "implement": "files_touched"},
        )
        self.assertIn('"research": "bounded_findings"', projection)
        self.assertIn('"implement": "files_touched"', projection)
        self.assertIn("export function workerScopeAssignedResult", projection)


if __name__ == "__main__":
    unittest.main()
