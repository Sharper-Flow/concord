#!/usr/bin/env python3
import contextlib
import copy
import importlib.util
import io
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("lane_generator", ROOT / "scripts/generate-agent-lanes.py")
if spec is None or spec.loader is None:
    raise RuntimeError("unable to load lane generator")
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)
prompt_spec = importlib.util.spec_from_file_location("primary_prompt_checker", ROOT / "scripts/check-primary-prompts.py")
if prompt_spec is None or prompt_spec.loader is None:
    raise RuntimeError("unable to load scripts/check-primary-prompts.py")
prompt_checker = importlib.util.module_from_spec(prompt_spec)
prompt_spec.loader.exec_module(prompt_checker)
FULL_PROFILES = ("Concord-RTE", "Concord")
PLANNER_WRITERS = ["concord_work_compact", "concord_work_define", "concord_work_relate", "concord_work_start", "concord_work_transition"]
REPORT_SCHEMA = json.loads((ROOT / "contracts/agent-lane-report.schema.json").read_text(encoding="utf-8"))
PACKET_SCHEMA = json.loads((ROOT / "contracts/agent-lane-packet.schema.json").read_text(encoding="utf-8"))


def lane_projection(lane, report=None):
    return generator.agent_projection(lane, report if report is not None else REPORT_SCHEMA, PACKET_SCHEMA)


def bound_worker_tools(capabilities):
    granted = set()
    if "read_repository" in capabilities:
        granted.add("concord_worker_read")
    if "inspect_diff" in capabilities:
        granted.add("concord_worker_diff")
    if {"run_tests", "run_targeted_checks", "run_validators"} & set(capabilities):
        granted.add("concord_worker_check")
    return granted


class CapabilityToolProjectionTests(unittest.TestCase):
    def test_opaque_code_mode_is_denied_for_every_lane(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            with self.subTest(lane=lane["id"]):
                self.assertFalse(generator.lane_tool_grants(lane).get("execute", True))
                self.assertIn("\n  execute: false\n", lane_projection(lane))

    def test_non_authoring_lanes_cannot_inherit_shell_or_file_authoring_tools(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            if "edit_scoped_files" in lane["capabilities"]:
                continue
            with self.subTest(lane=lane["id"]):
                projection = lane_projection(lane)
                tools = projection.split("\ntools:\n", 1)[1].split("\npermission:\n", 1)[0]
                for tool_id in ("bash", "edit", "write", "patch", "apply_patch", "morph_edit"):
                    self.assertIn(f"  {tool_id}: false\n", tools + "\n")

    def test_authoring_ceiling_does_not_override_host_tool_denials(self):
        manifest, _ = generator.load_manifest()
        lane = copy.deepcopy(manifest["lanes"][0])
        lane["capabilities"].extend(["edit_scoped_files", "run_tests"])
        projection = lane_projection(lane)
        tools = projection.split("\ntools:\n", 1)[1].split("\npermission:\n", 1)[0]
        for tool_id in ("bash", "edit", "write", "patch", "apply_patch", "morph_edit"):
            self.assertTrue(generator.lane_tool_grants(lane)[tool_id])
            self.assertNotIn(f"  {tool_id}:", tools)


class SubagentMethodProjectionTests(unittest.TestCase):
    def test_every_subagent_executes_quietly_and_returns_one_final_result(self):
        manifest, _ = generator.load_manifest()
        projections = [lane_projection(lane) for lane in manifest["lanes"]]
        projections.extend(generator.utility_projection(utility) for utility in manifest["utilities"])
        self.assertEqual(len(projections), 8)
        for projection in projections:
            with self.subTest(agent=projection.split("\n# ", 1)[1].splitlines()[0]):
                text = " ".join(projection.split())
                self.assertEqual(projection.count("## Execution\n"), 1)
                self.assertIn("without TODO tracking, progress updates, or direct operator questions", text)
                self.assertIn("one final result in the required format", text)
                self.assertIn("never a plan or promise of further work", text)
                self.assertIn("blocker and missing evidence", text)
                self.assertIn("do not invent fields or a second reporting channel", text)

    def test_utility_definitions_deny_interactive_and_bookkeeping_tools(self):
        manifest, _ = generator.load_manifest()
        for utility in manifest["utilities"]:
            with self.subTest(utility=utility["id"]):
                tools = generator.utility_projection(utility).split("\ntools:\n", 1)[1].split("\npermission:\n", 1)[0]
                for tool in ("question", "todowrite", "task"):
                    self.assertIn(f"\n  {tool}: false\n", "\n" + tools + "\n")

    def test_explore_handoff_answers_first_and_preserves_the_requested_deliverable(self):
        manifest, _ = generator.load_manifest()
        utility = next(item for item in manifest["utilities"] if item["id"] == "explore")
        text = " ".join(generator.utility_projection(utility).split())
        self.assertIn("Answer the bounded question first", text)
        self.assertIn("When an inventory is requested, return the complete requested set", text)
        self.assertIn("smallest caller -> owner -> consumer map", text)
        self.assertIn("`path:line` for each observed connection", text)
        self.assertIn("Mark unverified connections as unknown", text)
        self.assertIn("Do not infer a root cause", text)

    def test_explore_distinguishes_adaptive_investigation_from_targeted_retrieval(self):
        manifest, _ = generator.load_manifest()
        utility = next(item for item in manifest["utilities"] if item["id"] == "explore")
        text = " ".join(generator.utility_projection(utility).split())
        self.assertIn("discovering and following evidence paths", utility["purpose"])
        self.assertIn("direct tools for targeted retrieval", utility["purpose"])
        self.assertIn("bounded question limits the investigation; it need not name the files", text)
        self.assertIn("Choose each next search from the evidence already found", text)

    def test_explore_handoff_is_a_source_backed_briefing_with_retrieval_handles(self):
        manifest, _ = generator.load_manifest()
        utility = next(item for item in manifest["utilities"] if item["id"] == "explore")
        text = " ".join(generator.utility_projection(utility).split())
        self.assertIn("right-sized briefing, not a search transcript", text)
        self.assertIn("exact symbols, search strings, test names, or commands", text)
        self.assertIn("confirm or expand each finding without repeating the investigation", text)
        self.assertIn("Do not repeat the same fact in several sections", text)

    def test_explore_marks_an_incomplete_inventory_and_names_its_coverage(self):
        manifest, _ = generator.load_manifest()
        utility = next(item for item in manifest["utilities"] if item["id"] == "explore")
        text = " ".join(generator.utility_projection(utility).split())
        self.assertIn("If the cap or unavailable evidence prevents completeness", text)
        self.assertIn("label the inventory partial", text)
        self.assertIn("population covered and the remaining gaps", text)

    def test_only_review_traces_changed_boundaries_and_classifies_findings(self):
        manifest, _ = generator.load_manifest()
        carrying = set()
        for lane in manifest["lanes"]:
            projection = lane_projection(lane)
            if "## Review method\n" not in projection:
                continue
            carrying.add(lane["id"])
            text = " ".join(projection.split())
            self.assertIn("trace the producing code through the relevant consumer or dispatch path", text)
            self.assertIn("Cite both sides", text)
            self.assertIn("triggering input or state, impact, and exact source location", text)
            self.assertIn("proven defects, missing required evidence, and optional suggestions", text)
            self.assertIn("existing finding detail", text)
            self.assertIn("use `review_finding.oracle.classification` and keep the detail consistent with it", text)
            self.assertIn("Do not turn style preferences into defects or a `no_ship` verdict", text)
            self.assertIn("do not broaden the review into an unrelated audit", text)
        self.assertEqual(carrying, {"review"})


class LaneDigestRetentionTests(unittest.TestCase):
    def test_generation_refuses_a_replaced_digest_without_retention(self):
        manifest = json.loads(generator.MANIFEST.read_text(encoding="utf-8"))
        previous = generator.lane_digest(manifest["lanes"][0])
        manifest["lanes"][0]["purpose"] += " Preserve future evidence."
        self.assertNotEqual(previous, generator.lane_digest(manifest["lanes"][0]))
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "manifest.json"
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with mock.patch.object(generator, "MANIFEST", path):
                with self.assertRaisesRegex(ValueError, previous):
                    generator.load_manifest()

    def test_generation_accepts_a_retained_replaced_digest(self):
        manifest = json.loads(generator.MANIFEST.read_text(encoding="utf-8"))
        lane = manifest["lanes"][0]
        previous = generator.lane_digest(lane)
        lane["purpose"] += " Preserve future evidence."
        manifest["legacy_lane_digests"][f"{lane['id']}:{lane['version']}"].append(previous)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "manifest.json"
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with mock.patch.object(generator, "MANIFEST", path):
                loaded, _ = generator.load_manifest()
        self.assertIn(previous, loaded["legacy_lane_digests"]["research:1"])

    def test_generation_refuses_a_removed_legacy_digest(self):
        manifest = json.loads(generator.MANIFEST.read_text(encoding="utf-8"))
        removed = manifest["legacy_lane_digests"]["research:1"].pop(0)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "manifest.json"
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with mock.patch.object(generator, "MANIFEST", path):
                with self.assertRaisesRegex(ValueError, removed):
                    generator.load_manifest()

    def test_history_reads_ignore_inherited_repository_redirection(self):
        with mock.patch.dict("os.environ", {"GIT_DIR": "/fixtures/not-a-repository", "GIT_WORK_TREE": "/fixtures/not-a-worktree"}):
            generator.load_manifest()


class AgentProjectionTests(unittest.TestCase):
    LANE = {
        "id": "review",
        "purpose": "Review a bounded change against its contract.",
        "budgets": {"time_seconds_max": 1200},
        "evidence_obligations": ["findings", "verdict"],
    }

    def test_projection_hides_the_lane_from_the_operator_agent_cycle(self):
        self.assertIn("\nhidden: true\n", lane_projection(self.LANE, REPORT_SCHEMA))

    def test_every_lane_is_subagent_only(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            with self.subTest(lane=lane["id"]):
                self.assertIn("\nmode: subagent\n", lane_projection(lane).split("---\n", 2)[1])

    def test_projection_denies_task_dispatch(self):
        # CD-0070 Invariant 3, carrying CD-0064 Invariant 3 forward.
        self.assertIn('"*": deny', lane_projection(self.LANE, REPORT_SCHEMA))

    def test_projection_denies_every_concord_tool(self):
        # CD-0017 D4, extended by CD-0223. The tool ids come from the two
        # tool-surface contracts, so the check reads them from there too.
        ids = generator.concord_tool_ids()
        self.assertIn("concord_work_start", ids)
        self.assertIn("concord_work_transition", ids)
        # The host catalog carries the CI watcher, so the denials must cover
        # every registered tool.
        self.assertIn("concord_ci_watch", ids)
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        for tool_id in ids:
            self.assertIn(f"\n  {tool_id}: false\n", projection)
        self.assertIn("## Concord context boundary", projection)

    def test_every_lane_and_utility_definition_denies_workflow_tools_without_positive_grants(self):
        # generate-agent-lanes.py --check keeps these files equal to the
        # projections, so this covers every lane and utility the manifest
        # declares. Named primary profiles hold Concord authority by role and
        # are covered by PrimaryProfileProjectionTests.
        manifest, _ = generator.load_manifest()
        entries = manifest["lanes"] + manifest["utilities"]
        self.assertTrue(entries)
        for entry in entries:
            path = ROOT / ".opencode/agents" / f"concord-{entry['id']}.md"
            text = path.read_text(encoding="utf-8")
            granted = bound_worker_tools(entry.get("capabilities", [])) if entry in manifest["lanes"] else set()
            for tool_id in generator.concord_tool_ids():
                denial = f"\n  {tool_id}: false\n"
                if tool_id in granted:
                    self.assertNotIn(denial, text)
                else:
                    self.assertIn(denial, text, f"{path.name} does not deny {tool_id}")
                self.assertNotIn(f"\n  {tool_id}: true\n", text)

    def test_projection_reads_report_bounds_from_schema(self):
        changed = copy.deepcopy(REPORT_SCHEMA)
        changed["properties"]["readback_model"]["maxLength"] = 77
        changed["properties"]["evidence"]["maxItems"] = 11
        changed["$defs"]["evidence_entry"]["properties"]["detail"]["maxLength"] = 23
        projection = lane_projection(self.LANE, changed)
        self.assertNotIn("maxLength=77", projection)
        self.assertIn("maxItems=11", projection)
        self.assertIn("maxLength=23", projection)

    def test_projection_teaches_the_canonical_task_and_binding_authority(self):
        # The packet task is the objective verbatim and the binding
        # is its typed authority, so the guidance names both and the one-result
        # completion rule.
        normalized = " ".join(lane_projection(self.LANE).split())
        self.assertIn("## Objective and binding", normalized)
        self.assertIn("`inputs.task` is the canonical objective, carried verbatim", normalized)
        self.assertIn("The packet adds no header or trailer", normalized)
        self.assertIn("`inputs.binding` is the typed authority", normalized)
        self.assertIn("`objective_source`", normalized)
        self.assertIn("`work_version` and `contract_version`", normalized)
        self.assertIn("`contract_version` is null before a contract is approved", normalized)
        self.assertIn("`assigned_result` names the one evidence obligation whose discharge completes this attempt", normalized)
        self.assertIn("Complete only that assigned result", normalized)

    def test_projection_instructs_the_lane_to_refuse_a_non_packet_first_message(self):
        # CD-0102 heuristic control: with no adapter plugin nothing
        # adapter-side runs, so every generated lane definition must itself
        # refuse a first message that is not a well-formed packet and return
        # the report with status failed.
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
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
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("evidence_entry.predicate_ids: optional array", normalized)
        self.assertIn("minItems=0, maxItems=8", normalized)
        self.assertIn("omit it or use an empty array on an entry that proves no declared predicate", normalized)
        self.assertIn("Both forms mean no predicate tie", normalized)
        self.assertNotIn("an empty array fails", normalized)
        self.assertIn("Tie a declared `predicate_id` only to an entry whose evidence proves that predicate", normalized)
        self.assertIn("the store refuses a completed report that ties a `predicate_id` the packet's `inputs.outcome_predicates` did not declare with `invalid_report`", normalized)
        self.assertIn("decided by the completion verdicts, never by this report", normalized)
        self.assertNotIn("must name every declared", normalized)

    def test_projection_teaches_the_work_context_first_read(self):
        # CON-887: when the packet carries inputs.work_context the worker
        # reads it before the objective's own sources, walks the readings in
        # order at the pinned commits, and records new typed findings on the
        # report. The bounds the block states are read off the packet
        # schema, not restated as literals.
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("When `inputs.work_context` is present, read it first", normalized)
        self.assertIn("`required_reading` in order", normalized)
        self.assertIn("with `concord_worker_read`", normalized)
        self.assertIn("zero-based `ordinal`", normalized)
        self.assertIn("Continue at `next_offset` until `eof`", normalized)
        self.assertIn("original pinned source, not the changed checkout or the network", normalized)
        self.assertIn("`domain_groups`", normalized)
        self.assertIn("Record new conclusions, rejected routes, and open questions as report `context_findings`", normalized)
        view = PACKET_SCHEMA["$defs"]["lane_work_context_view"]
        self.assertIn(
            f"at most {view['properties']['required_reading']['maxItems']} readings and {view['properties']['findings']['maxItems']} findings",
            normalized,
        )

    def test_projection_work_context_bounds_track_the_packet_schema(self):
        # The numbers in the work-context block are parameters fed from the
        # packet schema, not literals in the generator body.
        changed = copy.deepcopy(PACKET_SCHEMA)
        changed["$defs"]["lane_work_context_view"]["properties"]["required_reading"]["maxItems"] = 5
        changed["$defs"]["lane_work_context_view"]["properties"]["findings"]["maxItems"] = 6
        normalized = " ".join(generator.agent_projection(self.LANE, REPORT_SCHEMA, changed).split())
        self.assertIn("at most 5 readings and 6 findings", normalized)
        self.assertNotIn("at most 32 readings", normalized)

    def test_projection_teaches_the_checkpoint_as_coordinator_directions(self):
        # CON-883: when the packet carries inputs.checkpoint its strategy
        # and diagnosis are coordinator directions the worker follows for
        # the attempt, and a departure is recorded on the report rather
        # than silently taken.
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("When `inputs.checkpoint` is present", normalized)
        self.assertIn("`strategy` and `diagnosis` are coordinator directions to follow", normalized)
        self.assertIn("`hypothesis` states what the coordinator believed", normalized)
        self.assertIn("`touched_refs` and `evidence_refs`", normalized)
        self.assertIn("Record the departure as report `context_findings`", normalized)
        self.assertIn("never by silently ignoring the checkpoint", normalized)

    def test_projection_states_the_declared_budget_as_a_command_duration_rule(self):
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        self.assertIn("## Command duration", projection)
        self.assertIn("1200 seconds", projection)
        self.assertIn("General shell execution is unavailable", projection)
        self.assertIn("`concord_worker_check`", projection)
        self.assertIn("The host supplies the recorded", projection)
        self.assertIn("native execution and its deadline", projection)
        self.assertNotIn("`timeout`", projection)

    def test_editing_projection_requires_an_explicit_shell_timeout(self):
        lane = dict(self.LANE, capabilities=["edit_scoped_files", "run_tests"])
        projection = lane_projection(lane, REPORT_SCHEMA)
        self.assertIn("`timeout`", projection)
        self.assertIn("milliseconds", projection)

    def test_projection_budget_tracks_the_declared_time_seconds_max(self):
        lane = dict(self.LANE, budgets={"time_seconds_max": 777})
        projection = lane_projection(lane, REPORT_SCHEMA)
        self.assertIn("777 seconds", projection)
        self.assertNotIn("1200 seconds", projection)

    def test_projection_treats_full_go_suites_as_able_to_exceed_400_seconds(self):
        lane = dict(self.LANE, capabilities=["edit_scoped_files", "run_tests"])
        projection = lane_projection(lane, REPORT_SCHEMA)
        self.assertIn("full Go package suites", projection)
        self.assertIn("exceed 400 seconds", projection)
        self.assertIn("go test ./...", projection)

    def test_projection_does_not_require_a_worker_cwd_readback(self):
        # The dispatch window pins the worker directory, and the adapter
        # composes the admitted report from the authorized packet, so the
        # lane definition must not ask the worker for a `pwd` readback.
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        self.assertNotIn("pwd", projection)
        self.assertNotIn("cwd", projection)

    def test_no_lane_or_utility_carries_a_lookup_procedure(self):
        # CD-0043 D1: lane definitions carry no procedure. The conduct corpus
        # (.concord/instructions/evidence.md) owns the technical-unknown
        # obligation and reaches every session, so a definition never restates
        # a lookup routine.
        manifest = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text())
        projections = [(lane["id"], lane_projection(lane)) for lane in manifest["lanes"]]
        projections += [(utility["id"], generator.utility_projection(utility)) for utility in manifest["utilities"]]
        for agent_id, projection in projections:
            with self.subTest(agent=agent_id):
                self.assertNotIn("## Source lookup routing", projection)
                self.assertNotIn("Route each technical lookup", projection)

    def test_only_the_review_lane_carries_the_review_block_constraints(self):
        manifest = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text())
        for lane in manifest["lanes"]:
            with self.subTest(lane=lane["id"]):
                projection = lane_projection(lane)
                carries = "review" in lane.get("required_report_blocks", [])
                self.assertEqual("- review.verdict:" in projection, carries)
                self.assertEqual("- review_finding.oracle:" in projection, carries)
                self.assertIn("- evidence_entry.oracle_receipt:", projection)

    def test_projection_never_asks_the_worker_to_copy_the_worker_job(self):
        # The adapter strips a worker-authored worker_job and derives the
        # binding from the packet, so the definition only tells the worker to
        # omit it.
        normalized = " ".join(lane_projection(self.LANE).split())
        self.assertNotIn("copy its `job_id`", normalized)
        self.assertNotIn("- worker_job:", lane_projection(self.LANE))

    def test_projection_keeps_the_native_oracle_teaching(self):
        # CD-0223: native-oracle teaching distinguishes the supported Go and
        # target-repository drivers, and the host owns preparation and
        # execution.
        normalized = " ".join(lane_projection(self.LANE).split())
        self.assertIn("The authenticated host owns `native_oracle_v2` preparation and execution", normalized)
        self.assertIn("pinned pure-Go harness compiles", normalized)
        self.assertIn("`target_runner_v1` recipe", normalized)
        self.assertIn("Ordinary test output does not acquire a qualified oracle receipt", normalized)
        self.assertIn("A timeout is `unavailable` evidence, never a measured failure", normalized)
        self.assertIn("Workers gain no general Concord tools", normalized)
        self.assertNotIn("Managed workers gain no Concord tools", normalized)

    def test_projection_frames_the_report_when_the_packet_pins_a_report_protocol(self):
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        marker = PACKET_SCHEMA["properties"]["inputs"]["properties"]["report_protocol"]["const"]
        normalized = " ".join(projection.split())
        self.assertIn(
            f"When `inputs.report_protocol` is present, return exactly one Markdown fence whose info string is `{marker}`.",
            normalized,
        )
        self.assertIn("exactly three backticks and that info string on one line", normalized)
        self.assertIn("Return the entire frame as one final text part", normalized)

    def test_projection_takes_the_frame_marker_from_the_packet_schema(self):
        # The reserved fence info string belongs to the packet schema, not
        # the generator: changing the schema const must change the marker the
        # lane is told to use, with no generator-side literal left behind.
        changed = copy.deepcopy(PACKET_SCHEMA)
        changed["properties"]["inputs"]["properties"]["report_protocol"]["const"] = "concord-worker-result-test9"
        normalized = " ".join(generator.agent_projection(self.LANE, REPORT_SCHEMA, changed).split())
        self.assertIn("info string is `concord-worker-result-test9`", normalized)
        self.assertNotIn("concord-worker-result-v1", normalized)

    def test_projection_names_the_legacy_plain_object_branch(self):
        # A historical packet carries no `inputs.report_protocol`; the lane
        # must be told that branch by name instead of guessing a format.
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn(
            "For a historical packet without `inputs.report_protocol`, return one plain JSON report object as your final text part.",
            normalized,
        )
        self.assertIn("Never return multiple report candidates", normalized)

    def test_projection_requests_worker_content_without_host_or_packet_identity(self):
        projection = lane_projection(self.LANE, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertNotIn("Set `schema_version` to", normalized)
        self.assertNotIn("readback_model", normalized)
        self.assertIn("required=[\"status\", \"evidence\"]", normalized)
        self.assertIn("The adapter supplies report identity from the dispatch packet and host readback", normalized)

    def test_projection_keeps_the_file_change_rules_for_editing_lanes(self):
        # The dispatched packet carries the approved contract's bound law and
        # Domains as recorded state. A lane whose capabilities grant
        # edit_scoped_files is told to read each named law before changing
        # files, conform to it, and edit a law document only when its roles
        # list modified or added.
        lane = dict(self.LANE, capabilities=["read_repository", "edit_scoped_files", "run_tests", "report_evidence"])
        projection = lane_projection(lane, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("## Approved law and Domains", projection)
        self.assertIn("When `inputs.law_context` is present", normalized)
        self.assertIn("Read each named law document before you change files", normalized)
        self.assertIn("Conform to it.", normalized)
        self.assertIn("`modified` or `added`", normalized)
        self.assertIn("Report any conflict between that law and the assigned result in your evidence", normalized)
        self.assertIn("`status` `failed`", normalized)

    def test_projection_gives_non_editing_lanes_the_assess_rule(self):
        # A lane without edit_scoped_files reads each named law before it
        # assesses the result, and receives no file-change rule, so the law context
        # never implies edit authority the lane does not hold.
        lane = dict(self.LANE, capabilities=["read_repository", "inspect_diff", "run_targeted_checks", "report_findings"])
        projection = lane_projection(lane, REPORT_SCHEMA)
        normalized = " ".join(projection.split())
        self.assertIn("## Approved law and Domains", projection)
        self.assertIn("When `inputs.law_context` is present", normalized)
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
        # CD-0184 registry descriptions: a utility is a coordinator-native
        # Task, so it generates subagent-only and never as a primary agent.
        self.assertIn("mode: subagent", projection)
        self.assertNotIn("mode: all", projection)
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
        self.assertIn("MCP access depends on host connections", projection)
        self.assertIn("Use read-only tools only", projection)

    def test_exploration_body_returns_a_facts_packet_with_bounded_unknowns(self):
        # CD-0210 draft amends CD-0149 D2 for the intake root-cause route:
        # the explore body returns a facts packet as plain text, distinct
        # from lane evidence and free of any report schema, holding
        # observed facts at path:line, counts with their population,
        # commit SHAs for historical facts, and unknowns as unknowns.
        utility = {
            "id": "explore",
            "purpose": "Inspect a repository.",
            "allowed_tools": ["bash", "read", "glob", "grep", "execute"],
            "allowed_commands": ["git status *"],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        normalized = " ".join(projection.split())
        self.assertIn("facts packet", normalized)
        self.assertIn("plain text", normalized)
        self.assertIn("not lane evidence", normalized)
        self.assertIn("no report schema", normalized)
        self.assertIn("`path:line`", projection)
        self.assertIn("population", normalized)
        self.assertIn("commit SHA", normalized)
        self.assertIn("State the missing evidence when the question cannot be answered from the repository", normalized)
        self.assertNotIn("Separate observed facts from inferences", normalized)

    def test_exploration_body_prohibits_diagnosis_and_places_root_cause_with_the_coordinator(self):
        # CD-0210 draft: explore holds no diagnostic authority. The body
        # states the prohibition explicitly — the packet is prose, so the
        # limits are return limits, not runtime enforcement — and names the
        # parent coordinator as the owner of diagnosis and root cause.
        utility = {
            "id": "explore",
            "purpose": "Inspect a repository.",
            "allowed_tools": ["bash", "read", "glob", "grep", "execute"],
            "allowed_commands": ["git status *"],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        normalized = " ".join(projection.split())
        self.assertIn("Do not infer a root cause", normalized)
        self.assertIn("recommend a repair", normalized)
        self.assertIn("The parent coordinator owns the diagnosis and the root-cause assessment", normalized)
        self.assertNotIn("inferences", projection)

    def test_external_only_lookup_projection_carries_no_repository_source_guidance(self):
        # The lookup utility is external research only: its declared tools
        # grant no repository reads, and its body forbids inspecting the
        # repository. The shared routing block must therefore never direct
        # it to repository files, `read`/`grep`, or a connected code-search
        # tool, while the external routing, consulted-source citation, and
        # signature discovery stay.
        utility = {
            "id": "lookup",
            "purpose": "Research bounded external questions.",
            "allowed_tools": ["webfetch", "execute"],
            "allowed_commands": [],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        normalized = " ".join(projection.split())
        self.assertIn("Do not inspect or edit the repository", normalized)
        self.assertNotIn("local source lookup", normalized)
        self.assertNotIn("`read` and `grep`", normalized)
        self.assertNotIn("lgrep", normalized)
        self.assertNotIn("the repository does not settle", normalized)

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
        self.assertNotIn("## Source lookup routing", projection)
        self.assertNotIn("Context7", projection)

    def test_all_registered_projections_omit_external_call_quotas(self):
        manifest = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text())
        projections = [
            (lane["id"], lane_projection(lane)) for lane in manifest["lanes"]
        ] + [
            (utility["id"], generator.utility_projection(utility))
            for utility in manifest["utilities"]
        ]
        for agent_id, projection in projections:
            with self.subTest(agent=agent_id):
                normalized = " ".join(projection.split())
                self.assertNotIn("For each bounded technical task, make one real source lookup", normalized)
                self.assertNotIn("whether or not the parent supplied URLs", normalized)
                self.assertNotIn("look up a relevant external technology", normalized)

    def test_lookup_projection_names_the_services_and_discovers_signatures(self):
        # The lookup method searches connected services as needed for
        # external evidence the supplied URLs do not settle. Fetching a
        # supplied authoritative source is itself external research, so no
        # unrelated Context7/Exa quota rides on a question the supplied
        # URLs already answer.
        utility = {
            "id": "lookup",
            "purpose": "Research bounded external questions.",
            "allowed_tools": ["webfetch", "execute"],
            "allowed_commands": [],
            "time_seconds_max": 600,
        }
        projection = generator.utility_projection(utility)
        normalized = " ".join(projection.split())
        self.assertIn("Fetch each supplied source URL with `webfetch`", normalized)
        self.assertIn("Fetching a supplied authoritative source is external research", normalized)
        self.assertIn("as needed for external evidence the supplied URLs do not settle", normalized)
        self.assertIn("Context7 for library documentation", normalized)
        self.assertIn("Exa for", normalized)
        self.assertIn("Discover the exact callable signature first", normalized)
        self.assertNotIn("whether or not the parent supplied URLs", normalized)
        self.assertNotIn("the required source lookup", normalized)

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

    def test_generated_permissions_cover_every_registered_lane(self):
        manifest = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text(encoding="utf-8"))
        for lane in manifest["lanes"]:
            with self.subTest(lane=lane["id"]):
                frontmatter = lane_projection(lane).split("\n---\n", 1)[0]
                permissions = frontmatter.split("\npermission:\n", 1)[1]
                for editor in ("edit", "morph_edit"):
                    denial = f"  {editor}: deny\n"
                    if "edit_scoped_files" in lane["capabilities"]:
                        self.assertNotIn(denial, permissions)
                    else:
                        self.assertIn(denial, permissions)

    def test_generated_editor_permissions_follow_capability_not_lane_name(self):
        for lane_id in ("review", "implement", "synthetic"):
            for capabilities in (self.NON_EDITING, self.EDITING, []):
                with self.subTest(lane=lane_id, capabilities=capabilities):
                    lane = self.lane(id=lane_id, capabilities=capabilities)
                    frontmatter = lane_projection(lane).split("\n---\n", 1)[0]
                    permissions = frontmatter.split("\npermission:\n", 1)[1]
                    for editor in ("edit", "morph_edit"):
                        self.assertEqual(
                            f"  {editor}: deny\n" in permissions,
                            "edit_scoped_files" not in capabilities,
                        )

    def test_boundary_includes_authored_concord_sources_and_law_reference(self):
        for capabilities in (self.NON_EDITING, self.EDITING):
            with self.subTest(capabilities=capabilities):
                boundary = self.boundary(self.lane(capabilities=capabilities))
                self.assertIn("Authored `.concord/` files are repository sources", boundary)
                self.assertIn(
                    ".concord/docs/decisions/CD-0220-repository-authoring-follows-lane-capability-and-approved-scope.md",
                    boundary,
                )

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
        self.assertIn("Normal test artifacts and regenerated projections are not source edits", boundary)
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
            projection = lane_projection(lane, REPORT_SCHEMA)
            description = next(line for line in projection.splitlines() if line.startswith("description:"))
            self.assertIn(clause, description, lane["id"])
            self.assertIn("## Repository edit boundary", projection, lane["id"])
            self.assertIn(boundary_text, " ".join(projection.split()), lane["id"])

    def test_boundary_section_sits_between_the_intro_and_the_packet_rule(self):
        projection = lane_projection(self.lane(), REPORT_SCHEMA)
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
            projection = lane_projection(lane, REPORT_SCHEMA)
            self.assertIn("## Repository edit boundary", projection, lane["id"])
            if "edit_scoped_files" in lane["capabilities"]:
                self.assertIn("Edits only files inside the approved contract scope.", projection, lane["id"])
                self.assertIn("change only files inside the approved contract scope", projection, lane["id"])
                self.assertNotIn("do not commit", projection, lane["id"])
            else:
                self.assertIn("Does not edit repository source.", projection, lane["id"])
                self.assertIn("do not create, change, or delete repository source files", projection, lane["id"])


class CommittedContentRuleTests(unittest.TestCase):
    # The rule has one owner, the conduct corpus paragraph the generator
    # validates. Lanes that write repository files apply it by its lead
    # sentence; the diff-inspecting lane reviews against it; other lanes carry
    # no section.
    EDITING = ["read_repository", "edit_scoped_files", "run_tests", "report_evidence"]
    REVIEWING = ["read_repository", "inspect_diff", "run_targeted_checks", "report_findings"]
    VERIFYING = ["read_repository", "run_tests", "run_validators", "report_evidence"]

    @staticmethod
    def lane(lane_id, capabilities):
        return {
            "id": lane_id,
            "purpose": "Do one bounded thing.",
            "budgets": {"time_seconds_max": 1200},
            "evidence_obligations": ["files_touched"],
            "capabilities": capabilities,
        }

    @staticmethod
    def owner_paragraph():
        text = (ROOT / ".concord/instructions/change.md").read_text(encoding="utf-8")
        blocks = [" ".join(block.split()) for block in text.split("\n\n")]
        return next(block for block in blocks if block.startswith(generator.COMMITTED_CONTENT_LEAD))

    def owner_reference(self):
        return f'the conduct rule that opens "{generator.COMMITTED_CONTENT_LEAD}" in `.concord/instructions/change.md`'

    def test_editing_lane_applies_the_owner_rule_by_reference(self):
        projection = " ".join(lane_projection(self.lane("implement", self.EDITING)).split())
        self.assertIn("## Committed content", projection)
        self.assertIn(f"Apply {self.owner_reference()} to every comment and document you add or change", projection)
        self.assertTrue(self.owner_paragraph().startswith(generator.COMMITTED_CONTENT_LEAD))
        self.assertNotIn(self.owner_paragraph(), projection)

    def test_reviewing_lane_reports_violations_as_named_findings(self):
        projection = " ".join(lane_projection(self.lane("review", self.REVIEWING)).split())
        self.assertIn("## Committed content", projection)
        self.assertIn(f"Review every added comment and committed document against {self.owner_reference()}", projection)
        self.assertIn("`committed-content:`", projection)
        self.assertNotIn(self.owner_paragraph(), projection)

    def test_other_lanes_carry_no_committed_content_section(self):
        self.assertNotIn("## Committed content", lane_projection(self.lane("verify", self.VERIFYING)))

    def test_section_sits_between_the_edit_boundary_and_the_packet_rule(self):
        projection = lane_projection(self.lane("design", self.EDITING))
        self.assertLess(projection.index("## Repository edit boundary"), projection.index("## Committed content"))
        self.assertLess(
            projection.index("## Committed content"),
            projection.index("Before any work, verify the first message you received"),
        )

    def test_manifest_lanes_follow_their_capabilities(self):
        manifest, _ = generator.load_manifest()
        carrying = {lane["id"] for lane in manifest["lanes"] if "## Committed content" in lane_projection(lane)}
        self.assertEqual(carrying, {"implement", "design", "review"})

    def test_a_missing_or_repeated_owner_paragraph_fails_generation(self):
        original = generator.CONDUCT_CHANGE
        self.addCleanup(setattr, generator, "CONDUCT_CHANGE", original)
        paragraph = "Commit only durable content. Keep comments durable."
        for body in ("# Changing things\n\nNo rule here.\n", f"{paragraph}\n\n{paragraph}\n"):
            with self.subTest(body=body), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "change.md"
                path.write_text(body, encoding="utf-8")
                generator.CONDUCT_CHANGE = path
                with self.assertRaises(ValueError):
                    generator.committed_content_rule()


class JobSizeProjectionTests(unittest.TestCase):
    # CD-0225 D2 and CD-0227 D2: the capped job's size accounting, the
    # coordinator-owned overrun split, and the direct-caller repair boundary
    # are generated lane instructions, so the tests prove the projected
    # behavior for every relevant lane, not only the helper.

    @staticmethod
    def normalized(lane):
        return " ".join(lane_projection(lane).split())

    def manifest_lane(self, lane_id):
        manifest, _ = generator.load_manifest()
        return next(lane for lane in manifest["lanes"] if lane["id"] == lane_id)

    def test_capped_size_rule_is_stated_for_every_registered_lane(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            with self.subTest(lane=lane["id"]):
                text = self.normalized(lane)
                self.assertIn("## Job size and caller boundary", text)
                self.assertIn("carries `production_size_max` and `measurement_basis`", text)
                self.assertIn(
                    "measured against the recorded `measurement_basis.base_commit` under the recorded path inventory",
                    text,
                )
                self.assertIn("counts as `production`", text)
                self.assertIn("additions plus deletions of text lines over the whole base..tree delta", text)
                self.assertIn("a binary path adds no text lines and never establishes available production size", text)

    def test_only_the_implementation_lane_carries_the_size_report_and_split_boundary(self):
        manifest, _ = generator.load_manifest()
        carrying = set()
        for lane in manifest["lanes"]:
            text = self.normalized(lane)
            if "completed report carries `production_size`" not in text:
                continue
            carrying.add(lane["id"])
            self.assertIn(
                "`base` is the packet's `measurement_basis.base_commit`, `tree` is the raw delivered commit OID, "
                "and `count` is that whole base..tree text count",
                text,
            )
            self.assertIn(
                "Never pre-apply a proposed supplemental classification and never subtract another job's accepted paths",
                text,
            )
            self.assertIn(
                "the coordinator alone applies final classifications and foreign subtraction, "
                "records the accepted size, and holds it to the fixed cap",
                text,
            )
            self.assertIn("an overrun stays reported", text)
            self.assertIn(
                "Never raise the cap, hide the delta, rebase the correction, invent an acceptance, "
                "author a split, or reset the cumulative base",
                text,
            )
        self.assertEqual(carrying, {"implement"})

    def test_direct_caller_boundary_is_implementation_lane_text(self):
        manifest, _ = generator.load_manifest()
        repairs_max = REPORT_SCHEMA["properties"]["direct_caller_repairs"]["maxItems"]
        for lane in manifest["lanes"]:
            text = self.normalized(lane)
            if lane["id"] != "implement":
                self.assertNotIn("`direct_caller_repairs` entries", text)
                continue
            with self.subTest(lane=lane["id"]):
                self.assertIn(
                    "under the packet's `direct_caller_allowance` and outside its `caller_exclusions`, "
                    "never as a widening of `path_scope`",
                    text,
                )
                self.assertIn(f"Report at most {repairs_max} `direct_caller_repairs` entries", text)
                self.assertIn(
                    "the owner and caller mechanisms, the direct edge between them, "
                    "the exposing request or fixture, the changed paths",
                    text,
                )
                self.assertIn("the three checks `baseline_pass`, `owner_only_fail`, and `combined_pass`", text)
                self.assertIn("its `observed` output apart from any `inferred_cause`", text)
                self.assertIn("A compile-time break names the compile check in all three", text)
                self.assertIn("minimum source and test change that preserves an existing direct caller's request behavior", text)
                self.assertIn("names each generator input in `generator_ref` and among the changed paths", text)
                self.assertIn("a generated output is never hand-edited", text)
                self.assertIn("A transitive caller is not a direct edge and joins no repair", text)
                self.assertIn("a path another work item owns is never claimed", text)
                self.assertIn(
                    "the Project, approved Domains, pinned law, and other work's scope, resource, and overlap boundaries stay",
                    text,
                )
                self.assertIn(
                    "Missing authority or evidence stops the repair: report the blocker through this report, "
                    "never as a direct operator question",
                    text,
                )

    def test_other_lanes_invent_no_size_fields_and_gain_no_capability(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            if lane["id"] == "implement":
                continue
            with self.subTest(lane=lane["id"]):
                text = self.normalized(lane)
                self.assertIn("A capped packet expands no capability", text)
                self.assertIn("reports no `production_size` and no `direct_caller_repairs`", text)
                self.assertIn("never invents either field", text)

    def test_review_distinguishes_missing_evidence_from_a_proven_defect(self):
        text = self.normalized(self.manifest_lane("review"))
        self.assertIn("Examine the size and caller-repair evidence a completed report carries", text)
        self.assertIn("distinguish missing evidence from a proven defect", text)

    def test_design_is_not_mislabelled_read_only_by_the_size_block(self):
        # The design lane edits its scoped files; it is not the implementation
        # capability class, and the size block must not call it read-only.
        projection = lane_projection(self.manifest_lane("design"))
        section = projection.split("## Job size and caller boundary", 1)[1]
        self.assertNotIn("read-only", section)
        self.assertIn("expands no capability", section)

    def test_uncapped_and_historical_packets_gain_no_fields(self):
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            with self.subTest(lane=lane["id"]):
                text = self.normalized(lane)
                self.assertIn("A packet without these members is historical or uncapped", text)
                self.assertIn("report without the absent fields and never invent them", text)

    def test_repairs_ceiling_tracks_the_report_schema(self):
        # The entries ceiling in the direct-caller paragraph is a parameter
        # fed from the report schema, not a literal in the generator body.
        changed = copy.deepcopy(REPORT_SCHEMA)
        changed["properties"]["direct_caller_repairs"]["maxItems"] = 3
        text = " ".join(lane_projection(self.manifest_lane("implement"), changed).split())
        self.assertIn("at most 3 `direct_caller_repairs` entries", text)
        self.assertNotIn("at most 8 `direct_caller_repairs` entries", text)

    def test_implementation_projects_closed_size_and_repair_report_shapes(self):
        fields = ("production_size", "direct_caller_repairs")
        definitions = ("production_size", "direct_caller_repair", "caller_mechanism", "caller_repair_checks", "caller_repair_check")
        manifest, _ = generator.load_manifest()
        for lane in manifest["lanes"]:
            lines = generator.report_projection_constraints(REPORT_SCHEMA, lane)
            with self.subTest(lane=lane["id"]):
                for name in fields:
                    expected = f"{name}: implementation-only report member; schema={json.dumps(REPORT_SCHEMA['properties'][name], ensure_ascii=False)}."
                    if lane["id"] == "implement":
                        self.assertIn(expected, lines)
                        self.assertIn(expected, lane_projection(lane))
                    else:
                        self.assertNotIn(expected, lines)
                if lane["id"] == "implement":
                    for name in definitions:
                        self.assertIn(f"{name} shape: {json.dumps(REPORT_SCHEMA['$defs'][name], ensure_ascii=False)}.", lines)


class AgentDirectoryInventoryTests(unittest.TestCase):
    def run_check(self, extra=None):
        with tempfile.TemporaryDirectory() as directory:
            agents = Path(directory)
            for path in generator.AGENT_DIR.iterdir():
                (agents / path.name).write_bytes(path.read_bytes())
            if extra:
                (agents / extra).write_text("---\nmode: subagent\n---\n", encoding="utf-8")
            stderr = io.StringIO()
            with mock.patch.object(generator, "AGENT_DIR", agents), \
                    mock.patch.object(sys, "argv", ["generate-agent-lanes.py", "--check"]), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(stderr):
                return generator.main(), stderr.getvalue()

    def test_check_passes_when_every_definition_is_projected(self):
        self.assertEqual(self.run_check(), (0, ""))

    def test_check_refuses_a_definition_no_manifest_entry_projects(self):
        code, stderr = self.run_check("concord-retired.md")
        self.assertEqual(code, 1)
        self.assertIn("unprojected agent definition: concord-retired.md", stderr)


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

    def test_go_projection_emits_the_assigned_result_surface(self):
        # The store validates a packet binding's assigned result against the
        # same worker-scope contract the adapter reads, so both generated
        # layers must project it.
        projection = generator.go_projection(
            {"lanes": self.FULL_LANES, "utilities": [], "legacy_lane_digests": self.LEGACY},
            "sha256:" + "0" * 64,
            {"research": "bounded_findings"},
        )
        self.assertIn("var generatedWorkerScopeAssignments = map[string]string{\n\t\"research\": \"bounded_findings\",\n}", projection)

    def test_projections_carry_the_legacy_lane_digest_set(self):
        # The legacy digest set (CD-0197 D5) is registry contract: both
        # generated layers must project it, so a persisted packet pinned to a
        # pre-policy digest resolves in the store and the adapter alike.
        manifest = {"lanes": self.FULL_LANES, "utilities": [], "legacy_lane_digests": self.LEGACY}
        digest = "sha256:" + "0" * 64
        scope = {"research": "bounded_findings", "implement": "files_touched"}
        go = generator.go_projection(manifest, digest, scope)
        ts = generator.ts_projection(manifest, digest, {}, {}, scope)
        for projection in (go, ts):
            self.assertIn('"research:1"', projection)
            self.assertIn('"sha256:' + "b" * 64 + '"', projection)
            self.assertIn('"implement:1"', projection)
            self.assertIn('"sha256:' + "c" * 64 + '"', projection)


class PrimaryProfileProjectionTests(unittest.TestCase):
    def setUp(self):
        self.manifest, _ = generator.load_manifest()
        self.profiles = generator.load_primary_profiles(self.manifest)

    def profile(self, agent_id):
        return next(profile for profile in self.profiles["profiles"] if profile["id"] == agent_id)

    def projection(self, agent_id):
        profile = self.profile(agent_id)
        return generator.primary_projection(profile, (ROOT / profile["role_source"]).read_text(encoding="utf-8"))

    def frontmatter(self, text):
        self.assertTrue(text.startswith("---\n"))
        return text[4:text.index("\n---\n", 4)]

    def broken(self, mutate):
        profiles = copy.deepcopy(self.profiles)
        mutate(profiles)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "profiles.json"
            path.write_text(json.dumps(profiles), encoding="utf-8")
            with self.assertRaises(ValueError) as caught:
                generator.load_primary_profiles(self.manifest, path)
        return str(caught.exception)

    def test_registry_holds_exactly_the_rte_concord_and_planner(self):
        # The ids are exactly Concord-RTE, Concord, and Concord-Planner so the
        # host UI shows those names; the role sources stay lowercase.
        self.assertEqual([profile["id"] for profile in self.profiles["profiles"]], ["Concord-RTE", "Concord", "Concord-Planner"])
        self.assertEqual(self.profile("Concord-RTE")["role_source"], ".concord/roles/rte.md")
        self.assertEqual(self.profile("Concord")["role_source"], ".concord/roles/concord.md")
        self.assertEqual(self.profile("Concord-Planner")["role_source"], ".concord/roles/planner.md")

    def test_primary_bash_rules_cannot_reallow_host_denials(self):
        for profile in self.profiles["profiles"]:
            if profile["id"] not in FULL_PROFILES:
                continue
            with self.subTest(agent=profile["id"]):
                self.assertTrue(all(rule["action"] == "deny" for rule in profile["bash"]))
                bash = self.frontmatter(self.projection(profile["id"])).split("\n  bash:\n", 1)[1].split("\n  task:\n", 1)[0]
                self.assertNotIn(": allow", bash)
                self.assertNotIn('"*":', bash)

    def test_every_shell_capable_projection_explicitly_denies_stash(self):
        patterns = ("git stash *", "git * stash *", "*/git stash *", "*/git * stash *")
        entries = [(profile["id"], self.projection(profile["id"])) for profile in self.profiles["profiles"]]
        entries += [(lane["id"], lane_projection(lane)) for lane in self.manifest["lanes"]]
        entries += [(utility["id"], generator.utility_projection(utility)) for utility in self.manifest["utilities"]]
        for name, projection in entries:
            frontmatter = self.frontmatter(projection)
            with self.subTest(agent=name):
                if "\n  bash: deny\n" in frontmatter:
                    continue
                match = re.search(r"\n  bash:\n((?:    .*\n?)*)", frontmatter)
                bash = match[1] if match else ""
                # Every deny follows any utility allowlist or primary rules.
                for pattern in patterns:
                    self.assertIn(f"    {json.dumps(pattern)}: deny", bash)
                self.assertNotIn(": allow", bash[bash.index('    "git stash *": deny'):])

    def test_shared_conduct_forbids_stash_and_saves_permitted_edits_as_commits(self):
        text = " ".join(generator.CONDUCT_CHANGE.read_text(encoding="utf-8").split())
        self.assertIn("Never use `git stash`", text)
        self.assertIn("save work as a commit on the item's own branch", text)

    def test_concord_resume_routes_use_project_only_for_worktree_claim(self):
        text = " ".join(self.projection("Concord").split())
        self.assertIn("`concord_work_start` with `work_id` alone", text)
        self.assertIn("`session_placement`", text)
        self.assertIn("`concord_work_transition.worktree_claim` with that Project's `project_id`", text)
        self.assertNotIn("`work_id` and the second `project_id`", text)

    def test_projection_is_a_selectable_primary_agent(self):
        for agent_id in ("Concord-RTE", "Concord", "Concord-Planner"):
            frontmatter = self.frontmatter(self.projection(agent_id))
            self.assertIn("\nmode: primary\n", "\n" + frontmatter + "\n")
            self.assertNotIn("hidden:", frontmatter)

    def test_projection_body_is_the_role_source_verbatim(self):
        for agent_id in ("Concord-RTE", "Concord", "Concord-Planner"):
            profile = self.profile(agent_id)
            source = (ROOT / profile["role_source"]).read_text(encoding="utf-8")
            self.assertTrue(self.projection(agent_id).endswith("\n---\n\n" + source))

    def test_concord_retry_guidance_states_the_three_attempt_wall(self):
        text = " ".join(self.projection("Concord").split())
        sentence = (
            "At the three-attempt limit, `dispatch_worker` needs a store-derived basis: "
            "shrinking `open_finding_ids` or an approved `supersede_contract` that changes "
            "the approach. An approval cannot open the wall."
        )
        self.assertTrue(sentence in text, "generated Concord prompt must state the exact retry-wall exception")
        for agent_id in ("Concord", "Concord-RTE"):
            self.assertNotIn("To retry it, call `dispatch_worker` with the unchanged approved contract", " ".join(self.projection(agent_id).split()))

    def test_retired_primary_profiles_and_role_sources_are_gone(self):
        # Zero tolerance: the retired profiles and their role sources survive
        # nowhere.
        self.assertFalse((ROOT / ".opencode/agents/concord-coordinator.md").exists())
        self.assertFalse((ROOT / ".concord/roles/coordinator.md").exists())
        self.assertFalse((ROOT / ".opencode/agents/concord-operator.md").exists())
        self.assertFalse((ROOT / ".concord/roles/operator.md").exists())
        # The shipped ids are exactly Concord-RTE and Concord: neither a
        # lowercase nor a wrong-case spelling of either ships, and none is
        # an alias for the real name.
        self.assertFalse((ROOT / ".opencode/agents/concord-rte.md").exists())
        self.assertFalse((ROOT / ".opencode/agents/concord-RTE.md").exists())
        self.assertFalse((ROOT / ".opencode/agents/concord.md").exists())

    def test_rte_grants_the_build_level_host_surface_without_a_profile_ceiling(self):
        frontmatter = self.frontmatter(self.projection("Concord-RTE"))
        for tool in ("edit", "write", "patch"):
            self.assertIn(f"\n  {tool}: allow", frontmatter)
        # morph_edit is host-supplied: the portable profile grants the
        # editor trio and broad task access, and names no host editor.
        self.assertNotIn("morph_edit", frontmatter)
        self.assertNotIn("concord_work_", frontmatter)
        self.assertNotIn("concord_ci_watch", frontmatter)
        self.assertEqual(self.profile("Concord-RTE")["denied_concord_tools"], [])

    def test_rte_keeps_shell_safety_denials_and_unrestricted_tasks(self):
        frontmatter = self.frontmatter(self.projection("Concord-RTE"))
        bash = frontmatter.split("\n  bash:\n", 1)[1].split("\n  task:\n", 1)[0]
        self.assertIn('    "sudo *": deny', bash)
        self.assertIn('    "rm -rf / *": deny', bash)
        task = frontmatter.split("\n  task:\n", 1)[1]
        self.assertEqual(task.splitlines(), ['    "*": allow'])

    def test_concord_grants_the_build_level_host_surface_without_a_profile_ceiling(self):
        # Concord drives one work item end to end: the same Build-level
        # editor grants as the RTE, no workflow denial, and no host editor
        # name in the portable frontmatter.
        frontmatter = self.frontmatter(self.projection("Concord"))
        for tool in ("edit", "write", "patch"):
            self.assertIn(f"\n  {tool}: allow", frontmatter)
        self.assertNotIn("morph_edit", frontmatter)
        self.assertNotIn("concord_work_", frontmatter)
        self.assertNotIn("concord_ci_watch", frontmatter)
        self.assertEqual(self.profile("Concord")["denied_concord_tools"], [])

    def test_concord_keeps_shell_safety_denials_and_unrestricted_tasks(self):
        frontmatter = self.frontmatter(self.projection("Concord"))
        bash = frontmatter.split("\n  bash:\n", 1)[1].split("\n  task:\n", 1)[0]
        self.assertIn('    "sudo *": deny', bash)
        self.assertIn('    "rm -rf / *": deny', bash)
        task = frontmatter.split("\n  task:\n", 1)[1]
        self.assertEqual(task.splitlines(), ['    "*": allow'])

    def test_installed_definitions_equal_the_projections(self):
        for agent_id in ("Concord-RTE", "Concord", "Concord-Planner"):
            path = ROOT / ".opencode/agents" / f"{agent_id}.md"
            self.assertEqual(path.read_text(encoding="utf-8"), self.projection(agent_id))

    def registered_role_ids(self):
        names = [profile["id"] for profile in self.profiles["profiles"]]
        names += [f"concord-{lane['id']}" for lane in self.manifest["lanes"]]
        names += [f"concord-{utility['id']}" for utility in self.manifest["utilities"]]
        return names

    def ts_role_map(self):
        projection = generator.primary_ts_projection(self.profiles, self.manifest)
        body = projection[projection.index("= {") + 2 : projection.rindex("}") + 1]
        return json.loads(body.replace(",\n}", "\n}"))

    def test_ts_projection_carries_the_denied_concord_tools(self):
        projection = generator.primary_ts_projection(self.profiles, self.manifest)
        self.assertIn("DO NOT EDIT", projection)
        self.assertIn('"Concord-RTE"', projection)
        self.assertIn('"Concord"', projection)
        self.assertIn('"concord_work_transition"', projection)
        # One consumed map: the projection exports exactly one binding, so the
        # guard cannot read a second, diverging denial source.
        self.assertEqual(projection.count("export const"), 1)

    def test_ts_projection_covers_exactly_the_registered_roles(self):
        self.assertEqual(sorted(self.ts_role_map()), sorted(self.registered_role_ids()))
        self.assertNotIn("concord-repair", self.ts_role_map())

    def test_ts_projection_denies_workflow_tools_and_gates_the_bound_reader_by_capability(self):
        catalog = generator.concord_tool_ids()
        for kind, entries in (("lane", self.manifest["lanes"]), ("utility", self.manifest["utilities"])):
            for entry in entries:
                agent = f"concord-{entry['id']}"
                denied = self.ts_role_map()[agent]
                granted = bound_worker_tools(entry.get("capabilities", [])) if kind == "lane" else set()
                expected = [tool for tool in catalog if tool not in granted]
                self.assertEqual(sorted(denied["tools"]), expected)
                # The boundary names the role class; the guard composes the
                # agent name and the tool in front of it.
                self.assertIn(kind, denied["boundary"])
                self.assertIn("no general Concord tool access" if kind == "lane" else "no Concord tool access", denied["boundary"])

    def test_ts_projection_keeps_the_profile_entries(self):
        # Neither full primary profile denies a tool, so each entry carries
        # the empty denial list and no refusal boundary. Planner's entry
        # carries its work-state writers and a boundary sentence.
        planner = self.ts_role_map()["Concord-Planner"]
        self.assertEqual(planner["tools"], PLANNER_WRITERS)
        self.assertEqual(planner["tools"], self.profile("Concord-Planner")["denied_concord_tools"])
        self.assertIn("writes work state", planner["boundary"])
        rte = self.ts_role_map()["Concord-RTE"]
        self.assertEqual(rte["tools"], self.profile("Concord-RTE")["denied_concord_tools"])
        self.assertEqual(rte["tools"], [])
        self.assertEqual(rte["boundary"], "")
        concord = self.ts_role_map()["Concord"]
        self.assertEqual(concord["tools"], self.profile("Concord")["denied_concord_tools"])
        self.assertEqual(concord["tools"], [])
        self.assertEqual(concord["boundary"], "")

    def test_lane_and_utility_frontmatter_denials_equal_the_ts_role_map(self):
        # Host visibility (frontmatter denials) and execution denial (the role
        # guard's map) must derive from the same registry population, so the
        # two generated projections must agree tool for tool.
        ts_map = self.ts_role_map()
        catalog = generator.concord_tool_ids()
        for entry in self.manifest["lanes"] + self.manifest["utilities"]:
            agent = f"concord-{entry['id']}"
            text = (ROOT / ".opencode/agents" / f"{agent}.md").read_text(encoding="utf-8")
            denied = sorted(tool for tool in catalog if f"\n  {tool}: false\n" in text)
            self.assertEqual(denied, sorted(ts_map[agent]["tools"]), f"{agent} frontmatter and role map disagree")

    def planner_rules(self, key):
        return prompt_checker.permission_rules(self.frontmatter(self.projection("Concord-Planner")), key)

    def test_planner_edits_resolve_only_under_planning(self):
        # Evaluated with OpenCode's last-match wildcard rules against the
        # worktree-relative path the edit tools ask with.
        rules = self.planner_rules("edit")
        self.assertEqual(rules[0], ("*", "deny"))
        for path, expected in (
            (".planning/notes.md", "allow"),
            (".planning/deep/prototype/main.go", "allow"),
            ("internal/store/store.go", "deny"),
            (".opencode/agents/Concord.md", "deny"),
            ("../sibling/file.go", "deny"),
            ("src/.planning/notes.md", "deny"),
            (".planning-other/notes.md", "deny"),
        ):
            with self.subTest(path=path):
                self.assertEqual(prompt_checker.evaluate(rules, path), expected)

    def test_planner_grants_no_unscoped_editor(self):
        frontmatter = self.frontmatter(self.projection("Concord-Planner"))
        for tool in ("edit", "write", "patch"):
            self.assertNotRegex(frontmatter, rf"(?m)^  {tool}: (?:allow|ask)$")
        self.assertIn("\n  edit:\n", "\n" + frontmatter)

    def test_planner_shell_denies_publication_and_asks_for_unnamed_commands(self):
        rules = self.planner_rules("bash")
        self.assertEqual(rules[0], ("*", "ask"))
        for command, expected in (
            ("git status", "allow"),
            ("git diff --stat", "allow"),
            ("gh pr view 12 --json state", "allow"),
            ("gh pr diff 12", "allow"),
            ("git commit -m change", "deny"),
            ("git push", "deny"),
            ("git push origin HEAD", "deny"),
            ("git merge --ff-only origin/main", "deny"),
            ("git rebase main", "deny"),
            ("git tag v1", "deny"),
            ("git stash push", "deny"),
            ("gh pr create --fill", "deny"),
            ("gh pr merge 12 --auto --squash", "deny"),
            ("gh pr edit 12 --title x", "deny"),
            ("gh release create v1", "deny"),
            ("gh workflow run release.yml", "deny"),
            ("rm -rf / --no-preserve-root", "deny"),
            ("make deploy", "ask"),
            ("python3 scripts/install.py", "ask"),
            ("git checkout -b topic", "ask"),
        ):
            with self.subTest(command=command):
                self.assertEqual(prompt_checker.evaluate(rules, command), expected)

    def test_planner_delegates_only_to_the_read_only_utilities(self):
        rules = self.planner_rules("task")
        utilities = {f"concord-{utility['id']}" for utility in self.manifest["utilities"]}
        self.assertEqual(utilities, {"concord-explore", "concord-lookup", "concord-advisor"})
        for agent in sorted(utilities):
            self.assertEqual(prompt_checker.evaluate(rules, agent), "allow", agent)
        for agent in [f"concord-{lane['id']}" for lane in self.manifest["lanes"]] + ["general", "build", "Concord"]:
            self.assertEqual(prompt_checker.evaluate(rules, agent), "deny", agent)

    def test_planner_frontmatter_denials_equal_the_ts_role_map(self):
        frontmatter = self.frontmatter(self.projection("Concord-Planner"))
        denied = re.findall(r"(?m)^  (concord_\w+): deny$", frontmatter)
        self.assertEqual(denied, PLANNER_WRITERS)
        self.assertEqual(denied, self.ts_role_map()["Concord-Planner"]["tools"])

    def test_path_scoped_edits_beside_an_editor_grant_refuse(self):
        message = self.broken(lambda p: p["profiles"][2]["tools"].update({"write": "allow"}))
        self.assertIn("scopes edits by path", message)

    def test_planner_task_rule_reaching_a_lane_refuses(self):
        message = self.broken(lambda p: p["profiles"][2]["task"].append({"pattern": "concord-implement", "action": "allow"}))
        self.assertIn("concord-implement", message)

    def test_unknown_denied_concord_tool_refuses(self):
        message = self.broken(lambda p: p["profiles"][0]["denied_concord_tools"].append("concord_work_launch"))
        self.assertIn("concord_work_launch", message)

    def test_concord_tool_in_the_general_tool_map_refuses(self):
        message = self.broken(lambda p: p["profiles"][1]["tools"].update({"concord_work_start": "deny"}))
        self.assertIn("denied_concord_tools", message)

    def test_non_dispatching_profile_cannot_reach_a_lane(self):
        # The utility-only Task boundary binds a profile that cannot
        # transition work. The live profiles dispatch freely, so the
        # fixture opts into the non-dispatching posture before the lane
        # rule can refuse.
        def non_dispatching_lane_allow(pattern):
            def mutate(p):
                profile = p["profiles"][0]
                profile["denied_concord_tools"] = ["concord_work_transition"]
                profile["task"] = [{"pattern": "*", "action": "deny"}]
                profile["task"].append({"pattern": pattern, "action": "allow"})
            return mutate

        self.assertIn("concord-implement", self.broken(non_dispatching_lane_allow("concord-implement")))
        self.assertIn("concord-*", self.broken(non_dispatching_lane_allow("concord-*")))

    def test_missing_role_source_refuses(self):
        message = self.broken(lambda p: p["profiles"][0].update({"role_source": ".concord/roles/absent.md"}))
        self.assertIn(".concord/roles/absent.md", message)

    def test_profile_id_outside_the_closed_set_refuses(self):
        message = self.broken(lambda p: p["profiles"][1].update({"id": "concord-implement"}))
        self.assertIn("closed", message)

    def test_role_source_never_names_a_generated_agent(self):
        # The registry descriptions own lane and utility routing; a role body
        # that names one forks that routing into prose.
        names = [f"concord-{entry['id']}" for entry in self.manifest["lanes"] + self.manifest["utilities"]]
        for profile in self.profiles["profiles"]:
            source = (ROOT / profile["role_source"]).read_text(encoding="utf-8")
            for name in names:
                self.assertNotIn(name, source, f"{profile['role_source']} names {name}")

    def test_planner_role_source_states_its_optional_boundary(self):
        source = " ".join((ROOT / ".concord/roles/planner.md").read_text(encoding="utf-8").split())
        for phrase in prompt_checker.PLANNER_REQUIRED_PHRASES:
            self.assertIn(phrase, source)

    def test_role_sources_state_the_optional_rte_rule(self):
        # Concord performs the whole item without an RTE session, and the
        # user may still choose to start one; each role source states the
        # rule from its own side, and neither makes the session mandatory.
        rte = (ROOT / ".concord/roles/rte.md").read_text(encoding="utf-8")
        concord = (ROOT / ".concord/roles/concord.md").read_text(encoding="utf-8")
        self.assertIn("The user may choose to start this optional session", rte)
        self.assertIn("You do not need an RTE session", concord)
        self.assertIn("The user may choose to start an RTE session", concord)

    def test_concord_role_source_carries_the_full_primary_duties(self):
        # The primary agent owns the item end to end. The duty list stays in
        # the role source, not in a lane packet or a host overlay.
        source = (ROOT / ".concord/roles/concord.md").read_text(encoding="utf-8")
        self.assertIn(
            "You own design, code, tests, review, law text, publication, CI follow-through, deployment, and planning for your work item.",
            " ".join(source.split()),
        )


if __name__ == "__main__":
    unittest.main()
