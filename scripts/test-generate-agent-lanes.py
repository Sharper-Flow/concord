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
        "budgets": {"time_seconds_max": 1200},
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

    def test_projection_denies_every_concord_tool(self):
        # CD-0017 D4, extended by CD-0196. The tool ids come from the two
        # tool-surface contracts, so the check reads them from there too.
        ids = generator.concord_tool_ids()
        self.assertIn("concord_work_start", ids)
        self.assertIn("concord_work_transition", ids)
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        for tool_id in ids:
            self.assertIn(f"\n  {tool_id}: false\n", projection)
        self.assertIn("## Concord context boundary", projection)

    def test_every_installed_agent_definition_denies_every_concord_tool(self):
        # generate-agent-lanes.py --check keeps these files equal to the
        # projections, so this covers every lane and utility the manifest declares.
        definitions = sorted((ROOT / ".opencode/agents").glob("concord-*.md"))
        self.assertTrue(definitions)
        for path in definitions:
            text = path.read_text(encoding="utf-8")
            for tool_id in generator.concord_tool_ids():
                self.assertIn(f"\n  {tool_id}: false\n", text, f"{path.name} does not deny {tool_id}")

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

    def test_projection_states_the_predicate_tie_rule(self):
        # The store refuses a completed worker report only when an evidence
        # entry ties a predicate id the dispatch did not declare
        # (internal/store/worker_lanes.go verifyWorkerPredicateTies); the
        # predicates no entry proves are decided by the completion verdicts
        # (CD-0180). The generated contract text is the only place a lane
        # agent learns that rule, so every lane definition must state it
        # while predicate_ids stays schema-optional.
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("evidence_entry.predicate_ids: optional array", normalized)
        self.assertIn("omit it on an entry that proves no declared predicate", normalized)
        self.assertIn("Tie a declared `predicate_id` only to an entry whose evidence proves that predicate", normalized)
        self.assertIn("the store refuses a completed report that ties a `predicate_id` the packet's `inputs.outcome_predicates` did not declare with `invalid_report`", normalized)
        self.assertIn("decided by the completion verdicts, never by this report", normalized)
        self.assertNotIn("must name every declared", normalized)

    def test_projection_states_the_declared_budget_as_a_command_duration_rule(self):
        # The registry declares a per-lane time budget, and the body must
        # project it: a verify attempt that widens to a full Go package suite
        # runs for minutes, and a worker left to guess shell timeouts loses
        # the run at the host's 120-second default.
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        self.assertIn("## Command duration", projection)
        self.assertIn("1200 seconds", projection)
        self.assertIn("`timeout`", projection)
        self.assertIn("milliseconds", projection)

    def test_projection_budget_tracks_the_declared_time_seconds_max(self):
        lane = dict(self.LANE, budgets={"time_seconds_max": 777})
        projection = generator.agent_projection(lane, REPORT_SCHEMA)
        self.assertIn("777 seconds", projection)
        self.assertNotIn("1200 seconds", projection)

    def test_projection_treats_full_go_suites_as_able_to_exceed_400_seconds(self):
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        self.assertIn("full Go package suites", projection)
        self.assertIn("exceed 400 seconds", projection)
        self.assertIn("go test ./...", projection)

    def test_projection_does_not_require_a_worker_cwd_readback(self):
        # The dispatch window pins the worker directory, and the adapter
        # composes the admitted report from the authorized packet, so the
        # lane definition must not ask the worker for a `pwd` readback.
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        self.assertNotIn("pwd", projection)
        self.assertNotIn("cwd", projection)

    def test_projection_requires_a_real_execute_source_lookup(self):
        # Every lane must attempt one real Context7 or Exa call per bounded
        # technical task through `execute`, discover signatures first, and
        # report an unconnected service instead of inventing a result.
        projection = generator.agent_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("Source lookup through `execute`", projection)
        self.assertIn("one real source lookup", normalized)
        self.assertIn("applies to repository-only tasks", normalized)
        self.assertIn("look up a relevant external technology", normalized)
        self.assertIn("repository sources, not external search results", normalized)
        self.assertIn("Discover the exact callable signatures first", normalized)
        self.assertIn("host-connected options", normalized)
        self.assertIn("Never invent a lookup result", normalized)

    def test_projection_keeps_the_file_change_rules_for_editing_lanes(self):
        # The dispatched packet carries the approved contract's bound law and
        # Domains as recorded state. A lane whose capabilities grant
        # edit_scoped_files is told to read each named law before changing
        # files, conform to it, and edit a law document only when the block
        # lists it as modified or added.
        lane = dict(self.LANE, capabilities=["read_repository", "edit_scoped_files", "run_tests", "report_evidence"])
        projection = generator.agent_projection(lane, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("Approved law and architecture block", projection)
        self.assertIn("Read each named law document before you change files", normalized)
        self.assertIn("Conform to it.", normalized)
        self.assertIn("`modified` or `added`", normalized)
        self.assertIn("Report any conflict between that law and the assigned result in your evidence", normalized)
        self.assertIn("`status` `failed`", normalized)

    def test_projection_gives_non_editing_lanes_the_assess_rule(self):
        # A lane without edit_scoped_files reads each named law before it
        # assesses the result, and receives no file-change rule, so the block
        # never implies edit authority the lane does not hold.
        lane = dict(self.LANE, capabilities=["read_repository", "inspect_diff", "run_targeted_checks", "report_findings"])
        projection = generator.agent_projection(lane, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("Approved law and architecture block", projection)
        self.assertIn("Read each named law document before you assess the result", normalized)
        self.assertNotIn("before you change files", normalized)
        self.assertNotIn("`modified` or `added`", normalized)
        self.assertIn("Report any conflict between that law and the assigned result in your evidence", normalized)
        self.assertIn("`status` `failed`", normalized)

    def test_utility_projection_projects_declared_tools_and_permissions(self):
        utility = {
            "id": "advisor",
            "purpose": "Give an independent opinion.",
            "allowed_tools": ["bash"],
            "allowed_commands": ["git diff *", "git log *"],
            "time_seconds_max": 1800,
        }
        projection = generator.utility_projection(utility)
        self.assertIn("mode: all", projection)
        self.assertIn("  bash: true", projection)
        self.assertIn("  read: false", projection)
        self.assertIn("  task: false", projection)
        self.assertIn('"*": deny', projection)
        self.assertIn('"git diff *": allow', projection)
        self.assertIn('"git log *": allow', projection)
        self.assertIn("30 minutes", projection)

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
        self.assertIn("query Context7", projection)
        self.assertIn("MCP access depends on host connections", projection)
        self.assertIn("Use read-only tools only", projection)

    def test_execute_enabled_utilities_carry_the_source_lookup_block(self):
        for utility_id, tools in (
            ("explore", ["bash", "read", "glob", "grep", "execute"]),
            ("lookup", ["webfetch", "execute"]),
            ("advisor", ["bash", "read", "glob", "grep", "execute"]),
        ):
            utility = {
                "id": utility_id,
                "purpose": "Bounded work.",
                "allowed_tools": tools,
                "allowed_commands": [],
                "time_seconds_max": 600,
            }
            projection = generator.utility_projection(utility)
            self.assertIn("Source lookup through `execute`", projection, utility_id)
            self.assertIn("Context7", projection, utility_id)
            self.assertIn("Exa", projection, utility_id)

    def test_bash_only_projection_never_receives_the_source_lookup_block(self):
        # The block is gated on declared `execute` access, not on the utility
        # id.
        utility = {
            "id": "advisor",
            "purpose": "Give an independent opinion.",
            "allowed_tools": ["bash"],
            "allowed_commands": ["git diff *"],
            "time_seconds_max": 1800,
        }
        projection = generator.utility_projection(utility)
        self.assertNotIn("Source lookup through `execute`", projection)
        self.assertNotIn("Context7", projection)

    def test_lookup_projection_names_the_services_and_discovers_signatures(self):
        utility = {
            "id": "lookup",
            "purpose": "Research bounded external questions.",
            "allowed_tools": ["webfetch", "execute"],
            "allowed_commands": [],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        normalized = " ".join(projection.split())
        self.assertIn("whether or not the parent supplied URLs", normalized)
        self.assertIn("Context7 for library documentation", normalized)
        self.assertIn("Exa for", normalized)
        self.assertIn("Discover the exact callable signature first", normalized)

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
        self.assertIn("search connected tools through `execute` first", " ".join(projection.split()))
        self.assertIn("one real source lookup", projection)
        self.assertIn("line range", projection)
        self.assertIn("10 minutes", projection)


class RepositoryEditBoundaryTests(unittest.TestCase):
    # D1: the boundary is derived from the edit_scoped_files capability in
    # contracts/agent-lanes.v1.json, never from the lane id and never from a
    # hand-written per-lane text.
    EDITING = ["read_repository", "edit_scoped_files", "run_tests", "report_evidence"]
    NON_EDITING = ["read_repository", "inspect_diff", "run_targeted_checks", "report_findings"]

    @staticmethod
    def lane(**overrides):
        base = {
            "id": "review",
            "purpose": "Review a bounded change against its contract.",
            "budgets": {"time_seconds_max": 1200},
            "evidence_obligations": ["findings", "verdict"],
            "capabilities": list(RepositoryEditBoundaryTests.NON_EDITING),
        }
        base.update(overrides)
        return base

    @staticmethod
    def boundary(lane):
        # The generator wraps prose at 80 columns; assert on normalized text.
        return " ".join(generator.repository_edit_boundary(lane).split())

    def test_editing_capability_yields_the_scoped_edit_boundary(self):
        boundary = self.boundary(self.lane(capabilities=self.EDITING))
        self.assertIn("change only files inside the approved contract scope", boundary)
        self.assertIn("Report a needed out-of-scope change instead of making it", boundary)
        self.assertNotIn("do not commit", boundary)

    def test_missing_capability_grants_no_edit_boundary_text(self):
        lane = self.lane()
        del lane["capabilities"]
        boundary = self.boundary(lane)
        self.assertIn("do not create, change, or delete repository source files", boundary)
        self.assertIn("do not commit", boundary)

    def test_running_checks_stays_permitted_for_non_editing_lanes(self):
        # Test-running roles keep their allowed commands: the boundary must
        # not call them globally read-only, and command artifacts are not
        # source edits.
        boundary = self.boundary(self.lane())
        self.assertIn("Running the tests and validators the role allows is permitted", boundary)
        self.assertIn("files those commands produce are not source edits", boundary)
        self.assertIn("Report a needed source change as evidence", boundary)

    def test_flipping_the_capability_flips_the_boundary_not_the_lane_name(self):
        for lane_id in ("review", "implement"):
            without = self.boundary(self.lane(id=lane_id, capabilities=self.NON_EDITING))
            with_edit = self.boundary(self.lane(id=lane_id, capabilities=self.EDITING))
            self.assertNotEqual(without, with_edit, lane_id)
            self.assertIn("do not create, change, or delete repository source files", without, lane_id)
            self.assertIn("change only files inside the approved contract scope", with_edit, lane_id)

    def test_projection_carries_the_boundary_in_description_and_body_section(self):
        cases = (
            (self.lane(id="design", capabilities=self.EDITING), "Edits only files inside the approved contract scope.", "change only files inside the approved contract scope"),
            (self.lane(id="verify"), "Does not edit repository source.", "do not create, change, or delete repository source files"),
        )
        for lane, clause, boundary_text in cases:
            projection = generator.agent_projection(lane, REPORT_SCHEMA)
            description = next(line for line in projection.splitlines() if line.startswith("description:"))
            self.assertIn(clause, description, lane["id"])
            self.assertIn("## Repository edit boundary", projection, lane["id"])
            self.assertIn(boundary_text, " ".join(projection.split()), lane["id"])

    def test_boundary_section_sits_between_the_intro_and_the_packet_rule(self):
        projection = generator.agent_projection(self.lane(), REPORT_SCHEMA)
        self.assertLess(projection.index("spawn nested workers."), projection.index("## Repository edit boundary"))
        self.assertLess(
            projection.index("## Repository edit boundary"),
            projection.index("Before any work, verify the first message you received"),
        )

    def test_utilities_never_carry_the_lane_boundary_section(self):
        utility = {
            "id": "advisor",
            "purpose": "Give an independent opinion.",
            "allowed_tools": ["bash"],
            "allowed_commands": ["git diff *"],
            "time_seconds_max": 1800,
        }
        projection = generator.utility_projection(utility)
        self.assertNotIn("## Repository edit boundary", projection)
        self.assertNotIn("Does not edit repository source.", projection)

    def test_every_manifest_lane_renders_the_boundary_matching_its_capability(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            projection = generator.agent_projection(lane, REPORT_SCHEMA)
            self.assertIn("## Repository edit boundary", projection, lane["id"])
            if "edit_scoped_files" in lane["capabilities"]:
                self.assertIn("Edits only files inside the approved contract scope.", projection, lane["id"])
                self.assertIn("change only files inside the approved contract scope", projection, lane["id"])
                self.assertNotIn("do not commit", projection, lane["id"])
            else:
                self.assertIn("Does not edit repository source.", projection, lane["id"])
                self.assertIn("do not create, change, or delete repository source files", projection, lane["id"])


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
    # Each fixture carries only the fields the surface under test reads, so
    # the tests state the resolution and projection rules rather than the
    # registry. FULL_LANES exists for go_projection, which formats complete
    # lane definitions.
    MANIFEST = {
        "lanes": [
            {"id": "research", "evidence_obligations": ["source_citations", "bounded_findings"]},
            {"id": "implement", "evidence_obligations": ["files_touched"]},
        ]
    }
    LEGACY = {
        "research:1": ["sha256:" + "b" * 64],
        "implement:1": ["sha256:" + "c" * 64],
    }
    FULL_LANES = [
        {
            "id": "research", "version": 1, "digest": "sha256:" + "a" * 64,
            "purpose": "p", "capability_class": "read_only", "capabilities": ["product_read"],
            "packet_schema_ref": "contracts/agent-lane-packet.schema.json",
            "report_schema_ref": "contracts/agent-lane-report.schema.json",
            "budgets": {"cost_usd_max": 1, "context_tokens_max": 1, "time_seconds_max": 1},
            "evidence_obligations": ["source_citations", "bounded_findings"],
            "required_report_blocks": [],
            "lifecycle_states": ["dispatched", "completed", "failed"],
        },
    ]

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
            {"lanes": self.MANIFEST["lanes"], "utilities": [], "legacy_lane_digests": self.LEGACY},
            "sha256:" + "0" * 64,
            {},
            {},
            {"research": "bounded_findings", "implement": "files_touched"},
        )
        self.assertIn('"research": "bounded_findings"', projection)
        self.assertIn('"implement": "files_touched"', projection)
        self.assertIn("export function workerScopeAssignedResult", projection)

    def test_projections_carry_the_legacy_lane_digest_set(self):
        # The legacy digest set (CD-0197 D5) is registry contract: both
        # generated layers must project it, so a persisted packet pinned to a
        # pre-policy digest resolves in the store and the adapter alike.
        manifest = {"lanes": self.FULL_LANES, "utilities": [], "legacy_lane_digests": self.LEGACY}
        digest = "sha256:" + "0" * 64
        go = generator.go_projection(manifest, digest)
        ts = generator.ts_projection(manifest, digest, {}, {}, {"research": "bounded_findings"})
        for projection in (go, ts):
            self.assertIn('"research:1"', projection)
            self.assertIn('"sha256:' + "b" * 64 + '"', projection)
            self.assertIn('"implement:1"', projection)
            self.assertIn('"sha256:' + "c" * 64 + '"', projection)


if __name__ == "__main__":
    unittest.main()
