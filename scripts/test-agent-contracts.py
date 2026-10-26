#!/usr/bin/env python3
import copy, importlib.util, io, json, os, re, shutil, subprocess, sys, tempfile, unittest, unittest.mock
from contextlib import ExitStack, redirect_stderr, redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("generator", ROOT / "scripts/generate-agent-contracts.py")
generator = importlib.util.module_from_spec(spec); spec.loader.exec_module(generator)
manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
ir = json.loads((ROOT / "contracts/agent-tool-surface.schema.json").read_text())
checker_spec = importlib.util.spec_from_file_location("lane_checker", ROOT / "scripts/check-agent-contracts.py")
lane_checker = importlib.util.module_from_spec(checker_spec); checker_spec.loader.exec_module(lane_checker)
lanes_schema = json.loads((ROOT / "contracts/agent-lanes.schema.json").read_text())
report_schema = json.loads((ROOT / "contracts/agent-lane-report.schema.json").read_text())
lane_registry = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text())
packet_schema = json.loads((ROOT / "contracts/agent-lane-packet.schema.json").read_text())
envelope_schema = json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text())
payload_schema = json.loads((ROOT / "contracts/agent-tool-surface-payloads.schema.json").read_text())

# CD-0218 D1/D2 absence witnesses, not a production action catalog.
RETIRED_PUBLISHED_ACTIONS = {
    "accept_decision", "add_condition", "approve_operation", "cancel_condition",
    "checkpoint_action", "checkpoint_analysis", "checkpoint_poc", "checkpoint_run",
    "cleanup_run", "declare_scope", "discard_poc", "frame_question", "record_decision",
    "record_health", "record_option", "record_research", "resolve_condition", "rollback_run",
    "run_analysis", "start_action", "start_poc", "start_run", "record_reproduction",
    "record_root_cause", "start_repair", "start_refine", "record_worker_failure", "record_discovery",
}


class DomainFactProjectionTests(unittest.TestCase):
    def test_delegated_shape_reading_projects_the_same_closed_source(self):
        packet = copy.deepcopy(packet_schema)
        lanes_generator.project_domain_shape_reading(packet)
        lanes_generator.project_domain_shape_reading(packet)
        source = {"kind": "domain_shape", "source_id": "project", "commit_oid": "a" * 40,
                  "content_hash": "sha256:" + "b" * 64, "selector": {"path": "source/main.go"}}
        reading = {"domain_id": "root", "reason": "inspect selected shape", "source": source}
        schema = packet["$defs"]["lane_work_context_reading"]
        generator.schema_validate(reading, schema, packet)
        self.assertEqual(len(schema["properties"]["source"]["oneOf"]), 3)
        for extra in ["command", "law_id", "project_id"]:
            with self.subTest(extra=extra):
                bad = copy.deepcopy(reading)
                bad["source"][extra] = "not-admitted"
                with self.assertRaises(ValueError):
                    generator.schema_validate(bad, schema, packet)
        source["selector"]["domain_id"] = "root"
        with self.assertRaises(ValueError):
            generator.schema_validate(reading, schema, packet)

    def test_fact_projection_resolves_local_refs_and_preserves_path_rules(self):
        source = json.loads((ROOT / "contracts/domain-fact.v1.schema.json").read_text())
        projected = {"$defs": {}}
        generator.project_domain_fact_schema(projected)
        generator.check_schema_keywords(projected)
        generator.check_payload_closed(projected)
        # Exercise both the source's lookahead and the equivalent RE2-friendly
        # not constraints. Go round-trip tests exercise the projected validator.
        for path in ["source", "source/main.go", "one/two/three", "/source", "a//b", "a/", ".", "..", "a/../b", "a/./b", "a\\b", "a*b", "a?b", "a[b]", "a{b}"]:
            with self.subTest(path=path):
                def accepts(schema, root):
                    try:
                        generator.schema_validate(path, schema, root)
                        return True
                    except ValueError:
                        return False
                self.assertEqual(accepts(source["$defs"]["path"], source),
                                 accepts(projected["$defs"]["domain_fact_path"], projected))
        for name in source["$defs"]:
            self.assertIn("domain_fact_" + name, projected["$defs"])


class IndependentPartIntegrationTests(unittest.TestCase):
    """Exercise composition identity and real regeneration on disposable source bytes."""

    PROBE = r'''
package main

import (
    "encoding/json"
    "errors"
    "os"

    "github.com/sharper-flow/concord/internal/store"
)

func must(err error) {
    if err != nil { panic(err) }
}

func main() {
    previous := map[string][]string{}
    must(json.NewDecoder(os.Stdin).Decode(&previous))
    registry := store.NewWorkflowDefinitionRegistry()
    result := map[string]any{}
    for _, definition := range store.BuiltinWorkflowDefinitions() {
        registered, err := registry.Register(definition)
        must(err)
        current, ok := registry.Current(definition.Ref)
        if !ok { panic("current definition missing") }
        digest, err := store.WorkflowDefinitionDigest(current.Definition)
        must(err)
        if digest != current.Digest || digest != registered.Digest {
            panic("current definition digest differs from registered content")
        }
        must(registry.Verify(definition.Ref, digest))
        canonical, err := store.CanonicalWorkflowDefinition(current.Definition)
        must(err)
        rejected := []string{}
        for _, old := range previous[definition.Ref] {
            if old == digest {
                must(registry.Verify(definition.Ref, old))
                continue
            }
            var failure *store.Failure
            err := registry.Verify(definition.Ref, old)
            if !errors.As(err, &failure) || failure.Kind != store.KindDefinitionDigestMismatch {
                panic("old definition digest did not receive a typed mismatch refusal")
            }
            rejected = append(rejected, old)
        }
        result[definition.Ref] = map[string]any{
            "digest": digest, "manifest": json.RawMessage(canonical), "rejected": rejected,
        }
    }
    must(json.NewEncoder(os.Stdout).Encode(result))
}
'''

    def _run(self, root, *argv, input=None, show=True):
        # The proof uses cached dependencies only, never an install or network fetch.
        result = subprocess.run(
            argv, cwd=root, input=input, text=True, capture_output=True,
            timeout=240, env=os.environ | {"GOPROXY": "off", "GOSUMDB": "off"},
        )
        if show or result.returncode:
            print(f"$ {' '.join(map(str, argv))}\n{result.stdout}{result.stderr}", flush=True)
        self.assertEqual(result.returncode, 0, f"command failed: {argv}")
        return result.stdout

    def _assert_no_content_counters(self, root):
        for filename, names in (
            ("workflow_parts.go", ("WorkflowPart", "WorkflowPartUse")),
            ("workflow_composition.go", ("WorkflowComposition",)),
        ):
            source = (root / "internal/store" / filename).read_text()
            for name in names:
                body = re.search(rf"type {name} struct \{{(.*?)\n\}}", source, re.S)
                self.assertIsNotNone(body, f"missing {name} declaration")
                self.assertNotRegex(body[1], r"(?m)^\s*Version\s+", f"retained {name}.Version content counter")
            self.assertNotIn("ActionCatalogVersion", source, "retained action catalog content counter")

    def test_independent_part_edits_merge_and_regenerate_without_version_edits(self):
        self._assert_no_content_counters(ROOT)
        with tempfile.TemporaryDirectory(prefix="concord-part-integration-") as temporary:
            scratch = Path(temporary)
            root = scratch / "source"
            root.mkdir()
            tracked = self._run(ROOT, "git", "ls-files", "-z", show=False).split("\0")
            excluded = {".git", "node_modules", "__pycache__", ".ruff_cache", "build", "dist"}
            for relative in filter(None, tracked):
                path = Path(relative)
                self.assertFalse(path.is_absolute() or ".." in path.parts)
                if excluded.intersection(path.parts):
                    continue
                source = ROOT / path
                self.assertFalse(source.is_symlink(), f"tracked source symlink: {relative}")
                target = root / path
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source, target)
            self.assertFalse((root / ".git").exists())

            parts = root / "internal/store/workflow_parts.go"
            base = parts.read_text()
            # Reorder existing actions in distant, disjoint parts, not shared counters.
            edits = (
                ('"record_alignment", "record_work_context"', '"record_work_context", "record_alignment"'),
                ('"record_report", "link_successor"', '"link_successor", "record_report"'),
            )
            copies = []
            for name, source in (("base", base), ("left", base.replace(*edits[0])), ("right", base.replace(*edits[1]))):
                path = scratch / f"{name}.go"
                path.write_text(source)
                copies.append(path)
            for old, new in edits:
                self.assertEqual(base.count(old), 1)
                self.assertNotIn(new, base)
            merged = self._run(scratch, "git", "merge-file", "-p", str(copies[1]), str(copies[0]), str(copies[2]), show=False)
            self.assertEqual(merged, base.replace(*edits[0]).replace(*edits[1]))
            for old, new in edits:
                self.assertNotIn(old, merged)
                self.assertEqual(merged.count(new), 1)

            probe = root / "scripts/part-integration-probe/main.go"
            probe.parent.mkdir()
            probe.write_text(self.PROBE)
            states = {}
            previous = {}
            for name, source in (("base", base), ("left", copies[1].read_text()), ("right", copies[2].read_text()), ("merged", merged)):
                parts.write_text(source)
                self._assert_no_content_counters(root)
                states[name] = json.loads(self._run(root, "go", "run", "./scripts/part-integration-probe", input=json.dumps(previous), show=False))
                self.assertEqual(set(states[name]), {"workflow.implementation", "workflow.break_fix", "workflow.research"})
                for ref, row in states[name].items():
                    self.assertRegex(row["digest"], r"^sha256:[0-9a-f]{64}$")
                    self.assertNotIn("version", row["manifest"])
                    self.assertIn("capabilities", row["manifest"])
                    self.assertTrue(row["manifest"]["capabilities"])
                    self.assertEqual(row["rejected"], [old for old in previous.get(ref, []) if old != row["digest"]])
                print(f"{name} digests: {json.dumps({ref: row['digest'] for ref, row in states[name].items()}, sort_keys=True)}", flush=True)
                for ref, row in states[name].items():
                    previous.setdefault(ref, []).append(row["digest"])

            for ref in states["base"]:
                expected_left = copy.deepcopy(states["base"][ref]["manifest"])
                expected_right = copy.deepcopy(expected_left)
                expected_merged = copy.deepcopy(expected_left)
                for step in expected_left["step_graph"]["steps"]:
                    if step["id"] == "alignment":
                        step["actions"][0:2] = list(reversed(step["actions"][0:2]))
                for step in expected_right["step_graph"]["steps"]:
                    if step["id"] == "findings":
                        step["actions"][0:2] = list(reversed(step["actions"][0:2]))
                for step in expected_merged["step_graph"]["steps"]:
                    if step["id"] in ("alignment", "findings"):
                        step["actions"][0:2] = list(reversed(step["actions"][0:2]))
                # Root available/action-definition ordering may also change with first use.
                for name, expected in (("left", expected_left), ("right", expected_right), ("merged", expected_merged)):
                    self.assertEqual(states[name][ref]["manifest"]["step_graph"], expected["step_graph"])
                    changed = ref == "workflow.implementation" and name in ("left", "merged") or ref == "workflow.research" and name in ("right", "merged")
                    self.assertEqual(states[name][ref]["digest"] != states["base"][ref]["digest"], changed, (name, ref))
                owner = "left" if ref == "workflow.implementation" else "right" if ref == "workflow.research" else "base"
                self.assertEqual(states["merged"][ref]["manifest"], states[owner][ref]["manifest"])
                self.assertEqual(states["merged"][ref]["digest"], states[owner][ref]["digest"])

            print("Independent edits merged exactly; current digests verified and stale affected digests received typed mismatch refusals.", flush=True)
            self._run(root, sys.executable, "scripts/generate-agent-contracts.py")
            for row in states["merged"].values():
                exported = json.loads(self._run(root, "go", "run", "./scripts/workflow-action-contracts", "--canonical-definition",
                                                input=json.dumps(row["manifest"]), show=False))
                self.assertEqual(exported.pop("digest"), row["digest"])
                self.assertEqual(exported, row["manifest"])
            print("Generator canonical exporter agrees with all merged current definitions.", flush=True)
            self._run(root, sys.executable, "scripts/generate-agent-contracts.py", "--check")
            self._run(root, sys.executable, "scripts/generate-agent-lanes.py", "--check")
            self._run(root, "go", "test", "-timeout=4m", "./internal/store", "-run",
                      "^Test(WorkflowDefinitionCanonicalEncodingMatchesPublicFixture|WorkflowCapabilityVocabularyMatchesPublicSchema|WorkflowDefinitionCanonicalDigestAndDriftProtection)$", "-count=1", "-v")
            self.assertEqual(parts.read_text(), merged)
            self._assert_no_content_counters(root)


class CurrentScenarioCarrierTests(unittest.TestCase):
    def setUp(self):
        self.schema_path = ROOT / "contracts/workflow-engine-scenarios.schema.json"
        corpus = json.loads((ROOT / ".concord/scenarios/workflow-engine.v1.json").read_text())
        self.current = next(item for item in corpus["scenarios"] if "current_state" in item.get("setup", {}))
        self.corpus = {"scenarios": [self.current]}

    def test_current_carrier_checks_need_no_historical_scenario(self):
        self.assertEqual(lane_checker._current_scenario_carrier_check(self.corpus), [])

    def test_current_operation_rejects_recorded_execution_members(self):
        schema = lane_checker._load_json(self.schema_path)
        operation = {"op_id": self.current["request"]["operation"]["op_id"]}
        generator.schema_validate(operation, schema["$defs"]["operationInput"], schema)
        for member, value in (
            ("attempt_epoch", 1),
            ("step_id", "proposal"),
            ("step_kind", "internal_sqlite"),
            ("state", "pending"),
        ):
            with self.subTest(member=member):
                with self.assertRaisesRegex(ValueError, rf"unknown properties.*{member}"):
                    generator.schema_validate(operation | {member: value}, schema["$defs"]["operationInput"], schema)

    def test_actor_identity_refs_accept_real_fixture_slashes(self):
        schema = lane_checker._load_json(self.schema_path)
        corpus = lane_checker._load_json(ROOT / ".concord/scenarios/workflow-engine.v1.json")
        actor = next(actor for actor in corpus["fixtures"]["actors"] if actor["agent_ref"] == "agent/owner")
        grant = {key: actor[key] for key in ("principal_ref", "client_ref", "agent_ref", "session_ref")}
        grant.update(capability="workflow_action", scope="project")
        for name, value in (("actor", actor), ("grantInput", grant)):
            with self.subTest(schema=name):
                generator.schema_validate(value, schema["$defs"][name], schema)
                old_rule = copy.deepcopy(schema)
                old_rule["$defs"]["actorIdentityRef"] = old_rule["$defs"]["id"]
                with self.assertRaisesRegex(ValueError, "pattern"):
                    generator.schema_validate(value, old_rule["$defs"][name], old_rule)

    def test_actor_identity_refs_refuse_unicode_whitespace(self):
        schema = lane_checker._load_json(self.schema_path)
        corpus = lane_checker._load_json(ROOT / ".concord/scenarios/workflow-engine.v1.json")
        actor = next(actor for actor in corpus["fixtures"]["actors"] if actor["agent_ref"] == "agent/owner")
        keys = ("principal_ref", "client_ref", "agent_ref", "session_ref")
        grant = {key: actor[key] for key in keys} | {"capability": "workflow_action", "scope": "project"}
        spaces = "\t\n\v\f\r \u0085\u00a0\u1680" + "".join(chr(code) for code in range(0x2000, 0x200b)) + "\u2028\u2029\u202f\u205f\u3000"
        for name, value in (("actor", actor), ("grantInput", grant)):
            for key in keys:
                for space in spaces:
                    with self.subTest(schema=name, field=key, whitespace=hex(ord(space))):
                        with self.assertRaisesRegex(ValueError, "pattern"):
                            generator.schema_validate(value | {key: value[key] + space + "other"}, schema["$defs"][name], schema)

    def test_actor_identity_rule_does_not_relax_scenario_ids(self):
        schema = lane_checker._load_json(self.schema_path)
        generator.schema_validate("session/work-alpha", schema["$defs"]["actorIdentityRef"], schema)
        with self.assertRaisesRegex(ValueError, "pattern"):
            generator.schema_validate("WF02/planning", schema["$defs"]["id"], schema)

    def test_full_checker_reports_missing_inventory_without_historical_lookup(self):
        corpus_path = ROOT / ".concord/scenarios/workflow-engine.v1.json"
        load = lane_checker._load_json
        corpus = load(corpus_path)
        corpus["scenarios"] = [item for item in corpus["scenarios"] if item["id"] != "WF26-condition-actions-refused"]
        with unittest.mock.patch.object(lane_checker, "_load_json", side_effect=lambda path: corpus if path == corpus_path else load(path)):
            findings = lane_checker.check_workflow_contracts()
        self.assertTrue(any("scenario" in finding and "got" in finding for finding in findings), findings)
        self.assertFalse(any("current-scenario" in finding for finding in findings), findings)

    def test_each_current_carrier_rule_has_a_negative_control(self):
        original = lane_checker._load_json(self.schema_path)
        for mutation in ("history", "pin", "missing_step", "unknown_current_field"):
            with self.subTest(mutation=mutation):
                schema = copy.deepcopy(original)
                if mutation == "history":
                    schema["$defs"]["scenarioSetup"]["additionalProperties"] = True
                elif mutation == "pin":
                    schema["$defs"]["scenarioRequest"]["additionalProperties"] = True
                elif mutation == "missing_step":
                    schema["$defs"]["scenarioSetup"]["properties"]["current_state"]["required"] = []
                else:
                    schema["$defs"]["scenarioSetup"]["properties"]["current_state"]["additionalProperties"] = True
                with unittest.mock.patch.object(lane_checker, "_load_json", return_value=schema):
                    findings = lane_checker._current_scenario_carrier_check(self.corpus)
                self.assertEqual(findings, [f"current-scenario-{mutation}: schema accepted malformed current fixture"])

    def test_invalid_baseline_cannot_supply_negative_controls(self):
        del self.current["setup"]["current_state"]["step"]
        findings = lane_checker._current_scenario_carrier_check(self.corpus)
        self.assertEqual(len(findings), 1)
        self.assertIn("current-scenario-baseline:", findings[0])

    def test_missing_current_fixture_is_reported(self):
        self.assertEqual(lane_checker._current_scenario_carrier_check({"scenarios": []}), ["current-scenario-baseline: no current-state scenario exists"])

    def test_workflow_contract_checker_accepts_current_inventory(self):
        self.assertEqual(lane_checker.check_workflow_contracts(), [])

    def test_pipeline_inventory_requires_owner_and_worker_cases(self):
        corpus_path = ROOT / ".concord/scenarios/workflow-engine.v1.json"
        original = lane_checker._load_json(corpus_path)
        load = lane_checker._load_json
        for missing in ("WF48-owner-accepts-typed-result", "WF48-worker-self-accept-refused"):
            with self.subTest(missing=missing):
                corpus = copy.deepcopy(original)
                pipeline = next(item for item in corpus["scenarios"] if item["scenario_number"] == 48)
                pipeline["cases"] = [case for case in pipeline["cases"] if case["id"] != missing]
                with unittest.mock.patch.object(lane_checker, "_load_json", side_effect=lambda path: corpus if path == corpus_path else load(path)):
                    findings = lane_checker.check_workflow_contracts()
                self.assertTrue(any("expected current cases" in finding and missing in finding for finding in findings), findings)

    def test_native_requests_keep_their_required_fields(self):
        original = lane_checker._load_json(ROOT / ".concord/scenarios/workflow-engine.v1.json")
        for action, field in (("approve_contract", "premise"), ("record_proposal", "problem"), ("record_verdict", "verdicts"), ("complete", "impact_verdict"), ("accept_worker_result", "attempt_id")):
            with self.subTest(action=action, field=field):
                corpus = copy.deepcopy(original)
                scenario = next(item for item in corpus["scenarios"] if item["action"] == action)
                del scenario["request"]["fields"][field]
                findings = lane_checker._structured_scenario_check(corpus)
                self.assertTrue(any("mandatory structured request field" in finding and field in finding for finding in findings), findings)

    def test_checker_mutations_reach_current_safeguards(self):
        corpus_path = ROOT / ".concord/scenarios/workflow-engine.v1.json"
        original = lane_checker._load_json(corpus_path)
        for mutation, expected in (
            ("actor", "actor-ID derivation mismatch"),
            ("scope", "request scope mismatch"),
            ("definition", "dangling fixture reference definition"),
            ("error", "undeclared scenario error kind"),
        ):
            with self.subTest(mutation=mutation):
                corpus = copy.deepcopy(original)
                scenario = corpus["scenarios"][0]
                if mutation == "actor":
                    scenario["request"]["grant"]["agent_ref"] = "agent:other"
                elif mutation == "scope":
                    scenario["request"]["grant"]["scope"] = "project:other"
                elif mutation == "definition":
                    scenario["request"]["definition_ref"] = "workflow.missing"
                else:
                    condition = next(item for item in corpus["scenarios"] if item["id"] == "WF26-condition-actions-refused")
                    condition["cases"][0]["expected"]["assertions"][0]["value"] = "not_a_real_error_kind"
                load = lane_checker._load_json
                with unittest.mock.patch.object(lane_checker, "_load_json", side_effect=lambda path: corpus if path == corpus_path else load(path)):
                    findings = lane_checker.check_workflow_contracts()
                self.assertTrue(any(expected in finding for finding in findings), findings)


class ContractGuidanceProjectionTests(unittest.TestCase):
    def test_guidance_keeps_schema_key_order(self):
        document = {"title": "Example", "$defs": {"code": {"type": "string", "minLength": 2}}}
        projected = generator.project_schema_guidance(document, ["code"])
        self.assertEqual(list(projected), list(document))
        self.assertEqual(list(projected["$defs"]["code"])[:2], ["type", "minLength"])

    def test_examples_are_instances_not_subschemas(self):
        generator.check_schema_keywords({"type": "object", "examples": [{"approval_ref": "sample"}]})
        with self.assertRaisesRegex(ValueError, "unsupported JSON Schema keywords"):
            generator.check_schema_keywords({"items": {"unknown_constraint": True}})

    def test_contract_fields_publish_schema_derived_examples(self):
        actions, workflows, teaching, _ = generator.load_workflow_action_contracts()
        projected, _ = generator.project_workflow_action_schema(payload_schema, actions, workflows, teaching)
        for name in ("workflow_outcome_subject", "workflow_outcome_surface"):
            with self.subTest(schema=name):
                field = projected["$defs"][name]
                self.assertIn("Accepted shape:", field.get("description", ""))
                self.assertEqual(len(field.get("examples", [])), 1)
                generator.schema_validate(field["examples"][0], field, projected)

_SCRIPTS_DIR = str(ROOT / "scripts")
if _SCRIPTS_DIR not in sys.path:
    sys.path.insert(0, _SCRIPTS_DIR)

# Preserve real tool boundaries while individual controls patch discovery
# and reject Bun subprocesses.
_REAL_SUBPROCESS_RUN = subprocess.run
_REAL_WHICH = shutil.which


def _load_cli_module(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


lanes_generator = _load_cli_module("cli_safety_lanes", "generate-agent-lanes.py")
work_kinds_generator = _load_cli_module("cli_safety_work_kinds", "generate-work-kinds.py")
native_run_statuses_generator = _load_cli_module("cli_safety_native_run_statuses", "generate-native-run-statuses.py")
work_sizes_generator = _load_cli_module("cli_safety_work_sizes", "generate-work-sizes.py")
relation_vocabulary_generator = _load_cli_module("cli_safety_relation_vocabulary", "generate-relation-vocabulary.py")
durable_tier_budget_generator = _load_cli_module("cli_safety_durable_tier_budget", "generate-durable-tier-budget.py")
branch_freshness_generator = _load_cli_module("cli_safety_branch_freshness", "generate-branch-freshness.py")
lane_step_dispatch_generator = _load_cli_module("cli_safety_lane_step_dispatch", "generate-lane-step-dispatch.py")
adapter_oracle_generator = _load_cli_module("adapter_oracle_generator", "generate-adapter-oracle.py")


class AdapterOracleGenerationTests(unittest.TestCase):
    def test_committed_outputs_match_sources(self):
        for path, expected in adapter_oracle_generator.render().items():
            with self.subTest(path=path):
                self.assertEqual(path.read_text(), expected)

    def test_checks_and_helpers_are_pinned_existing_files(self):
        recipe = json.loads(adapter_oracle_generator.render()[adapter_oracle_generator.OUTPUT])
        harness = recipe["harness_files"]
        self.assertLessEqual(len(harness), 16)
        for name in harness:
            self.assertTrue((ROOT / name).is_file(), name)
        for check in recipe["checks"]:
            for name in check["argv"][1:]:
                self.assertIn(f"adapter/opencode/{name}", harness)

    def test_a_dependency_change_cannot_regenerate_an_empty_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            package = Path(directory) / "package.json"
            package.write_text(json.dumps({"name": "adapter", "private": True, "dependencies": {"unexpected": "1.0.0"}}))
            with unittest.mock.patch.object(adapter_oracle_generator, "PACKAGE", package):
                with self.assertRaisesRegex(SystemExit, "must remain dependency-free"):
                    adapter_oracle_generator.render()

class LaneSelectabilityTests(unittest.TestCase):
    def check_definition(self, mode, hidden=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "contracts").mkdir()
            (root / ".opencode/agents").mkdir(parents=True)
            (root / "contracts/agent-lanes.v1.json").write_text(json.dumps({"lanes": [{"id": "review"}]}))
            definition = f"---\nmode: {mode}\n" + ("hidden: true\n" if hidden else "") + "---\n"
            (root / ".opencode/agents/concord-review.md").write_text(definition)
            with unittest.mock.patch.object(lane_checker, "ROOT", root):
                return lane_checker._check_lane_agent_selectability()

    def test_hidden_subagent_is_dispatchable(self):
        self.assertEqual(self.check_definition("subagent"), [])

    def test_primary_capable_lane_is_refused(self):
        for mode in ("primary", "all"):
            with self.subTest(mode=mode):
                self.assertTrue(any("mode: subagent" in finding for finding in self.check_definition(mode)))

    def test_visible_lane_is_refused(self):
        self.assertTrue(any("hidden: true" in finding for finding in self.check_definition("subagent", hidden=False)))

class PublicationPinGenerationTests(unittest.TestCase):
    def test_pin_measures_the_production_publication(self):
        with unittest.mock.patch.object(generator.subprocess, "run") as run:
            run.return_value.stdout = "135199\n"
            projection = generator.ts_publication_pin_projection()
        self.assertIn("export const PINNED_PUBLISHED_TOTAL_BYTES = 135199;", projection)
        argv = run.call_args.args[0]
        self.assertEqual(argv[:2], ["bun", "-e"])
        self.assertIn("publishedRequestSchema(tool)", argv[2])
        self.assertIn("Buffer.byteLength", argv[2])
        self.assertEqual(run.call_args.kwargs["cwd"], ROOT)

    def test_pin_refuses_invalid_measurements(self):
        for measurement in ["0", "-1", "true", "1.5", '"135199"', "{}", "noise"]:
            with self.subTest(measurement=measurement):
                with unittest.mock.patch.object(generator.subprocess, "run") as run:
                    run.return_value.stdout = measurement
                    with self.assertRaises(ValueError):
                        generator.ts_publication_pin_projection()

    def test_measurement_failure_is_not_a_pin(self):
        with unittest.mock.patch.object(generator.subprocess, "run") as run:
            run.side_effect = subprocess.CalledProcessError(1, "bun")
            with self.assertRaises(subprocess.CalledProcessError):
                generator.ts_publication_pin_projection()


class GeneratorWithoutBunTests(unittest.TestCase):
    """Run real deterministic projections while forbidding Bun and file writes.

    Drift controls intercept generated-file reads, never edit generated files.
    """

    BUN_MISSING = FileNotFoundError(2, "No such file or directory", "bun")

    @staticmethod
    def _which_without_bun(name):
        return None if name == "bun" else _REAL_WHICH(name)

    @staticmethod
    def _which_with_bun(name):
        return "/fake/bun" if name == "bun" else _REAL_WHICH(name)

    @staticmethod
    def _refuse(action):
        def guard(*args, **kwargs):
            raise AssertionError(f"generator attempted {action} without Bun")
        return guard

    @staticmethod
    def _guarded_run(attempts, bun_error):
        def guarded(args, **kwargs):
            if str(args[0]) == "bun":
                attempts.append([str(argument) for argument in args])
                raise bun_error
            return _REAL_SUBPROCESS_RUN(args, **kwargs)
        return guarded

    @staticmethod
    def _replace_pin(text, replacement):
        head, separator, tail = text.rpartition("\nexport const PINNED_PUBLISHED_TOTAL_BYTES = ")
        assert separator, "fixture lost the publication pin declaration"
        return head + separator + replacement.replace("{n}", tail.split(";", 1)[0]) + "\n"

    def _main(self, *, check=True, require_bun=False, bun_present=False, mutate_read=None):
        attempts: list[list[str]] = []
        real_read_text = Path.read_text
        patches = [
            unittest.mock.patch.object(
                generator.subprocess, "run",
                self._guarded_run(attempts, subprocess.CalledProcessError(1, "bun") if bun_present else self.BUN_MISSING),
            ),
            unittest.mock.patch("shutil.which", self._which_with_bun if bun_present else self._which_without_bun),
            unittest.mock.patch.dict(os.environ, {"CONCORD_REQUIRE_BUN": "1"} if require_bun else {}),
        ]
        if mutate_read is not None:
            def intercepted(path, *args, **kwargs):
                text = real_read_text(path, *args, **kwargs)
                return mutate_read(text) if path.name == "generated-contract-tests.ts" else text
            patches.append(unittest.mock.patch.object(Path, "read_text", intercepted))
        if not bun_present:
            for target in ("pathlib.Path.write_text", "pathlib.Path.mkdir"):
                patches.append(unittest.mock.patch(target, self._refuse(target.rsplit(".", 1)[1].replace("_", " "))))
        out, err = io.StringIO(), io.StringIO()
        with ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            if not require_bun:
                os.environ.pop("CONCORD_REQUIRE_BUN", None)
            with redirect_stdout(out), redirect_stderr(err):
                with unittest.mock.patch.object(sys, "argv", ["generate-agent-contracts.py"] + (["--check"] if check else [])):
                    code = generator.main()
        return code, attempts, out.getvalue(), err.getvalue()

    def _assert_no_bun_refusal(self, code, err):
        # The refusal must be the generator's own decision naming the drifted
        # file or the publication pin it refused, never a crash on the
        # missing Bun binary.
        self.assertEqual(code, 1)
        self.assertTrue("generated-contract-tests.ts" in err or "PINNED_PUBLISHED_TOTAL_BYTES" in err, err)

    def test_check_without_bun_validates_real_projections_and_skips_measurement(self):
        code, attempts, out, err = self._main()
        self.assertEqual(code, 0, err)
        self.assertEqual(attempts, [], "a no-Bun check must never spawn a Bun subprocess")
        self.assertIn("NOT run", out + err)
        self.assertIn("publication", (out + err).lower())

    def test_check_without_bun_fails_when_the_validator_body_drifts(self):
        def drift(text):
            return text.replace("export function validateGeneratedPayload(", "export function validateGeneratedPayloadX(", 1)
        code, _, _, err = self._main(mutate_read=drift)
        self._assert_no_bun_refusal(code, err)

    def test_check_without_bun_refuses_a_malformed_or_missing_publication_pin(self):
        for name, mutate in (
            ("zero", lambda text: self._replace_pin(text, "0;")),
            ("not-an-integer", lambda text: self._replace_pin(text, '"116278";')),
            ("missing", lambda text: text.rpartition("\nexport const PINNED_PUBLISHED_TOTAL_BYTES = ")[0] + "\n"),
        ):
            with self.subTest(pin=name):
                code, _, _, err = self._main(mutate_read=mutate)
                self._assert_no_bun_refusal(code, err)

    def test_check_without_bun_accepts_no_content_after_the_pin(self):
        code, _, _, err = self._main(mutate_read=lambda text: self._replace_pin(text, "{n};\n// content after the pin\n"))
        self._assert_no_bun_refusal(code, err)

    def test_check_without_bun_fails_closed_under_the_strict_environment(self):
        code, attempts, _, err = self._main(require_bun=True)
        self.assertEqual(code, 1)
        self.assertEqual(attempts, [], "the strict refusal must not reach a Bun subprocess")
        self.assertIn("bun", err.lower())

    def test_write_mode_without_bun_refuses_before_any_write(self):
        code, attempts, _, err = self._main(check=False)
        self.assertEqual(code, 1)
        self.assertEqual(attempts, [], "write mode must refuse on missing Bun, not crash mid-workload")
        self.assertIn("bun", err.lower())

    def test_with_bun_a_failed_measurement_still_fails_main(self):
        code, attempts, _, err = self._main(bun_present=True)
        self.assertEqual(code, 1)
        self.assertEqual(len(attempts), 1, "the publication measurement must still be attempted")
        self.assertEqual(attempts[0][:2], ["bun", "-e"])


class ManifestTamperTests(unittest.TestCase):
    def assert_rejected(self, value):
        with self.assertRaises(ValueError):
            generator.validate_persisted_manifest(value, ir)

    def test_unknown_field(self):
        value=copy.deepcopy(manifest); value["unexpected"]=True; self.assert_rejected(value)
    def test_invalid_enum(self):
        value=copy.deepcopy(manifest); value["operations"][0]["capability"]="not-a-capability"; self.assert_rejected(value)
    def test_missing_operation_pairing(self):
        value=copy.deepcopy(manifest); value["tools"][0]["operations"].pop()
        with self.assertRaises(ValueError): generator.validate(value)
    def test_changed_schema_ref(self):
        value=copy.deepcopy(manifest); value["schemas"]["product_context"]["ref"]="contracts/missing.json#/$defs/product_context"
        with self.assertRaises(ValueError): generator.validate(value)
    def test_duplicate_id(self):
        value=copy.deepcopy(manifest); value["operations"][1]["id"]=value["operations"][0]["id"]
        with self.assertRaises(ValueError): generator.validate(value)
    def test_missing_coverage(self):
        value=copy.deepcopy(manifest); value["tools"][0]["operations"].append("concord_product_view.missing")
        with self.assertRaises(ValueError): generator.validate(value)

class ToolDescriptionTests(unittest.TestCase):
    def test_domain_description_matches_the_read_only_navigation_surface(self):
        tool = next(tool for tool in manifest["tools"] if tool["id"] == "concord_domain")
        self.assertEqual(tool["operations"], [
            "concord_domain.list", "concord_domain.detail",
            "concord_domain.attachments", "concord_domain.overlaps",
            "concord_domain.footprint", "concord_domain.shape",
        ])
        self.assertEqual(tool["description"],
                          "Navigate Product architecture: current Domains, current law, "
                          "attachments, unresolved overlap, the footprint of a change, and typed Domain Shape selected by one path, Domain or subject. Shape results are bounded to 32768 bytes and never execute Product checks.")

    def test_tool_descriptions_do_not_name_retired_operations(self):
        retired = [
            "concord_domain.active_work",
            "concord_domain.observation_record",
            "concord_domain.observation_dismiss",
            "concord_work_relate.link",
            "concord_work_relate.unlink",
            "concord_work_relate.supersede",
            "concord_work_relate.restore_superseded",
            "concord_product_view.portfolio",
            "concord_work_compact.lesson_publish",
            "concord_knowledge.unprocessed",
        ]
        registered = {operation["id"] for operation in manifest["operations"]}
        for operation in retired:
            owner, name = operation.split(".")
            with self.subTest(operation=operation):
                self.assertNotIn(operation, registered)
                for tool in manifest["tools"]:
                    self.assertNotRegex(tool["description"],
                                        re.compile(r"\b" + re.escape(operation) + r"\b", re.I))
                    if tool["id"] == owner:
                        self.assertNotRegex(tool["description"],
                                            re.compile(r"\b" + re.escape(name) + r"\b", re.I))


class WorkStartHostContractTests(unittest.TestCase):
    def setUp(self):
        host = json.loads((ROOT / "contracts/host-tool-surface.v1.json").read_text())
        self.tool = next(tool for tool in host["tools"] if tool["name"] == "concord_work_start")

    def edited(self, change):
        tool = copy.deepcopy(self.tool)
        change(tool["args"]["oneOf"][0], tool["args"]["oneOf"][1])
        return tool

    def test_shipped_capture_and_resume_selector_xor_validate(self):
        generator.validate_work_start_host_tool(self.tool)
        capture, resume = self.tool["args"]["oneOf"]
        self.assertEqual(list(resume["properties"]), ["issue_key", "work_id"])
        self.assertEqual(resume["oneOf"], [{"required": ["work_id"]}, {"required": ["issue_key"]}])
        self.assertNotIn("issue_key", capture["properties"])

    def test_selector_shape_closure_and_bounds_cannot_drift(self):
        changes = {
            "work_id only": lambda _, resume: (resume["properties"].pop("issue_key"), resume.pop("oneOf"), resume.update(required=["work_id"])),
            "both required": lambda _, resume: resume.update(required=["work_id", "issue_key"]),
            "selector optional": lambda _, resume: resume.pop("oneOf"),
            "selector any": lambda _, resume: resume.update(anyOf=resume.pop("oneOf")),
            "Project override": lambda _, resume: resume["properties"].update(project_id={"type": "string"}),
            "open resume": lambda _, resume: resume.update(additionalProperties=True),
            "capture issue key": lambda capture, resume: capture["properties"].update(issue_key=resume["properties"]["issue_key"]),
            "key pattern": lambda _, resume: resume["properties"]["issue_key"].update(pattern="^.+$"),
            "key minimum": lambda _, resume: resume["properties"]["issue_key"].update(minLength=1),
            "key maximum": lambda _, resume: resume["properties"]["issue_key"].update(maxLength=128),
            "key type": lambda _, resume: resume["properties"]["issue_key"].update(type=["string", "null"]),
        }
        for name, change in changes.items():
            with self.subTest(change=name), self.assertRaises(ValueError):
                generator.validate_work_start_host_tool(self.edited(change))


class WorkerReadingHostContractTests(unittest.TestCase):
    TOOL = "concord_worker_read"
    FIELDS = ["ordinal", "offset", "length"]
    VALIDATOR = staticmethod(generator.validate_worker_read_host_tool)

    def setUp(self):
        self.host = json.loads((ROOT / "contracts/host-tool-surface.v1.json").read_text())
        self.schema = json.loads((ROOT / "contracts/host-tool-surface.schema.json").read_text())
        self.tool = next(tool for tool in self.host["tools"] if tool["name"] == self.TOOL)

    def test_the_shipped_reader_is_a_named_closed_host_tool(self):
        generator.validate_host_manifest(self.host, self.schema)
        self.assertEqual(list(self.tool["args"]["properties"]), self.FIELDS)

    def test_no_source_execution_or_binding_selector_can_enter_the_host_contract(self):
        for name in ("path", "ref", "command", "runner", "packet", "work_id", "attempt_id", "worktree_identity"):
            with self.subTest(name=name):
                tool = copy.deepcopy(self.tool)
                tool["args"]["properties"][name] = {"type": "string"}
                with self.assertRaises(ValueError):
                    self.VALIDATOR(tool)

    def test_ranges_requiredness_and_closure_cannot_drift(self):
        for name in self.FIELDS:
            for bound in ("minimum", "maximum"):
                with self.subTest(name=name, bound=bound):
                    tool = copy.deepcopy(self.tool)
                    tool["args"]["properties"][name][bound] += 1
                    with self.assertRaises(ValueError):
                        self.VALIDATOR(tool)
        for change in ({"additionalProperties": True}, {"required": self.FIELDS[:-1]}):
            tool = copy.deepcopy(self.tool)
            tool["args"].update(change)
            with self.assertRaises(ValueError):
                self.VALIDATOR(tool)


class WorkerDiffHostContractTests(WorkerReadingHostContractTests):
    TOOL = "concord_worker_diff"
    FIELDS = ["offset", "length"]
    VALIDATOR = staticmethod(generator.validate_worker_diff_host_tool)


class ResultRequirednessTests(unittest.TestCase):
    def validate_result(self, result):
        payload = copy.deepcopy(payload_schema)
        payload["$defs"]["research_read_result"] = result
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "payload.json"
            path.write_text(json.dumps(payload))
            with unittest.mock.patch.object(generator, "PAYLOAD", path):
                return generator.validate(manifest)

    def test_versioned_branches_without_a_common_field_are_required(self):
        self.validate_result(payload_schema["$defs"]["research_read_result"])

    def test_one_branch_without_required_fields_is_refused(self):
        result = copy.deepcopy(payload_schema["$defs"]["research_read_result"])
        result["oneOf"][1].pop("required")
        with self.assertRaisesRegex(ValueError, "result schema lacks required fields"):
            self.validate_result(result)

    def test_root_without_required_fields_or_branches_is_refused(self):
        result = copy.deepcopy(payload_schema["$defs"]["research_read_result"])
        result.pop("oneOf")
        with self.assertRaisesRegex(ValueError, "result schema lacks required fields"):
            self.validate_result(result)

    def test_branch_reference_must_resolve_to_a_closed_required_object(self):
        for branch in [{}, {"$ref": "#/$defs/missing"}, {"type": "string"},
                       {"type": "object", "required": ["id"]},
                       {"type": "object", "additionalProperties": False}]:
            with self.subTest(branch=branch):
                self.assertFalse(generator.union_result_branch_requires(branch, payload_schema["$defs"]))
        self.assertTrue(generator.union_result_branch_requires(
            {"$ref": "#/$defs/research_pack"}, payload_schema["$defs"]))


class EvidenceFixedBudgetCeilingTests(unittest.TestCase):
    """CD-0038 D2: the surface ceiling is uniform except where accepted
    evidence fixes a different value. The generator admits only the
    declared exceptions (workflow_action TS1 30; worktree_verify CON-317
    1800); any other non-uniform value, including one that mimics an
    evidence-fixed value on a different operation, must fail closed so the
    rule cannot erode by accretion.
    """

    def _replace_supported_budget(self, operation_id, value):
        value_dict = copy.deepcopy(manifest)
        for op in value_dict["operations"]:
            if op["id"] == operation_id:
                op["supported_budget_seconds"] = value
        return value_dict

    def test_shipped_manifest_admits_only_declared_evidence_fixed_ceilings(self):
        # The shipped manifest declares exactly two non-uniform ceilings
        # (workflow_action 30 and worktree_verify 1800); validate() enforces
        # that, and the rest of the surface must stay at the uniform 300.
        try:
            generator.validate(manifest)
        except ValueError as err:
            self.fail(f"shipped manifest violates its own uniformity rule: {err}")
        non_uniform = {op["id"]: op["supported_budget_seconds"] for op in manifest["operations"]
                       if op["supported_budget_seconds"] != 300}
        self.assertEqual(non_uniform, {
            "concord_work_transition.workflow_action": 30,
            "concord_work_transition.worktree_verify": 1800,
        })

    def test_a_uniform_seven_minute_ceiling_is_rejected(self):
        # A budget value no evidence pins is still over the uniform 300 and
        # the generator refuses it before the rule reach can widen.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 420)
        with self.assertRaises(ValueError):
            generator.validate(drifted)

    def test_an_undeclared_non_uniform_ceiling_is_rejected(self):
        # A ceiling the evidence list does not admit (one operation, a value
        # no rule fixes) breaks the uniformity discipline even when the
        # value would otherwise lie in range.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 600)
        with self.assertRaises(ValueError):
            generator.validate(drifted)

    def test_an_evidence_value_on_an_undeclared_operation_is_rejected(self):
        # 1800 is a declared ceiling for worktree_verify only. Putting it on
        # another operation copies the value without the evidence that
        # names the operation; the rule's job is to refuse that copy.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 1800)
        with self.assertRaises(ValueError):
            generator.validate(drifted)


class EnvelopeOperationCoverageTests(unittest.TestCase):
    """The manifest is the source of truth for what `toolOperation` must pair.

    The sibling check proves a declared pair stays satisfiable. This proves the
    other direction, which nothing held: an operation the surface declares but
    the envelope never enumerates has no satisfiable branch, so the adapter
    answers malformed_core_response for a well-formed core result.
    """
    def test_the_shipped_contracts_cover_every_declared_operation(self):
        self.assertEqual(lane_checker.envelope_operation_coverage_findings(envelope_schema, manifest), [])

    def test_an_unenumerated_operation_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        removed = None
        for index, branch in enumerate(value["$defs"]["toolOperation"]["oneOf"]):
            properties = branch.get("properties", {})
            if properties.get("query_id", {}).get("const") == "PM1.Q1":
                removed = value["$defs"]["toolOperation"]["oneOf"].pop(index)
                break
        self.assertIsNotNone(removed, "fixture lost the branch this test removes")
        findings = lane_checker.envelope_operation_coverage_findings(value, manifest)
        self.assertTrue(findings)
        self.assertIn("concord_product_view.resolve", findings[0])

    def test_a_query_id_that_disagrees_with_the_manifest_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        for branch in value["$defs"]["toolOperation"]["oneOf"]:
            if branch.get("properties", {}).get("query_id", {}).get("const") == "PM1.Q1":
                branch["properties"]["query_id"]["const"] = "PM1.Q99"
                break
        findings = lane_checker.envelope_operation_coverage_findings(value, manifest)
        self.assertTrue(findings)
        self.assertIn("PM1.Q99", " ".join(findings))

class Ts2BudgetTests(unittest.TestCase):
    """TS2's tool budget is law in the document and in the manifest; CI joins them."""

    def test_document_budget_matches_manifest(self):
        manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text())
        document = (ROOT / ".concord/docs/agent-tool-surface-budget.md").read_text()
        matches = re.findall(r"always_visible_tools: (\d+)", document)
        self.assertEqual(len(matches), 1, "TS2 must state the budget exactly once")
        declared = int(matches[0])
        self.assertEqual(manifest["surface"]["tool_count"], declared,
                         "manifest surface.tool_count disagrees with the TS2 budget")
        self.assertEqual(len(manifest["tools"]), declared,
                         "manifest tool list disagrees with the TS2 budget")


class EvidenceObligationVocabularyTests(unittest.TestCase):
    """CD-0056 D2: the obligation vocabulary is one closed set across two contracts."""

    def findings(self, lanes=None, report=None, registry=None):
        return lane_checker.evidence_obligation_findings(
            copy.deepcopy(lanes if lanes is not None else lanes_schema),
            copy.deepcopy(report if report is not None else report_schema),
            copy.deepcopy(registry if registry is not None else lane_registry),
        )

    def test_shipped_contracts_agree(self):
        self.assertEqual(self.findings(), [])

    def test_divergent_enums_are_rejected(self):
        value = copy.deepcopy(report_schema); value["$defs"]["evidence_obligation"]["enum"].remove("severity")
        self.assertTrue(any("vocabulary differs" in f for f in self.findings(report=value)))

    def test_reordered_enums_are_rejected(self):
        value = copy.deepcopy(report_schema); value["$defs"]["evidence_obligation"]["enum"].reverse()
        self.assertTrue(any("ordered differently" in f for f in self.findings(report=value)))

    def test_lane_obligation_outside_the_vocabulary_is_rejected(self):
        value = copy.deepcopy(lane_registry); value["lanes"][0]["evidence_obligations"].append("vibes")
        self.assertTrue(any("outside the closed vocabulary" in f for f in self.findings(registry=value)))

    def test_missing_definition_is_rejected(self):
        value = copy.deepcopy(lanes_schema); del value["$defs"]["evidence_obligation"]
        self.assertTrue(any("missing $defs/evidence_obligation/enum" in f for f in self.findings(lanes=value)))

    def test_duplicated_token_is_rejected(self):
        value = copy.deepcopy(lanes_schema); value["$defs"]["evidence_obligation"]["enum"].append("severity")
        self.assertTrue(any("duplicate-free" in f for f in self.findings(lanes=value)))

class CD0043LaneMethodologyTests(unittest.TestCase):
    """Issue #461: CD-0043's verification facts are asserted, not narrated."""

    def findings(self, packet=None, registry=None, skills=None):
        return lane_checker.cd0043_lane_methodology_findings(
            copy.deepcopy(packet if packet is not None else packet_schema),
            copy.deepcopy(registry if registry is not None else lane_registry),
            list(skills if skills is not None else ["README.md", *sorted(lane_checker.ADMITTED_SKILLS)]),
        )

    def test_shipped_contracts_satisfy(self):
        self.assertEqual(self.findings(), [])

    def test_methodology_property_is_rejected(self):
        value = copy.deepcopy(packet_schema)
        value["properties"]["methodology"] = {"type": "string", "minLength": 1, "maxLength": 64}
        self.assertTrue(any("'methodology' property is rejected" in f for f in self.findings(packet=value)))

    def test_authored_registry_counter_is_rejected(self):
        for version in (1, 2, None):
            value = copy.deepcopy(lane_registry); value["version"] = version
            self.assertTrue(any("authored version counter is forbidden" in f for f in self.findings(registry=value)))

    def test_extra_skill_entry_is_rejected(self):
        findings = self.findings(skills=["README.md", "review-rubric.md"])
        self.assertTrue(any("exact admitted inventory" in f for f in findings))
        self.assertTrue(any("review-rubric.md" in f for f in findings))

    def test_empty_skills_boundary_is_rejected(self):
        self.assertTrue(any("exact admitted inventory" in f for f in self.findings(skills=[])))

class ShippedSkillTests(unittest.TestCase):
    NAME = "concord-domain-shape-stewardship"
    COMMANDS = {"product-knowledge-home-designate", "domain-shape-check"}
    OPERATIONS = {
        "concord_domain.shape", "concord_domain.footprint", "concord_domain.list",
        "concord_product_view.resolve", "concord_product_view.resources",
        "concord_knowledge.search", "concord_knowledge.resolve_note",
    }

    def content(self):
        return (ROOT / "skills" / self.NAME / "SKILL.md").read_text(encoding="utf-8")

    def findings(self, content):
        return lane_checker.shipped_skill_content_findings(
            content, skill_name=self.NAME, commands=self.COMMANDS, operations=self.OPERATIONS,
        )

    def test_shipped_content_satisfies_portability(self):
        self.assertEqual(self.findings(self.content()), [])

    def test_missing_or_wrong_frontmatter_is_rejected(self):
        for content in (
            self.content().split("---\n", 2)[2],
            self.content().replace("name: " + self.NAME, "name: review-methodology"),
            re.sub(r"(?m)^description:.*\n", "", self.content()),
        ):
            with self.subTest(content=content[:80]):
                self.assertTrue(any("frontmatter" in f for f in self.findings(content)))

    def test_private_content_uses_existing_public_content_patterns(self):
        for forbidden in ("/" + "home" + "/synthetic/notes", "127." + "0.0.1", "gh" + "p_" + "a" * 32):
            with self.subTest(forbidden=forbidden):
                self.assertTrue(any("private" in f or "credential" in f for f in self.findings(self.content() + "\n" + forbidden)))

    def test_host_tools_are_rejected(self):
        for invocation in ("lgrep_search_semantic", "context7_query_docs", "exa_web_search", "github_get_file_contents"):
            with self.subTest(invocation=invocation):
                self.assertTrue(any("host tool" in f for f in self.findings(self.content() + "\n`" + invocation + "`")))

    def test_unknown_concord_commands_and_operations_are_rejected(self):
        for invocation in ("concord domain-shape-setup", "concord_domain.invented", "concord domain-shape-check --product alpha"):
            with self.subTest(invocation=invocation):
                self.assertTrue(any("operation" in f or "command" in f for f in self.findings(self.content() + "\n`" + invocation + "`")))

    def test_unshipped_operations_are_not_assumed_from_the_skill(self):
        self.assertTrue(lane_checker.shipped_skill_content_findings(
            self.content(), skill_name=self.NAME, commands=set(), operations=set(),
        ))

    def test_executable_examples_cannot_introduce_another_tool(self):
        findings = self.findings(self.content() + "\n```sh\narbitrary-tool check\n```\n")
        self.assertTrue(any("executable" in f for f in findings))

    def test_shipped_help_json_examples_and_field_values_are_not_host_tools(self):
        content = self.content() + '\n`concord --help`\n`concord domain-shape-check --help`\n`kind: domain_shape`\n```json\n{"product_id":"alpha"}\n```\n'
        self.assertEqual(self.findings(content), [])

    def test_canonical_references_cannot_mix_revisions(self):
        revision = re.search(r"concord/(?:tree|blob)/([0-9a-f]{40})/", self.content())[1]
        content = self.content().replace(revision, ("b" if revision == "a" * 40 else "a") * 40, 1)
        self.assertTrue(any("one source revision" in f for f in self.findings(content)))

    def test_empty_concord_invocation_is_a_finding_not_a_crash(self):
        self.assertTrue(any("command" in f for f in self.findings(self.content() + "\n`concord `")))

    def test_field_spelling_cannot_hide_an_executable_invocation(self):
        self.assertTrue(any("executable" in f for f in self.findings(self.content() + "\n`run: arbitrary-tool apply -f x`")))

    def test_host_description_bound_is_checked(self):
        content = re.sub(r"(?m)^description:.*$", "description: " + "x" * 1025, self.content())
        self.assertTrue(any("frontmatter" in f for f in self.findings(content)))

    def test_reference_resolution_requires_a_reachable_commit_and_existing_paths(self):
        with unittest.mock.patch.object(lane_checker.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)) as run:
            self.assertEqual(lane_checker.shipped_skill_reference_findings(ROOT, self.content()), [])
            self.assertEqual(run.call_args_list[0].args[0][:3], ["git", "merge-base", "--is-ancestor"])
            self.assertGreaterEqual(run.call_count, 9)
        with unittest.mock.patch.object(lane_checker.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)):
            self.assertTrue(any("ancestor" in f for f in lane_checker.shipped_skill_reference_findings(ROOT, self.content())))

        def git_result(argv, **kwargs):
            missing = argv[1] == "cat-file" and argv[-1].endswith(":contracts/domain-fact.v1.schema.json")
            return subprocess.CompletedProcess(argv, 1 if missing else 0)

        with unittest.mock.patch.object(lane_checker.subprocess, "run", side_effect=git_result):
            self.assertTrue(any("domain-fact.v1.schema.json" in f for f in lane_checker.shipped_skill_reference_findings(ROOT, self.content())))

    def test_checkout_relative_mutable_and_unrelated_links_are_rejected(self):
        for target in (
            "../../.concord/docs/decisions/rule.md", "../references/schema.json",
            "https://github.com/Sharper-Flow/concord/tree/main/contracts/domain-fact.v1.schema.json",
            "https://github.com/example/" + "pr" + "ivate/tree/" + "a" * 40 + "/law.md",
        ):
            with self.subTest(target=target):
                self.assertTrue(any("reference" in f for f in self.findings(self.content() + f"\n[Required]({target})")))

    def test_reference_definitions_and_autolinks_are_checked(self):
        for addition in (
            "\n[required]: ../../.concord/docs/decisions/rule.md\n",
            "\n<https://github.com/Sharper-Flow/concord/blob/main/README.md>\n",
        ):
            with self.subTest(addition=addition):
                self.assertTrue(any("reference" in f for f in self.findings(self.content() + addition)))

    def test_canonical_shape_authority_and_publication_references_are_required(self):
        text = re.sub(r"\[Knowledge identity and publication\]\([^)]*\)", "Publication", self.content())
        self.assertTrue(any("publication" in f for f in self.findings(text)))

    def test_all_four_workflows_and_nine_audit_questions_are_present(self):
        for heading in ("Initialize", "Migrate and reorganise", "Refine", "Audit"):
            self.assertIn("## " + heading + "\n", self.content())
        audit = self.content().split("## Audit\n", 1)[1].split("## Report", 1)[0]
        self.assertEqual(re.findall(r"(?m)^(\d+)\. ", audit), [str(n) for n in range(1, 10)])

    def test_each_workflow_names_its_verifying_check_and_read(self):
        sections = dict(re.findall(r"(?ms)^## ([^\n]+)\n(.*?)(?=^## |\Z)", self.content()))
        for heading in ("Initialize", "Migrate and reorganise", "Refine", "Audit"):
            with self.subTest(heading=heading):
                self.assertIn("`concord domain-shape-check`", sections[heading])
                self.assertIn("`concord_domain.", sections[heading])

    def test_shared_investigation_names_its_product_and_knowledge_reads(self):
        shared = self.content().split("## Shared investigation\n", 1)[1].split("### Shape and verification\n", 1)[0]
        for read in ("`concord_product_view.resolve`", "`concord_domain.list`", "`concord_knowledge.resolve_note`"):
            with self.subTest(read=read):
                self.assertIn(read, shared)

    def test_admitted_inventory_is_exact_and_rejects_missing_extra_or_symlinked_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            skills = Path(temporary) / "skills"
            shutil.copytree(ROOT / "skills", skills)
            entry = skills / self.NAME
            content = entry / "SKILL.md"
            self.assertEqual(lane_checker.shipped_skill_inventory_findings(skills), [])
            extra = entry / "unadmitted.md"
            extra.write_text("Not admitted")
            self.assertTrue(any("unadmitted.md" in f for f in lane_checker.shipped_skill_inventory_findings(skills)))
            extra.unlink()
            extra_directory = entry / "unadmitted"
            extra_directory.mkdir()
            self.assertTrue(any("unadmitted directory" in f for f in lane_checker.shipped_skill_inventory_findings(skills)))
            extra_directory.rmdir()
            content.unlink()
            self.assertTrue(any("missing" in f for f in lane_checker.shipped_skill_inventory_findings(skills)))
            content.symlink_to(skills / "README.md")
            self.assertTrue(any("symlink" in f for f in lane_checker.shipped_skill_inventory_findings(skills)))

    def test_redirected_skills_directory_is_rejected_without_reading_its_content(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "outside"
            target.mkdir()
            skills = root / "skills"
            skills.symlink_to(target, target_is_directory=True)
            self.assertEqual(lane_checker.shipped_skill_inventory_findings(skills), [
                "skills/: symlink is not an admitted skill directory",
            ])


class LawWritingSkillTests(unittest.TestCase):
    NAME = "concord-law-writing"
    OPERATIONS = {
        "concord_product_view.resolve", "concord_domain.list", "concord_domain.footprint",
        "concord_knowledge.search", "concord_knowledge.resolve_note",
    }

    def content(self):
        return (ROOT / "skills" / self.NAME / "SKILL.md").read_text(encoding="utf-8")

    def findings(self, content):
        return lane_checker.shipped_skill_content_findings(
            content, skill_name=self.NAME, commands=set(), operations=self.OPERATIONS,
        )

    def test_law_writing_is_admitted_with_its_own_portable_references(self):
        self.assertIn(self.NAME, lane_checker.ADMITTED_SKILLS)
        self.assertEqual(self.findings(self.content()), [])

    def test_law_approval_reference_is_required_independently_of_shape_references(self):
        content = re.sub(r"\[Operator approval boundary\]\([^)]*\)", "Approval", self.content())
        self.assertTrue(any("missing canonical approval reference" in finding for finding in self.findings(content)))

    def test_law_references_resolve_in_delivered_history(self):
        self.assertEqual(lane_checker.shipped_skill_reference_findings(ROOT, self.content()), [])

    def test_law_license_is_required_without_admitting_arbitrary_support_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            skills = Path(temporary) / "skills"
            shutil.copytree(ROOT / "skills", skills)
            self.assertEqual(lane_checker.shipped_skill_inventory_findings(skills), [])
            license_path = skills / self.NAME / "LICENSE"
            self.assertIn("Copyright (c) 2026 Matt Pocock", license_path.read_text(encoding="utf-8"))
            license_path.unlink()
            findings = lane_checker.shipped_skill_inventory_findings(skills)
            self.assertTrue(any(f"{self.NAME}/LICENSE: missing admitted file" in finding for finding in findings))
            license_path.symlink_to(skills / "README.md")
            self.assertTrue(any("symlink" in finding for finding in lane_checker.shipped_skill_inventory_findings(skills)))


class EvidenceBoundParityTests(unittest.TestCase):
    """The request evidence declaration owns every evidence field bound.

    The result schema $defs/evidenceRef must carry the same bounds: authority
    and locator_kind 1..256, digest hex pattern 8..128, version <=256, locator
    1..2048. A mismatch admits evidence values the request would refuse, or
    refuses values the request admitted; both ways the parity check at
    generation time is the only thing that holds them in lockstep.
    """

    def check(self, payload=None, envelope=None):
        generator.check_evidence_bound_parity(
            copy.deepcopy(payload if payload is not None else payload_schema),
            copy.deepcopy(envelope if envelope is not None else envelope_schema),
        )

    def test_shipped_schemas_agree(self):
        # No exception means the bounds agree across both declarations.
        self.check()

    def test_a_shrunken_envelope_authority_bound_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["evidenceRef"]["properties"]["authority"]["maxLength"] = 128
        with self.assertRaises(ValueError) as ctx:
            self.check(envelope=value)
        self.assertIn("authority", str(ctx.exception))

    def test_an_envelope_locator_kind_bound_divergence_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["evidenceRef"]["properties"]["locator_kind"]["maxLength"] = 64
        with self.assertRaises(ValueError) as ctx:
            self.check(envelope=value)
        self.assertIn("locator_kind", str(ctx.exception))

    def test_a_payload_authority_bound_drift_is_rejected(self):
        # A request-side drift is also a mismatch. The request evidence
        # reaches authority through $defs/short, while the envelope inlines
        # its bound, so narrowing `short` alone must fire the parity check.
        value = copy.deepcopy(payload_schema)
        value["$defs"]["short"]["maxLength"] = 128
        with self.assertRaises(ValueError) as ctx:
            self.check(payload=value)
        self.assertIn("authority", str(ctx.exception))

    def test_an_envelope_digest_pattern_drift_is_rejected(self):
        # The digest bound includes its pattern. A different pattern string,
        # here one that drops the optional sha256: prefix and uppercase hex,
        # is a mismatch even when the length bounds agree.
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["evidenceRef"]["properties"]["digest"]["pattern"] = r"^[a-f0-9]+$"
        with self.assertRaises(ValueError) as ctx:
            self.check(envelope=value)
        self.assertIn("digest", str(ctx.exception))

    def test_a_payload_locator_maxlength_drift_is_rejected(self):
        value = copy.deepcopy(payload_schema)
        value["$defs"]["evidence"]["properties"]["locator"]["maxLength"] = 1024
        with self.assertRaises(ValueError) as ctx:
            self.check(payload=value)
        self.assertIn("locator", str(ctx.exception))

    def test_a_payload_version_maxlength_drift_is_rejected(self):
        value = copy.deepcopy(payload_schema)
        value["$defs"]["evidence"]["properties"]["version"]["maxLength"] = 128
        with self.assertRaises(ValueError) as ctx:
            self.check(payload=value)
        self.assertIn("version", str(ctx.exception))

    def test_missing_envelope_evidence_ref_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        del value["$defs"]["evidenceRef"]
        with self.assertRaises(ValueError):
            self.check(envelope=value)

    def test_missing_payload_evidence_is_rejected(self):
        value = copy.deepcopy(payload_schema)
        del value["$defs"]["evidence"]
        with self.assertRaises(ValueError):
            self.check(payload=value)


class WorkflowDefinitionContentIdentityTests(unittest.TestCase):
    def setUp(self):
        self.schema = json.loads((ROOT / "contracts/workflow-definition.schema.json").read_text())
        self.definition = json.loads((ROOT / "contracts/workflow-engine.fixtures.json").read_text())["cases"][0]["instance"]

    def test_definition_is_version_free_and_capabilities_are_digest_covered(self):
        generator.schema_validate(self.definition, self.schema, self.schema)
        self.assertNotIn("version", self.definition)
        self.assertEqual(lane_checker._validate_definition_semantics(self.definition), None)
        changed = copy.deepcopy(self.definition)
        changed["capabilities"] = ["worker_jobs"]
        self.assertEqual(lane_checker._validate_definition_semantics(changed), "definition digest mismatch")
        with self.assertRaises(ValueError):
            generator.schema_validate(self.definition | {"version": 1}, self.schema, self.schema)

    def test_capabilities_require_known_unique_sorted_names(self):
        for capabilities in (["unknown"], ["worker_jobs", "worker_jobs"]):
            with self.subTest(capabilities=capabilities), self.assertRaises(ValueError):
                generator.schema_validate(self.definition | {"capabilities": capabilities}, self.schema, self.schema)
        self.assertEqual(lane_checker._validate_definition_semantics(
            self.definition | {"capabilities": ["worker_jobs", "accept_delivery"]}),
            "definition capabilities must be sorted and unique")


class WorkerPacketMirrorProjectionTests(unittest.TestCase):
    """The TS8 worker_packet inputs mirror is derived from the canonical
    lane packet contract, not handwritten.

    A canonical inline input field must reach the published surface on
    regeneration. A missing projection fails --check before the core can
    refuse the dispatcher's packet as an unknown property.
    """

    def project(self, payload=None, packet=None):
        value = copy.deepcopy(payload_schema if payload is None else payload)
        canonical = copy.deepcopy(packet_schema if packet is None else packet)
        generator.project_worker_packet_inputs(value, canonical)
        return value["$defs"]["worker_packet"]["properties"]["inputs"]

    def test_shipped_mirror_carries_every_canonical_input_field(self):
        canonical = packet_schema["properties"]["inputs"]
        mirror = payload_schema["$defs"]["worker_packet"]["properties"]["inputs"]
        self.assertEqual(sorted(mirror["properties"]), sorted(canonical["properties"]))
        self.assertEqual(mirror.get("required"), canonical.get("required"))

    def test_projection_is_idempotent_on_the_shipped_mirror(self):
        self.assertEqual(self.project(), payload_schema["$defs"]["worker_packet"]["properties"]["inputs"])

    def test_live_projection_keeps_one_current_packet_shape(self):
        payload = copy.deepcopy(payload_schema)
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["focus"] = {"type": "string"}
        packet["properties"]["inputs"]["properties"]["work_context"]["description"] = "Current canonical teaching."
        generator.project_worker_packet_inputs(payload, packet)
        live = payload["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]
        self.assertEqual(live["focus"], {"type": "string"})
        self.assertEqual(live["work_context"]["description"], generator.WORKER_PACKET_INPUT_PUBLISHED_TEXT["work_context"])
        # CD-0242 D14 removes the history-only closures, not their current members.
        for name in ("worker_packet2", "worker_packet3", "worker_packet4", "worker_packet5",
                     "work_context_view2", "work_context_view3", "work_context_domain_group2",
                     "work_context_domain_group3", "worker_packet_checks_repair_job", "worker_packet5_worker_job"):
            self.assertNotIn(name, payload["$defs"])
        job = payload["$defs"]["worker_packet_worker_job"]
        self.assertEqual(set(job["properties"]), set(packet_schema["$defs"]["lane_packet_worker_job"]["properties"]))
        self.assertTrue({"checks_repair", "direct_caller_allowance", "caller_exclusions"} <= set(job["properties"]))

    def test_record_fields_keep_shared_aliases_and_canonical_teaching(self):
        projected = self.project()["properties"]
        canonical = packet_schema["properties"]["inputs"]["properties"]
        aliases = {
            "law_context": "workflow_law_context",
            "design_record": "workflow_design_record",
            "proposal_record": "workflow_proposal_record",
            "work_record": "worker_packet_work_record",
        }
        for field, alias in aliases.items():
            with self.subTest(field=field):
                self.assertIn(alias, payload_schema["$defs"])
                expected = {"$ref": f"#/$defs/{alias}"}
                if "description" in canonical[field]:
                    expected["description"] = canonical[field]["description"]
                self.assertEqual(projected[field], expected)

    def test_an_omitted_canonical_field_is_restored(self):
        # Regeneration restores a missing canonical field verbatim.
        payload = copy.deepcopy(payload_schema)
        del payload["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]["report_protocol"]
        restored = self.project(payload=payload)
        self.assertEqual(
            restored["properties"]["report_protocol"],
            packet_schema["properties"]["inputs"]["properties"]["report_protocol"],
        )

    def test_a_new_canonical_inline_field_lands_verbatim(self):
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["focus"] = {"type": "string", "minLength": 1, "maxLength": 64}
        projected = self.project(packet=packet)
        self.assertEqual(projected["properties"]["focus"], {"type": "string", "minLength": 1, "maxLength": 64})

    def test_a_removed_canonical_field_leaves_the_mirror(self):
        packet = copy.deepcopy(packet_schema)
        del packet["properties"]["inputs"]["properties"]["report_protocol"]
        projected = self.project(packet=packet)
        self.assertNotIn("report_protocol", projected["properties"])

    def test_the_required_set_follows_canonical(self):
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["required"].append("context")
        projected = self.project(packet=packet)
        self.assertEqual(projected["required"], ["task", "binding", "context"])

    def test_a_canonical_ref_field_without_an_alias_is_rejected(self):
        # A field the canonical schema binds through a lane-local $ref cannot
        # be copied verbatim: the ref target does not exist in the payloads
        # document. Generation must fail naming the field, so anchoring a new
        # alias is a deliberate act.
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["plan"] = {"$ref": "#/$defs/lane_plan"}
        with self.assertRaises(ValueError) as ctx:
            self.project(packet=packet)
        self.assertIn("plan", str(ctx.exception))

    def test_an_aliased_field_ignores_the_canonical_node_shape(self):
        # binding is aliased: the payloads schema's shared def owns the
        # published bound. A canonical change to the aliased node must not
        # leak into the mirror through a verbatim copy.
        packet = copy.deepcopy(packet_schema)
        packet["properties"]["inputs"]["properties"]["binding"] = {"$ref": "#/$defs/lane_other"}
        projected = self.project(packet=packet)
        self.assertEqual(projected["properties"]["binding"], {"$ref": "#/$defs/worker_packet_binding"})

    def test_context_fields_keep_their_aliases_and_published_text(self):
        projected = self.project()
        shipped = payload_schema["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]
        self.assertEqual(projected["properties"]["work_context"], shipped["work_context"])
        self.assertEqual(projected["properties"]["checkpoint"], shipped["checkpoint"])
        self.assertEqual(projected["properties"]["work_context"]["$ref"], "#/$defs/work_context_view")
        self.assertEqual(projected["properties"]["checkpoint"]["$ref"], "#/$defs/continuity_checkpoint")
        self.assertEqual(projected["properties"]["work_context"]["description"],
                         generator.WORKER_PACKET_INPUT_PUBLISHED_TEXT["work_context"])
        self.assertEqual(projected["properties"]["checkpoint"]["description"],
                         generator.WORKER_PACKET_INPUT_PUBLISHED_TEXT["checkpoint"])

    def test_an_alias_naming_an_unknown_def_is_rejected(self):
        payload = copy.deepcopy(payload_schema)
        del payload["$defs"]["continuity_checkpoint"]
        with self.assertRaises(ValueError) as ctx:
            self.project(payload=payload)
        self.assertIn("continuity_checkpoint", str(ctx.exception))

    def test_the_packets_top_level_fields_stay_declared(self):
        payload = copy.deepcopy(payload_schema)
        payload["$defs"]["worker_packet"]["properties"]["lane_digest"] = {"$ref": "#/$defs/id"}
        generator.project_worker_packet_inputs(payload, packet_schema)
        packet = payload["$defs"]["worker_packet"]
        self.assertEqual(packet["properties"]["lane_digest"], packet_schema["properties"]["lane_digest"])
        self.assertEqual(packet["required"], packet_schema["required"])
        self.assertNotIn("lane_version", packet["properties"])


class EnvelopeOperationVocabularyTests(unittest.TestCase):
    """Issue #352: every tool/operation pair the envelope declares must be satisfiable."""

    def findings(self, envelope=None):
        return lane_checker.envelope_operation_findings(
            copy.deepcopy(envelope if envelope is not None else envelope_schema),
        )

    def test_shipped_contracts_agree(self):
        self.assertEqual(self.findings(), [])

    def test_operation_removed_from_the_base_enum_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["operation"]["enum"].remove("continuity")
        found = self.findings(value)
        self.assertTrue(any("unsatisfiable" in f and "concord_work_trace.continuity" in f for f in found), found)

    def test_operation_removed_from_the_next_intent_enum_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["nextIntent"]["properties"]["operation"]["enum"].remove("messages")
        found = self.findings(value)
        self.assertTrue(any("nextIntent" in f and "concord_work_browse.messages" in f for f in found), found)

    def test_new_tool_operation_pair_without_an_enum_entry_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["toolOperation"]["oneOf"].append({
            "required": ["tool", "operation"], "not": {"required": ["query_id"]},
            "properties": {"tool": {"const": "concord_work_browse"}, "operation": {"const": "forecast"}},
        })
        found = self.findings(value)
        self.assertTrue(any("concord_work_browse.forecast" in f for f in found), found)
        self.assertEqual(len([f for f in found if "concord_work_browse.forecast" in f]), 2, found)

    def test_new_pair_whose_query_id_the_pattern_rejects_is_rejected(self):
        value = copy.deepcopy(envelope_schema)
        value["$defs"]["toolOperation"]["oneOf"].append({
            "required": ["tool", "operation", "query_id"],
            "properties": {"tool": {"const": "concord_work_browse"}, "operation": {"const": "scope"}, "query_id": {"const": "PM1.Q99"}},
        })
        found = self.findings(value)
        self.assertTrue(any("query_id" in f and "PM1.Q99" in f for f in found), found)

    def test_write_operations_absent_from_tool_operation_are_not_findings(self):
        # Containment is one-way: the enum may name operations no read branch pairs.
        value = copy.deepcopy(envelope_schema)
        for definition in ("base", "nextIntent"):
            value["$defs"][definition]["properties"]["operation"]["enum"].append("unpaired_write")
        self.assertEqual(self.findings(value), [])

    def test_missing_tool_operation_definition_is_rejected(self):
        value = copy.deepcopy(envelope_schema); del value["$defs"]["toolOperation"]["oneOf"]
        self.assertTrue(any("missing $defs/toolOperation/oneOf" in f for f in self.findings(value)))

    def test_duplicated_enum_token_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["operation"]["enum"].append("scope")
        self.assertTrue(any("duplicate-free" in f for f in self.findings(value)))

    def test_uncompilable_query_id_pattern_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["query_id"]["pattern"] = "^(PM1"
        self.assertTrue(any("not a valid regular expression" in f for f in self.findings(value)))

class AdapterHostPinTests(unittest.TestCase):
    """The pin replaced a hand-written mirror of the host surface.

    The mirror could only be wrong in the direction nothing checked, so these
    tests hold the two properties that keep the replacement honest: the pin
    names a reviewed set of declarations, and the diagnostics it allows are a
    budget rather than an exemption.
    """

    def setUp(self):
        self.pin = json.loads((ROOT / ".concord/docs/adapter-host-pin.v1.json").read_text())
        self.schema = json.loads((ROOT / "contracts/adapter-host-pin.schema.json").read_text())
        # Tamper cases run against a synthetic pin, not the shipped one. A test
        # that mutates the real manifest's allowances asserts the repository
        # still carries that debt, and would fail when the debt is paid off.
        self.fixture = {
            "schema_version": "1.0",
            "sources": ["adapter/opencode"],
            "packages": [
                {"name": "@opencode-ai/plugin", "version": "1.0.0", "declares": "the host tool surface"},
                {"name": "runtime-types", "version": "2.0.0", "declares": "the ambient runtime globals"},
            ],
            "typescript": "5.9.3",
            "compiler_options": {
                "target": "es2022", "module": "esnext", "moduleResolution": "bundler",
                "lib": ["es2022"], "types": ["runtime-types"], "strict": True,
                "noUnusedLocals": True, "noUnusedParameters": True, "noEmit": True,
                "skipLibCheck": True, "resolveJsonModule": True, "forceConsistentCasingInFileNames": True,
                "allowJs": True,
            },
            "allowances": [{"file": "example.ts", "code": "TS2322", "count": 1, "state": "outstanding", "issue": 1, "reason": "a recorded divergence"}],
            "runtime_probe": {
                "probe_identity": "tool-result-contract-v1", "runner": "python3 scripts/probe-adapter-host-result.py", "observed_on": "2026-08-29", "host_package": "opencode-ai", "host_version": "1.0.0",
                "source_urls": ["https://example.com/registry", "https://example.com/truncate"],
                "commands": {"bare": "bunx opencode-ai@1.0.0 --pure debug agent build --tool result-probe_bare --params '{}'", "conforming": "bunx opencode-ai@1.0.0 --pure debug agent build --tool result-probe_conforming --params '{}'"},
                "bare_object_failure": {"status": "failed", "exit_code": 1, "error_contains": "undefined is not an object (evaluating 'c.split')"},
                "conforming_result_success": {"status": "passed", "exit_code": 0, "output": "{}", "metadata_truncated": False},
                "limits": {"max_bytes": 51200, "max_lines": 2000},
            },
        }

    def findings(self, mutate=None, pin=None):
        value = copy.deepcopy(self.fixture if pin is None else pin)
        if mutate:
            mutate(value)
        found = []
        lane_checker.validate_host_pin(value, self.schema, found)
        return found

    def test_shipped_pin_satisfies_its_contract(self):
        self.assertEqual(self.findings(pin=self.pin), [])

    def test_fixture_is_valid_so_tamper_cases_isolate_one_defect(self):
        self.assertEqual(self.findings(), [])

    def test_missing_runtime_probe_evidence_is_rejected(self):
        def mutate(value):
            del value["runtime_probe"]
        self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_stale_runtime_probe_version_is_rejected(self):
        def mutate(value):
            value["runtime_probe"]["host_version"] = "2.0.0"
        self.assertTrue(any("version does not match" in f for f in self.findings(mutate)))

    def test_ambient_type_package_that_is_not_pinned_is_rejected(self):
        def mutate(value):
            value["compiler_options"]["types"].append("node")
        self.assertTrue(any("names no pinned package" in f for f in self.findings(mutate)))

    def test_outstanding_allowance_without_an_issue_is_rejected(self):
        def mutate(value):
            del value["allowances"][0]["issue"]
        self.assertTrue(any("carries no tracking issue" in f for f in self.findings(mutate)))

    def test_settled_allowance_carrying_an_issue_is_rejected(self):
        def mutate(value):
            value["allowances"][0]["state"] = "out_of_scope"
        self.assertTrue(any("must not carry an issue" in f for f in self.findings(mutate)))

    def test_version_range_is_rejected(self):
        def mutate(value):
            value["packages"][0]["version"] = "^1.18.23"
        self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_non_strict_typecheck_is_rejected(self):
        # A non-strict run against real declarations proves less than the
        # mirror it replaced, so the contract pins strict rather than defaulting it.
        def mutate(value):
            value["compiler_options"]["strict"] = False
        self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_unused_code_checks_cannot_be_disabled(self):
        for option in ("noUnusedLocals", "noUnusedParameters"):
            with self.subTest(option=option):
                def mutate(value, option=option):
                    value["compiler_options"][option] = False
                self.assertTrue(any("does not satisfy its contract" in f for f in self.findings(mutate)))

    def test_allowance_states_match_the_contract_vocabulary(self):
        declared = set(self.schema["properties"]["allowances"]["items"]["properties"]["state"]["enum"])
        self.assertEqual(declared, {"outstanding", "unmeasured", "out_of_scope"})
        for allowance in self.pin["allowances"]:
            self.assertIn(allowance["state"], declared)

    def test_every_shipped_outstanding_allowance_names_a_tracking_issue(self):
        for allowance in self.pin["allowances"]:
            if allowance["state"] == "outstanding":
                self.assertIsInstance(allowance.get("issue"), int)

    def test_pinned_bun_types_match_the_bun_the_workflow_installs(self):
        # The ambient runtime surface the typecheck asserts and the runtime CI
        # actually runs are two claims about one thing. Pinning them separately
        # is how they would come apart without anything noticing.
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        installed = re.search(r"bun-version:\s*(\S+)", workflow)
        self.assertIsNotNone(installed, "ci.yml declares no bun-version")
        pinned = [package["version"] for package in self.pin["packages"] if package["name"] == "bun-types"]
        self.assertEqual(pinned, [installed.group(1)])


class AdapterHostDiagnosticTests(unittest.TestCase):
    OUTPUT = "src/concord.ts(244,124): error TS2322: Type 'x' is not assignable to type 'y'.\n  Type 'a' is not assignable to type 'b'.\n"

    def reconcile(self, output, allowances, exit_code=0):
        found = []
        lane_checker.reconcile_diagnostics(output, allowances, found, exit_code)
        return found

    def allowance(self, **overrides):
        base = {"file": "concord.ts", "code": "TS2322", "count": 1, "state": "outstanding", "issue": 560, "reason": "recorded divergence"}
        base.update(overrides)
        return base

    def test_recorded_divergence_at_its_recorded_size_passes(self):
        self.assertEqual(self.reconcile(self.OUTPUT, [self.allowance()]), [])

    def test_continuation_lines_are_not_counted_as_diagnostics(self):
        # tsc indents the explanation under the diagnostic. Counting those lines
        # would inflate every allowance by however much detail tsc chose to print.
        self.assertEqual(self.reconcile(self.OUTPUT, [self.allowance(count=2)]), ["allowance drift: concord.ts emits 1 TS2322 diagnostic(s), the manifest records 2"])

    def test_unrecorded_diagnostic_is_a_finding(self):
        found = self.reconcile(self.OUTPUT, [])
        self.assertTrue(any("does not satisfy the pinned host declarations" in f for f in found), found)

    def test_spent_allowance_is_a_finding(self):
        found = self.reconcile("", [self.allowance()])
        self.assertTrue(any("stale allowance" in f for f in found), found)

    def test_allowance_matches_by_file_and_code_not_position(self):
        moved = self.OUTPUT.replace("(244,124)", "(999,1)")
        self.assertEqual(self.reconcile(moved, [self.allowance()]), [])

    def test_allowance_does_not_cover_a_different_code_in_the_same_file(self):
        other = "src/concord.ts(12,1): error TS2345: Argument of type 'x'.\n"
        found = self.reconcile(self.OUTPUT + other, [self.allowance()])
        self.assertTrue(any("TS2345" in f for f in found), found)

    def test_positionless_diagnostic_is_a_finding(self):
        # tsc reports "no inputs were found" without a source position. Silently
        # skipping it would let the check pass over a typecheck of nothing.
        found = self.reconcile("error TS18003: No inputs were found in config file.\n", [], 1)
        self.assertTrue(any("no source position" in f for f in found), found)

    def test_nonzero_exit_without_any_diagnostic_is_a_finding(self):
        found = self.reconcile("bun: command failed\n", [], 1)
        self.assertTrue(any("without reporting a diagnostic" in f for f in found), found)

    def test_clean_run_reports_nothing(self):
        self.assertEqual(self.reconcile("", [], 0), [])


class DeliveryDecidableTeachingTests(unittest.TestCase):
    """CD-0184: the authoring schema teaches that acceptance is decidable at
    delivery. The outcome_predicates array shared by approve_contract and
    supersede_contract carries the rule, and the generator refuses a
    projection that drops it. The CD-0184 wait-rule teaching retired with
    add_condition (CD-0218 D1): its absence is derived from the retired
    published actions, so a projected wait variant is a stale copy the
    generator refuses, never a silently skipped check.
    """

    def test_shipped_predicate_array_teaches_the_rule(self):
        items = payload_schema["$defs"]["workflow_action_outcome_predicates"]
        self.assertEqual(generator.DELIVERY_RULE_PREDICATE_DESCRIPTION, items.get("description"))

    def test_generator_refuses_a_projection_that_drops_the_rule(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        del defs["workflow_action_outcome_predicates"]["description"]
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_stale_projected_wait_variant(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        defs["work_transition_action_variant_add_condition"] = {"type": "object"}
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_stale_projected_public_wait_variant(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        defs["work_transition_action_public_variant_add_condition"] = {"type": "object"}
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_generator_refuses_a_predicate_description_reduced_to_markers(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        defs["workflow_action_outcome_predicates"]["description"] = "decidable at delivery; raised_from"
        with self.assertRaises(ValueError):
            generator.require_delivery_rule_teaching(defs)

    def test_no_shipped_wait_teaching_remains(self):
        # The wait rule's only carrier retired: no live action declares the
        # wait field, and the projected surface carries no add_condition
        # variant for a stale teaching to hide behind.
        self.assertNotIn("work_transition_action_variant_add_condition", payload_schema["$defs"])
        self.assertNotIn("work_transition_action_public_variant_add_condition", payload_schema["$defs"])
        for name, schema in payload_schema["$defs"].items():
            self.assertNotIn(
                "expected_within_seconds", schema.get("properties", {}),
                f"{name} carries a wait field with no live owner",
            )


def _schema_sample(node, root, path="$"):
    """Build one value a schema node admits, resolving local $refs.

    Bounded sampler for the variant-projection tests: it satisfies the
    keyword disciplines the repository's schemas use (const, enum, type,
    bounds, patterns, required) and fails loudly when it meets a shape it
    cannot sample, so a drifted def cannot silently pass as authorable.
    """
    if not isinstance(node, dict):
        raise AssertionError(f"unsamplable schema node at {path}: {node!r}")
    if "$ref" in node:
        ref = node["$ref"]
        target = root.get("$defs", {}).get(ref.removeprefix("#/$defs/"))
        if target is None:
            raise AssertionError(f"missing schema ref {ref} at {path}")
        merged = {key: value for key, value in node.items() if key != "$ref"}
        return _schema_sample({**target, **merged} if merged else target, root, path)
    if "const" in node:
        return node["const"]
    if "enum" in node:
        return node["enum"][0]
    if "oneOf" in node and "type" not in node and "properties" not in node:
        value = _schema_sample(node["oneOf"][0], root, path)
        generator.schema_validate(value, node, root)
        return value
    types = node.get("type")
    types = types if isinstance(types, list) else [types] if types else []
    kind = next((candidate for candidate in ("object", "array", "string", "integer", "number", "boolean") if candidate in types), None)
    if kind is None and "properties" in node:
        kind = "object"
    if kind == "object":
        value = {}
        for name in node.get("required", []):
            value[name] = _schema_sample(node["properties"][name], root, f"{path}.{name}")
        return value
    if kind == "array":
        minimum = node.get("minItems", 0)
        return [_schema_sample(node.get("items", {}), root, f"{path}[]") for _ in range(max(minimum, 0))]
    if kind in ("integer", "number"):
        return node.get("minimum", 1)
    if kind == "boolean":
        return False
    if kind == "string":
        minimum, maximum = node.get("minLength", 0), node.get("maxLength", float("inf"))
        pattern = node.get("pattern")
        if pattern is not None:
            candidates = ["id-1", "conformance", "TestOne", "predicate:x", "control:c-1", "case:c-1", "owner:o-1",
                          "finding:1:1", "sha256:" + "0" * 64, "0" * 40, "msg:" + "0" * 32,
                          "fence:prod-pause", "https://example.test/pull/1", "2026-08-08T00:00:00Z"]
            for candidate in candidates:
                if re.search(pattern, candidate) and minimum <= len(candidate) <= maximum:
                    return candidate
            raise AssertionError(f"no canned value matches pattern {pattern} within {minimum}..{maximum} at {path}")
        return "x" * max(minimum, 1)
    raise AssertionError(f"schema node names no samplable type at {path}: {sorted(node)}")


def _action_condition(defs, action_id):
    """The generated closed envelope one action_id carries."""
    return defs[f"work_transition_action_variant_{action_id}"]


def _condition_branches(then):
    fields = then["properties"]["fields"]
    alternatives = fields.get("oneOf", fields.get("anyOf"))
    return [{"properties": {"fields": branch}} for branch in alternatives] if alternatives else [then]


def _fields_object(branch):
    fields = branch.get("properties", {}).get("fields")
    if not isinstance(fields, dict):
        raise AssertionError(f"branch names no fields object: {branch!r}")
    return fields


class WorkflowActionVariantProjectionTests(unittest.TestCase):
    """An action whose current families declare different closed
    fieldsets projects as the union of those exact closed alternatives, not
    one merged shape. The store keeps validating each call against the
    pinned definition version; these tests hold the authoring surface to
    the same exactness.
    """

    @classmethod
    def setUpClass(cls):
        cls.actions, cls.workflows, cls.teaching, cls.schema_refs = generator.load_workflow_action_contracts()

    def action(self, action_id):
        return next(action for action in self.actions if action["id"] == action_id)

    def test_projection_records_carry_the_variant_shape(self):
        for action in self.actions:
            self.assertEqual(set(action), {"id", "variants", "cross_field"},
                             f"{action.get('id')} is not the closed variant record")
            self.assertTrue(action["variants"], f"{action.get('id')} names no variant")
            pairs = set()
            for variant in action["variants"]:
                self.assertEqual(set(variant), {"payload", "public_payload"})
                for payload_key in ("payload", "public_payload"):
                    payload = variant[payload_key]
                    self.assertEqual(set(payload), {"closed", "fields"}, f"{action['id']} variant is open")
                    self.assertIs(payload["closed"], True, f"{action['id']} variant is not closed")
                pairs.add((json.dumps(variant["payload"], sort_keys=True), json.dumps(variant["public_payload"], sort_keys=True)))
            self.assertEqual(len(pairs), len(action["variants"]), f"{action['id']} repeats a variant")

    def test_request_correction_eliminates_the_contained_alternative(self):
        # The optional open_finding_ids contract contains every valid input
        # of the same contract without that property. One closed branch is
        # their exact union, not a merge of unrelated fieldsets.
        action = self.action("request_correction")
        self.assertEqual(len(action["variants"]), 2)
        fieldsets = [[field["name"] for field in variant["payload"]["fields"]] for variant in action["variants"]]
        self.assertIn("open_finding_ids", fieldsets[0])
        self.assertNotIn("open_finding_ids", fieldsets[1])
        then = _action_condition(payload_schema["$defs"], "request_correction")
        branches = _condition_branches(then)
        self.assertEqual(len(branches), 1, "a contained alternative adds no accepted input")
        for branch in branches:
            fields = _fields_object(branch)
            self.assertIs(fields.get("additionalProperties"), False, "an alternative is not a closed object")
            self.assertIn("open_finding_ids", fields["properties"])

    def test_historical_layouts_are_authorable_nowhere(self):
        # The legacy input union is gone: the core input is exactly the
        # published live variants, so a historical oracle-free layout is
        # authorable nowhere. Its replay side is the frozen historical
        # decoder, not this document (CD-0218 D4).
        defs = payload_schema["$defs"]
        self.assertNotIn("work_transition_action_legacy_input", defs)
        current = _condition_branches(_action_condition(defs, "record_worker_job"))
        envelope = {"work_id": "work-1", "expected_version": 1, "action_id": "record_worker_job", "idempotency_key": "job-1"}
        ordinary = [branch for branch in current if "acceptance_oracle" in _fields_object(branch).get("required", [])]
        self.assertEqual(len(ordinary), 1)
        splits = [branch for branch in current if "split" in _fields_object(branch).get("required", [])]
        for branch in ordinary + splits:
            fields = _schema_sample(_fields_object(branch), payload_schema)
            historical = copy.deepcopy(fields)
            if "split" in historical:
                for successor in historical["split"]["successors"]:
                    successor.pop("acceptance_oracle")
            else:
                historical.pop("acceptance_oracle")
            for name in ("work_transition_action_input", "work_transition_action_public_input"):
                generator.schema_validate(envelope | {"fields": fields}, defs[name], payload_schema, name)
                with self.assertRaises(ValueError, msg=f"{name} admits a historical layout"):
                    generator.schema_validate(envelope | {"fields": historical}, defs[name], payload_schema, name)

    def test_sampler_preserves_declared_string_bounds(self):
        digest = {"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}
        self.assertEqual(_schema_sample(digest, payload_schema), "sha256:" + "0" * 64)
        with self.assertRaises(AssertionError):
            _schema_sample(digest | {"maxLength": 64}, payload_schema)

    def test_no_merged_union_admits_combinations_no_definition_allows(self):
        defs = payload_schema["$defs"]
        branches = _condition_branches(_action_condition(defs, "record_worker_job"))
        oracle_branch = next(branch for branch in branches if "acceptance_oracle" in _fields_object(branch).get("required", []))
        # A malformed oracle value passes no definition: the current variant
        # refuses the value, and the retained shapes never declared the
        # field at all. A merged optional-union would admit both payloads.
        broken = _schema_sample(_fields_object(oracle_branch), payload_schema)
        broken["acceptance_oracle"] = {"owners": "not-an-array"}
        call = {"work_id": "work-1", "expected_version": 1, "action_id": "record_worker_job", "idempotency_key": "job-1", "fields": broken}
        for name in ("work_transition_action_input", "work_transition_action_public_input"):
            with self.assertRaises(ValueError, msg=f"{name} admits a broken oracle"):
                generator.schema_validate(call, defs[name], payload_schema, "broken")
        # Dropping a field every alternative requires refuses both shapes.
        request_correction = _condition_branches(_action_condition(defs, "request_correction"))
        sample = _schema_sample(_fields_object(request_correction[0]), payload_schema)
        sample.pop("diagnosis")
        for branch in request_correction:
            with self.assertRaises(ValueError):
                generator.schema_validate(sample, _fields_object(branch), payload_schema, "missing-required")

    def test_unknown_fields_are_refused_by_every_alternative(self):
        defs = payload_schema["$defs"]
        for action_id in ("request_correction", "record_worker_job"):
            branches = _condition_branches(_action_condition(defs, action_id))
            sample = _schema_sample(_fields_object(branches[0]), payload_schema)
            sample["mystery"] = True
            for branch in branches:
                with self.assertRaises(ValueError):
                    generator.schema_validate(sample, _fields_object(branch), payload_schema, "unknown")

    def test_single_variant_actions_keep_one_unwrapped_condition(self):
        then = _action_condition(payload_schema["$defs"], "accept_worker_result")
        self.assertNotIn("anyOf", then["properties"]["fields"], "an action with one current contract must stay a single branch")

    def test_the_public_dispatch_override_remains_correct(self):
        # CD-0242 D14 has one packet; public authoring remains lane identity alone.
        action = self.action("dispatch_worker")
        self.assertEqual(len(action["variants"]), 1)
        for variant in action["variants"]:
            self.assertNotEqual(variant["payload"], variant["public_payload"])
            self.assertEqual([field["name"] for field in variant["public_payload"]["fields"]], ["lane_id"])
        packet_refs = {
            field["schema_ref"]
            for variant in action["variants"]
            for field in variant["payload"]["fields"]
            if field["name"] == "worker_packet"
        }
        self.assertEqual(packet_refs, {"worker_packet"})
        internal = payload_schema["$defs"]["work_transition_action_variant_dispatch_worker"]["properties"]["fields"]
        branches = internal.get("anyOf", internal.get("oneOf", [internal]))
        self.assertEqual(len(branches), 1)
        for fields in branches:
            self.assertEqual(sorted(fields["properties"]), ["attempt_id", "worker_packet"])
        public = payload_schema["$defs"]["work_transition_action_public_variant_dispatch_worker"]["properties"]["fields"]
        self.assertEqual(sorted(public["properties"]), ["lane_id"])


class LiveAuthoringClosureTests(unittest.TestCase):
    """The public authoring surface derives from the live heads and the
    recovery list only. Retired published actions carry no
    authoring variant, the outcome union narrows to what the live heads pin,
    and the frozen historical decoder keeps the retired vocabulary byte for
    byte for the released pins (CD-0218 D1/D4).
    """

    @classmethod
    def setUpClass(cls):
        cls.actions, cls.workflows, cls.teaching, cls.schema_refs = generator.load_workflow_action_contracts()

    def test_the_live_projection_is_the_approved_closed_set(self):
        live_ids = {action["id"] for action in self.actions}
        self.assertNotIn("record_worker_failure", live_ids)
        for action_id in RETIRED_PUBLISHED_ACTIONS:
            self.assertNotIn(action_id, live_ids, f"retired action {action_id} is authorable")
        self.assertIn("record_diagnosis", live_ids)

    def test_the_live_outcome_union_is_the_research_trio_without_a_decision_record(self):
        defs = payload_schema["$defs"]
        live_tokens = sorted({token for workflow in self.workflows for token in workflow["allowed_outcome_tokens"]})
        self.assertEqual(set(live_tokens), {"no_change", "resolved", "report_recorded"})
        allowed = defs["workflow_outcome_outcome"]["properties"]["allowed"]
        self.assertEqual(allowed["items"]["enum"], live_tokens)
        self.assertEqual(allowed["maxItems"], len(live_tokens))
        self.assertNotIn("decision_record", defs["workflow_outcome_outcome"]["properties"])
        self.assertNotIn("workflow_outcome_decision_record", defs)
        # The four predicate kinds survive with their per-kind payloads, and
        # every check result token the historical readers know stays.
        kinds = {branch["properties"]["outcome_kind"]["const"]
                 for branch in defs["workflow_action_outcome_predicates"]["items"]["oneOf"]}
        self.assertEqual(kinds, {"exists", "absent", "outcome", "check"})
        check_tokens = defs["workflow_outcome_check"]["properties"]["expected_result"]["enum"]
        self.assertEqual(sorted(check_tokens), sorted(["true", "false", "pass", "fail", "present",
                                                       "absent", "healthy", "unhealthy", "accepted", "rejected"]))
        self.assertEqual(defs["workflow_outcome_absent"]["properties"]["distinguish_from"]["items"]["enum"],
                         ["archived", "relocated", "renamed", "disabled"])
        # No authoring def names the retired tokens or a decision record.
        retired_tokens = {"accepted_decision", "insufficient_evidence", "remediated", "completed", "operator_defined"}
        text = json.dumps({name: defs[name] for name in ("workflow_action_outcome", "workflow_outcome_outcome",
                                                         "workflow_action_outcome_predicates",
                                                         "workflow_outcome_payload", "worker_packet_outcome_predicates")},
                          sort_keys=True)
        for token in retired_tokens:
            self.assertNotIn(f'"{token}"', text, f"retired outcome token {token} is still authorable")
        self.assertNotIn("decision_record", text)
        # The work pin replays stored predicates unchanged, so it keeps the
        # retained vocabulary and decision record that authoring narrows away.
        retained = defs["workflow_retained_outcome"]["properties"]
        self.assertEqual(retained["allowed"]["maxItems"], 8)
        self.assertTrue(retired_tokens <= set(retained["allowed"]["items"]["enum"]))
        self.assertIn("decision_record", retained)
        criterion = json.dumps(defs["work_pin_verified_criterion"], sort_keys=True)
        self.assertIn('"#/$defs/workflow_retained_outcome"', criterion)
        self.assertNotIn('"#/$defs/workflow_outcome_outcome"', criterion)

    def test_current_schema_refs_resolve_in_the_public_document(self):
        self.assertEqual(set(self.schema_refs), {"current"})
        generator.validate_current_schema_refs(self.schema_refs, payload_schema["$defs"])

    def test_current_schema_refs_refuse_missing_or_dangling_definitions(self):
        for defs in ({}, {"root": {"$ref": "#/$defs/missing"}}):
            with self.subTest(defs=defs), self.assertRaises(ValueError):
                generator.validate_current_schema_refs({"current": ["root"]}, defs)


class ProjectionAdmissionTests(unittest.TestCase):
    """The projection loader checks structure, not a second action catalog."""

    @staticmethod
    def canned(actions):
        teaching = {rule: "rule" for rule in (
            "predicate_ordinal_rule", "law_addition_mandate_rule", "obligation_membership_rule",
            "architecture_domain_rule", "alignment_combination_rule", "outcome_kind_equality_rule")}
        return json.dumps({
            "schema_version": "2.0", "actions": actions, "workflows": [],
            "teaching": teaching, "schema_refs": {"current": []},
        })

    def load(self, projection):
        with unittest.mock.patch.object(generator.subprocess, "run") as run:
            run.return_value.stdout = projection
            generator.load_workflow_action_contracts()

    def test_the_projection_refuses_an_action_without_a_contract(self):
        fake = self.canned([{"id": "action", "variants": [], "cross_field": {}}])
        with self.assertRaises(ValueError) as caught:
            self.load(fake)
        self.assertIn("names no variant", str(caught.exception))

    def test_the_projection_refuses_duplicate_actions(self):
        filler = [{"id": "same-action"}, {"id": "same-action"}]
        with self.assertRaises(ValueError) as caught:
            self.load(self.canned(filler))
        self.assertIn("no unique actions", str(caught.exception))

    def test_the_projection_refuses_an_old_projection_version(self):
        fake = json.dumps({
            "schema_version": "1.0",
            "actions": [{"id": "record_verdict", "variants": [], "legacy_payloads": [], "cross_field": {}}],
            "workflows": [], "teaching": {}, "schema_refs": {"current": []},
        })
        with self.assertRaises(ValueError) as caught:
            self.load(fake)
        self.assertIn("projection is invalid", str(caught.exception))


class NativeOracleLaneInstructionTests(unittest.TestCase):
    def test_native_producer_stays_host_owned_and_raw_subject_is_dispatch_owned(self):
        spec = importlib.util.spec_from_file_location("native_lane_generator", ROOT / "scripts/generate-agent-lanes.py")
        lanes = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(lanes)
        text = lanes.acceptance_oracle_instructions(packet_schema, {"id": "implement"})
        for claim in ("authenticated host", "native_oracle_v2", "without test execution", "readiness only",
                      "Workers gain no general Concord tools", "Ordinary test output",
                      "Never author `subject_commit`", "never", "invent a locator",
                      "target_runner_v1", "materializer", "runner", "collection"):
            self.assertIn(claim, text)
        self.assertNotIn("candidate_subject", text)
        self.assertNotIn("Run the declared controls within your lane permissions", text)
        self.assertNotIn("Managed workers gain no Concord tools", text)


class HostSafeWorkflowFieldUnionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        actions, workflows, teaching, _ = generator.load_workflow_action_contracts()
        cls.projected, _ = generator.project_workflow_action_schema(payload_schema, actions, workflows, teaching)
        cls.actions = actions
        cls.workflows = workflows
        cls.teaching = teaching

    def test_common_decision_fields_are_published_but_refused_outside_decision(self):
        for action in self.actions:
            schema = self.projected["$defs"]["work_transition_action_variant_" + action["id"]]
            for field in ("selected_choice", "decision_context_digest"):
                self.assertIn(field, schema["properties"], action["id"])
            if action["id"] == "confirm_premise":
                continue
            sample = _schema_sample(schema, self.projected)
            generator.schema_validate(sample, schema, self.projected)
            for field, value in (("selected_choice", "confirm"), ("decision_context_digest", "sha256:" + "a" * 64)):
                with self.assertRaises(ValueError, msg=action["id"] + ":" + field):
                    generator.schema_validate(sample | {field: value}, schema, self.projected)

    def test_accept_worker_result_preserves_overlapping_valid_layouts_without_union(self):
        fields = self.projected["$defs"]["work_transition_action_variant_accept_worker_result"]["properties"]["fields"]
        self.assertNotIn("anyOf", fields)
        self.assertNotIn("oneOf", fields, "the two-field contract is contained in the optional-delivery contract")
        base = {"attempt_id": "attempt-1", "attempt_epoch": 1}
        for delivery in ({}, {"delivery_artifact": "artifact:1"}, {"delivery_state": "asserted"},
                         {"delivery_artifact": "artifact:1", "delivery_state": "asserted"}):
            generator.schema_validate(base | delivery, fields, self.projected)
        for bad in ({"attempt_id": "attempt-1"}, {"attempt_epoch": 1}, base | {"attempt_epoch": 0},
                    base | {"delivery_state": "pending"}, base | {"delivery_artifact": ""}, base | {"unknown": True}):
            with self.assertRaises(ValueError):
                generator.schema_validate(bad, fields, self.projected)

    def test_retired_published_actions_project_nowhere(self):
        # A retired published action (CD-0218 D1/D2) has no authoring
        # variant after projection, even when the projected document starts
        # from a previous generation that still carried one: the drop is
        # explicit, and the closed core input names only live action IDs.
        projected, _ = generator.project_workflow_action_schema(payload_schema, self.actions, self.workflows, self.teaching)
        defs = projected["$defs"]
        live_ids = {action["id"] for action in self.actions}
        for action_id in sorted(RETIRED_PUBLISHED_ACTIONS):
            self.assertNotIn(f"work_transition_action_variant_{action_id}", defs, action_id)
            self.assertNotIn(f"work_transition_action_public_variant_{action_id}", defs, action_id)
        consts = set()
        for name, schema in defs.items():
            # A multi-branch action's field union publishes helper member
            # defs beside the action's own variant defs; only the variant
            # defs carry the action_id const the retirement check scans.
            if name.startswith("work_transition_action_variant_") and "_fields_member_" not in name:
                consts.add(schema["properties"]["action_id"]["const"])
        self.assertEqual(consts, live_ids)
        union = defs["work_transition_action_input"]
        for action_id in sorted(RETIRED_PUBLISHED_ACTIONS):
            call = {"work_id": "work-1", "expected_version": 1, "action_id": action_id, "idempotency_key": "x-1"}
            with self.assertRaises(ValueError, msg=f"core input admits retired action {action_id}"):
                generator.schema_validate(call, union, projected)

    def test_partial_overlap_is_disjoint_without_merging_fieldsets(self):
        base = {"type": "object", "additionalProperties": False, "required": ["id"],
                "properties": {"id": {"type": "string"}}}
        left = copy.deepcopy(base)
        right = copy.deepcopy(base)
        left["properties"]["left"] = {"type": "boolean"}
        right["properties"]["right"] = {"type": "boolean"}
        defs = {}
        union = generator.closed_field_union([left, right], defs, "overlap")
        self.assertNotIn("anyOf", union)
        self.assertEqual(len(union["oneOf"]), 2)
        for sample in ({"id": "x"}, {"id": "x", "left": True}, {"id": "x", "right": False}):
            generator.schema_validate(sample, union, {"$defs": defs})
        for sample in ({"id": "x", "left": True, "right": True}, {"id": "x", "left": "true"}, {}):
            with self.assertRaises(ValueError):
                generator.schema_validate(sample, union, {"$defs": defs})

    def test_equivalent_required_order_keeps_one_representative(self):
        left = {"type": "object", "additionalProperties": False, "required": ["one", "two"],
                "properties": {"one": {"type": "string"}, "two": {"type": "string"}}}
        right = copy.deepcopy(left)
        right["required"].reverse()
        union = generator.closed_field_union([left, right], {}, "equivalent")
        self.assertEqual(union, left)
        generator.schema_validate({"one": "1", "two": "2"}, union, {})

    def test_required_const_discriminators_need_no_exclusions(self):
        branches = [{"type": "object", "additionalProperties": False, "required": ["kind"],
                     "properties": {"kind": {"const": kind}}} for kind in ("first", "second", "third")]
        union = generator.closed_field_union(branches, {}, "const")
        self.assertEqual(union, {"oneOf": branches})
        for kind in ("first", "second", "third"):
            generator.schema_validate({"kind": kind}, union, {})
        for sample in ({}, {"kind": "other"}, {"kind": "first", "extra": True}):
            with self.assertRaises(ValueError):
                generator.schema_validate(sample, union, {})

    def test_required_key_forbidden_by_other_closed_branch_needs_no_exclusion(self):
        branches = [{"type": "object", "additionalProperties": False, "required": [key],
                     "properties": {key: {"type": "boolean"}}} for key in ("left", "right")]
        union = generator.closed_field_union(branches, {}, "key")
        self.assertEqual(union, {"oneOf": branches})
        for sample in ({"left": True}, {"right": False}):
            generator.schema_validate(sample, union, {})
        for sample in ({}, {"left": True, "right": False}):
            with self.assertRaises(ValueError):
                generator.schema_validate(sample, union, {})

    def test_optional_const_does_not_prove_disjointness(self):
        branches = [{"type": "object", "additionalProperties": False,
                     "properties": {"kind": {"const": kind}}} for kind in ("first", "second")]
        defs = {}
        union = generator.closed_field_union(branches, defs, "optional")
        self.assertEqual(union["oneOf"][1]["not"], {"$ref": "#/$defs/optional_fields_member_0"})
        self.assertEqual(defs["optional_fields_member_0"], branches[0])
        for sample in ({}, {"kind": "first"}, {"kind": "second"}):
            generator.schema_validate(sample, union, {"$defs": defs})

    def test_open_or_pattern_allowed_keys_do_not_prove_disjointness(self):
        left = {"type": "object", "additionalProperties": False, "required": ["left"],
                "properties": {"left": {"type": "boolean"}}}
        for other in ({"type": "object", "properties": {"right": {"type": "boolean"}}},
                      {"type": "object", "additionalProperties": False,
                       "patternProperties": {"^left$": {"type": "boolean"}}}):
            with self.subTest(other=other):
                defs = {}
                union = generator.closed_field_union([left, other], defs, "open")
                self.assertIn("not", union["oneOf"][1])
                generator.schema_validate({"left": True}, union, {"$defs": defs})

    def test_overlapping_exclusions_share_original_members_and_preserve_not(self):
        branches = [{"type": "object", "additionalProperties": False,
                     "properties": {"id": {"type": "string"}, key: {"type": "boolean"}},
                     "required": ["id"]} for key in ("left", "middle", "right")]
        branches[1]["not"] = {"properties": {"middle": {"const": False}}, "required": ["middle"]}
        originals = copy.deepcopy(branches)
        defs = {}
        union = generator.closed_field_union(branches, defs, "shared")
        first_ref = {"$ref": "#/$defs/shared_fields_member_0"}
        self.assertEqual(union["oneOf"][1]["not"], {"anyOf": [branches[1]["not"], first_ref]})
        self.assertEqual(union["oneOf"][2]["not"], {"anyOf": [first_ref, {"$ref": "#/$defs/shared_fields_member_1"}]})
        self.assertEqual(defs, {"shared_fields_member_0": branches[0], "shared_fields_member_1": branches[1]})
        self.assertEqual(branches, originals)
        for sample in ({"id": "x"}, {"id": "x", "left": True}, {"id": "x", "middle": True},
                       {"id": "x", "middle": False}, {"id": "x", "right": False},
                       {"id": "x", "left": True, "right": True}, {}, {"id": False}):
            expected = any(generator._valid(sample, branch, {"$defs": defs}, "sample") for branch in originals)
            self.assertEqual(generator._valid(sample, union, {"$defs": defs}, "sample"), expected, sample)

    def test_union_reprojection_removes_only_its_owned_definitions(self):
        branches = [{"type": "object", "additionalProperties": False,
                     "properties": {key: {"type": "boolean"}}} for key in ("left", "right")]
        defs = {"unrelated": {"type": "string"}}
        first = generator.closed_field_union(branches, defs, "repeat")
        original_defs = copy.deepcopy(defs)
        self.assertEqual(generator.closed_field_union(branches, defs, "repeat"), first)
        self.assertEqual(defs, original_defs)
        generator.closed_field_union([branches[0]], defs, "repeat")
        self.assertEqual(defs, {"unrelated": {"type": "string"}})

    def test_reference_siblings_are_not_direct_disjointness_evidence(self):
        branches = [{"type": "object", "additionalProperties": False, "required": ["kind"],
                     "properties": {"kind": {"$ref": "#/$defs/kind", "const": kind}}}
                    for kind in ("first", "second")]
        defs = {"kind": {"type": "string"}}
        union = generator.closed_field_union(branches, defs, "refs")
        self.assertIn("not", union["oneOf"][1])
        generator.schema_validate({"kind": "other"}, union, {"$defs": defs})
        for branch in branches:
            branch["$ref"] = "#/$defs/object"
        self.assertFalse(generator.field_variants_disjoint(*branches))


class NativeOracleRequestProjectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        actions, workflows, teaching, _ = generator.load_workflow_action_contracts()
        cls.projected, _ = generator.project_workflow_action_schema(payload_schema, actions, workflows, teaching)

    def validate(self, value, name):
        generator.schema_validate(value, self.projected["$defs"][name], self.projected)

    def test_prepare_and_execute_are_closed_disjoint_native_requests(self):
        defs = self.projected["$defs"]
        control = _schema_sample(defs["worker_oracle_control"], self.projected)
        control.pop("readiness_evidence_refs")
        bundle = {"owner": _schema_sample(defs["worker_oracle_owner"], self.projected), "control": control,
                  "cases": [_schema_sample(defs["worker_oracle_case"], self.projected)]}
        prepare = {"work_id": "work-1", "idempotency_key": "prepare-1", "oracle": {
            "phase": "prepare", "expected_contract_version": 1, "control_bundle": bundle}}
        execute = {"work_id": "work-1", "idempotency_key": "execute-1", "oracle": {
            "phase": "execute", "attempt_id": "attempt-1", "attempt_epoch": 1,
            "worker_packet_digest": "sha256:" + "a" * 64,
            "worker_job_binding": {"job_id": "job:1", "revision": 1, "digest": "sha256:" + "b" * 64},
            "control_id": "control:1", "preparation_run_ref": "worktree_verify:prepare-1"}}
        name = "work_transition_worktree_verify_input"
        for value in (prepare, execute, {"work_id": "work-1", "idempotency_key": "legacy-1", "command": ["true"]}):
            self.validate(value, name)
        for value in (prepare | {"command": ["true"]}, execute | {"command": []},
                      {"work_id": "work-1", "idempotency_key": "no-variant"}):
            with self.assertRaises(ValueError):
                self.validate(value, name)
        for value, foreign in ((prepare, execute["oracle"]), (execute, prepare["oracle"])):
            for field, content in foreign.items():
                if field == "phase":
                    continue
                invalid = copy.deepcopy(value)
                invalid["oracle"][field] = content
                with self.assertRaises(ValueError, msg=field):
                    self.validate(invalid, name)
            for field in ("subject_commit", "project_id", "cwd", "recipe_source", "stdout", "native_plan_sha256"):
                invalid = copy.deepcopy(value)
                invalid["oracle"][field] = "caller-assertion"
                with self.assertRaises(ValueError, msg=field):
                    self.validate(invalid, name)
        invalid = copy.deepcopy(prepare)
        invalid["oracle"]["control_bundle"]["control"]["readiness_evidence_refs"] = ["invented:ready"]
        with self.assertRaises(ValueError):
            self.validate(invalid, name)
        self.assertIn("readiness_evidence_refs", defs["worker_oracle_control"]["required"])
        # The prepare teaching names both recipe families explicitly: the
        # pure-Go harness compiles, a target_runner_v1 recipe runs only its
        # collection or check-input stage, and both grant readiness only.
        description = defs["native_oracle_request"]["description"]
        for claim in ("target_runner_v1", "compiles", "collection", "check-input", "readiness only"):
            self.assertIn(claim, description)
        self.assertNotIn("prepare compiles", description)

    def test_output_read_has_bounded_bytes_and_no_mutation_or_path_selector(self):
        name = "work_browse_worktree_inspect_input"
        page = {"work_id": "work-1", "mode": "oracle_output", "run_ref": "worktree_verify:run-1",
                "stream": "stdout", "offset": 0, "length": 16384}
        for stream in ("stdout", "stderr"):
            self.validate(page | {"stream": stream}, name)
        for mode in ("status", "diff", "file"):
            self.validate({"work_id": "work-1", "mode": mode, "path": "fixture.txt"}, name)
            with self.assertRaises(ValueError):
                self.validate(page | {"mode": mode}, name)
        for change in ({"path": "fixture.txt"}, {"project_id": "foreign"}, {"idempotency_key": "page-1"},
                       {"offset": -1}, {"length": 0}, {"length": 16385}, {"stream": "combined"}):
            with self.assertRaises(ValueError):
                self.validate(page | change, name)
        for field in ("run_ref", "stream", "offset", "length"):
            missing = dict(page)
            missing.pop(field)
            with self.assertRaises(ValueError):
                self.validate(missing, name)

    def test_native_metadata_is_closed_bounded_and_phase_specific(self):
        defs = self.projected["$defs"]
        preparation = _schema_sample(defs["native_oracle_preparation"], self.projected)
        preparation["case_to_test_map"] = {"case:one": ["TestOne"]}
        preparation["selected_test_names"] = ["TestOne"]
        preparation["selected_distinct_count"] = 1
        preparation["streams_complete"] = True
        for stream in ("stdout", "stderr"):
            preparation[stream]["stream"] = stream
            preparation[stream]["complete"] = True
            preparation[stream]["ref"] = "oracle_output:" + stream
        self.validate(preparation, "native_oracle_preparation")
        self.validate(preparation | {"case_to_test_map": {"case:" + "a" * 123: ["TestOne"]}}, "native_oracle_preparation")
        for cases in ({}, {"not-a-case": ["TestOne"]}, {"case:" + "a" * 124: ["TestOne"]},
                      {"case:one": []}, {"case:one": ["not-a-test"]},
                      {f"case:{index}": ["TestOne"] for index in range(65)}):
            with self.assertRaises(ValueError):
                self.validate(preparation | {"case_to_test_map": cases}, "native_oracle_preparation")
        for field in ("candidate_subject", "output", "data_base64", "stdout_blob", "native_plan_json"):
            with self.assertRaises(ValueError):
                self.validate(preparation | {field: "not-native-metadata"}, "native_oracle_preparation")
        for subject in ("commit:" + "a" * 40, "a" * 41, "not-an-oid"):
            with self.assertRaises(ValueError):
                self.validate(preparation | {"subject_commit": subject}, "native_oracle_preparation")
        for stream in ("stdout", "stderr"):
            invalid = copy.deepcopy(preparation)
            invalid[stream]["length"] = 2097153
            with self.assertRaises(ValueError):
                self.validate(invalid, "native_oracle_preparation")
        prepare_result = preparation | {"control_id": "control:one", "stages": []}
        self.validate(prepare_result, "native_oracle_result")
        execute = prepare_result | {"phase": "execute", "qualification": "pass", "attempt_id": "attempt-1",
            "attempt_epoch": 1, "worker_packet_digest": "sha256:" + "a" * 64,
            "worker_job_binding": {"job_id": "job:one", "revision": 1, "digest": "sha256:" + "b" * 64},
            "preparation_run_ref": "run:prepare", "authorization_event_id": "event:dispatch", "authorization_seq": 9, "start_seq": 8,
            "input_manifest_digest": "sha256:" + "c" * 64, "binary_digest": "sha256:" + "d" * 64, "observed_test_names": ["TestOne"]}
        self.validate(execute, "native_oracle_result")
        for invalid in (prepare_result | {"attempt_id": "attempt-1"}, execute | {"qualification": "ready"},
                        prepare_result | {"qualification": "pass"}):
            with self.assertRaises(ValueError):
                self.validate(invalid, "native_oracle_result")
        for field in ("worker_job_binding", "authorization_event_id", "preparation_run_ref"):
            missing = dict(execute)
            missing.pop(field)
            with self.assertRaises(ValueError):
                self.validate(missing, "native_oracle_result")
        incomplete = copy.deepcopy(execute)
        incomplete["streams_complete"] = False
        with self.assertRaises(ValueError):
            self.validate(incomplete, "native_oracle_result")
        go_pass_without_witnesses = {key: value for key, value in execute.items() if key != "observed_test_names"}
        with self.assertRaises(ValueError):
            self.validate(go_pass_without_witnesses, "native_oracle_result")
        # The target_runner_v1 family is the second closed branch of the same
        # record: the discriminator and both axes are required, real witness
        # IDs replace the Go test-name grammar, and the legacy Go fields are
        # forbidden. Only the three admitted pairs validate.
        target = {key: value for key, value in preparation.items()
                  if key not in ("selected_test_names", "case_to_test_map")}
        witness = "tests/test_billing.py::test_invoice_total"
        target.update({
            "recipe_kind": "target_runner_v1", "target_materializer": "uv", "target_runner": "pytest",
            "selected_witness_ids": [witness], "case_to_witness_map": {"case:one": [witness]},
        })
        self.validate(target, "native_oracle_preparation")
        for pair in ({"target_materializer": "none", "target_runner": "check"},
                     {"target_materializer": "uv", "target_runner": "check"}):
            self.validate(target | pair, "native_oracle_preparation")
        partial_target = {key: value for key, value in preparation.items() if key != "selected_test_names"}
        partial_target.update({"recipe_kind": "target_runner_v1", "target_materializer": "uv",
                               "target_runner": "pytest", "selected_witness_ids": [witness],
                               "case_to_witness_map": {"case:one": [witness]}})
        for invalid in (
            target | {"target_materializer": "none", "target_runner": "pytest"},
            target | {"target_materializer": "future", "target_runner": "pytest"},
            target | {"target_runner": "future"},
            target | {"recipe_kind": "target_runner_v2"},
            target | {"selected_test_names": ["TestOne"]},
            target | {"case_to_test_map": {"case:one": ["TestOne"]}},
            partial_target,
            target | {"selected_witness_ids": []},
            target | {"selected_witness_ids": [witness, witness]},
            target | {"selected_witness_ids": ["x" * 257]},
            target | {"selected_witness_ids": ["na\x00l"]},
            target | {"case_to_witness_map": {}},
            target | {"case_to_witness_map": {"not-a-case": [witness]}},
            target | {"case_to_witness_map": {"case:one": [f"w{index}" for index in range(9)]}},
            target | {"selected_witness_ids": [f"w{index}" for index in range(41)],
                      "selected_distinct_count": 41,
                      "case_to_witness_map": {f"case:c{index}": [f"w{index}"] for index in range(41)}},
            # Cross-field joins the schema cannot express — count equality
            # and map-witness membership — are the readiness and core
            # consumer obligation, proven in the adapter's readiness tests.
        ):
            with self.assertRaises(ValueError):
                self.validate(invalid, "native_oracle_preparation")
        target_prepare_result = target | {"control_id": "control:one", "stages": []}
        self.validate(target_prepare_result, "native_oracle_result")
        target_execute = target_prepare_result | {"phase": "execute", "qualification": "pass", "attempt_id": "attempt-1",
            "attempt_epoch": 1, "worker_packet_digest": "sha256:" + "a" * 64,
            "worker_job_binding": {"job_id": "job:one", "revision": 1, "digest": "sha256:" + "b" * 64},
            "preparation_run_ref": "run:prepare", "authorization_event_id": "event:dispatch", "authorization_seq": 9, "start_seq": 8,
            "input_manifest_digest": "sha256:" + "c" * 64, "binary_digest": "sha256:" + "d" * 64,
            "observed_witness_ids": [witness]}
        self.validate(target_execute, "native_oracle_result")
        for invalid in (
            target_execute | {"observed_test_names": ["TestOne"]},
            {key: value for key, value in target_execute.items() if key != "observed_witness_ids"},
        ):
            with self.assertRaises(ValueError):
                self.validate(invalid, "native_oracle_result")
        for invalid in (execute | {"observed_witness_ids": ["w"]},):
            with self.assertRaises(ValueError):
                self.validate(invalid, "native_oracle_result")

    def test_projection_is_idempotent_and_keeps_navigation_and_report_pins(self):
        actions, workflows, teaching, _ = generator.load_workflow_action_contracts()
        repeated, _ = generator.project_workflow_action_schema(self.projected, actions, workflows, teaching)
        self.assertEqual(repeated, self.projected)
        generator.check_schema_keywords(self.projected)
        generator.check_payload_closed(self.projected)
        for name in ("work_context_view", "worker_oracle_receipt"):
            self.assertNotIn("candidate_subject", self.projected["$defs"][name]["properties"])
            self.assertIn("subject_commit", self.projected["$defs"][name]["properties"])
        # The card-free live view carries domain groups without the
        # retired domain_cards member; the released card-bearing shapes stay
        # in the versioned lane packet contract's branches only.
        self.assertNotIn("domain_cards", self.projected["$defs"]["work_context_domain_group"]["properties"])
        self.assertNotIn("domain_cards", self.projected["$defs"]["work_context_domain_group"]["required"])
        self.assertEqual(self.projected["$defs"]["worker_packet"]["properties"]["schema_version"]["enum"], ["2.0"])
        generator.project_worker_packet_inputs(repeated, packet_schema)
        self.assertEqual(repeated["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]["report_protocol"],
                         packet_schema["properties"]["inputs"]["properties"]["report_protocol"])

    def test_output_page_result_keeps_legacy_modes_and_exact_byte_bound(self):
        import base64
        page = {"run_ref": "worktree_verify:run", "stream": "stderr", "offset": 0,
                "data_base64": base64.b64encode(b"x" * 16384).decode(), "length": 16384,
                "total_length": 2097152, "sha256": "sha256:" + "a" * 64, "complete": True,
                "next_offset": 16384, "eof": False}
        result = {"work_id": "work-1", "project_id": "project-1", "branch": "", "path": "", "mode": "oracle_output",
                  "content": "", "truncated": False, "oracle_output": page}
        self.validate(result, "worktree_inspect_result")
        self.assertEqual(len(page["data_base64"]), 21848)
        with self.assertRaises(ValueError):
            self.validate(result | {"mode": "status"}, "worktree_inspect_result")
        invalid = copy.deepcopy(result)
        invalid["oracle_output"]["length"] = 16385
        with self.assertRaises(ValueError):
            self.validate(invalid, "worktree_inspect_result")
        for mode in ("status", "diff", "file"):
            legacy = {"work_id": "work-1", "project_id": "project-1", "branch": "work/work-1", "path": "/synthetic/tree", "mode": mode,
                      "content": "", "truncated": False}
            self.validate(legacy, "worktree_inspect_result")
            with self.assertRaises(ValueError):
                self.validate(legacy | {"path": ""}, "worktree_inspect_result")


class MutationApprovalPropertyTests(unittest.TestCase):
    """Every mutation can reach the cross-Product approval escalation: the
    runtime forces requiresApproval when the derived Product scope crosses the
    selected Product, and the approved resubmission carries input.approval.
    Each mutation input must declare the optional typed approval property, and
    the generator refuses a mutation input without it, so the declared surface
    and the runtime rule cannot disagree.
    """

    def mutation_approval_refusals(self, defs):
        refusals = []
        for operation in manifest["operations"]:
            if operation["kind"] != "mutation":
                continue
            schema = defs[operation["input_schema"].split("/")[-1]]
            try:
                generator.require_mutation_approval_property(operation, schema)
            except ValueError as err:
                refusals.append(str(err))
        return refusals

    def test_shipped_mutation_inputs_declare_optional_approval(self):
        self.assertEqual(self.mutation_approval_refusals(payload_schema["$defs"]), [])

    def test_a_mutation_input_without_the_approval_property_is_rejected(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        del defs["work_define_observation_record_input"]["properties"]["approval"]
        refusals = self.mutation_approval_refusals(defs)
        self.assertTrue(any("concord_work_define.observation_record" in f for f in refusals), refusals)

    def test_a_required_approval_property_is_rejected(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        schema = defs["work_define_observation_record_input"]
        schema["required"] = list(schema["required"]) + ["approval"]
        refusals = self.mutation_approval_refusals(defs)
        self.assertTrue(any("concord_work_define.observation_record" in f for f in refusals), refusals)

    def test_an_untyped_approval_property_is_rejected(self):
        defs = copy.deepcopy(payload_schema["$defs"])
        defs["work_define_observation_record_input"]["properties"]["approval"] = {"type": "string"}
        refusals = self.mutation_approval_refusals(defs)
        self.assertTrue(any("concord_work_define.observation_record" in f for f in refusals), refusals)


def workflow_job_steps(job_name: str) -> list[dict]:
    """One job's steps as name/run/env records.

    Pinned to the indents scripts/check-script-tests.py enforces (step names
    at six spaces, `run:` and `env:` at eight, their bodies at ten), so the
    read stays structural and needs no YAML parser on a contributor laptop.
    """
    text = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    steps: list[dict] = []
    current: dict | None = None
    section: str | None = None
    in_job = False
    for line in text.splitlines():
        stripped = line.strip()
        if (
            in_job
            and line.startswith("  ")
            and not line.startswith("   ")
            and not stripped.startswith("#")
            and stripped.endswith(":")
        ):
            break  # the next job's key ends this job
        if not in_job:
            if line == f"  {job_name}:":
                in_job = True
            continue
        if line.startswith("      - name:"):
            current = {"name": line.split(":", 1)[1].strip(), "run": "", "env": {}}
            steps.append(current)
            section = None
            continue
        if current is None:
            continue
        if line.startswith("        run:"):
            inline = line.split(":", 1)[1].strip()
            current["run"] = "" if inline in {"|", ">"} else inline
            section = "run"
        elif line.startswith("        env:"):
            section = "env"
        elif stripped and line.startswith("          "):
            if section == "run":
                current["run"] += "\n" + stripped
            elif section == "env" and ":" in stripped:
                key, _, value = stripped.partition(":")
                current["env"][key.strip()] = value.strip().strip('"')
        elif stripped:
            section = None
    return steps


def workflow_job_block(job_name: str) -> str:
    """The job's YAML text, from its key line to the next job-level key."""
    text = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    body: list[str] = []
    collecting = False
    for line in text.splitlines():
        if not collecting:
            collecting = line == f"  {job_name}:"
            if collecting:
                body.append(line)
            continue
        if (
            line.startswith("  ")
            and not line.startswith("   ")
            and not line.strip().startswith("#")
            and line.strip().endswith(":")
        ):
            break  # the next job's key ends this job
        body.append(line)
    return "\n".join(body)


def job_needs(block: str) -> list[str]:
    """The job's `needs` dependencies, inline or as a block list."""
    inline = re.search(r"^ {4}needs: (.+)$", block, re.MULTILINE)
    if inline:
        return re.findall(r"[a-z][a-z0-9-]*", inline.group(1))
    listed = re.search(r"^ {4}needs:\n((?: {6}- .+\n?)+)", block, re.MULTILINE)
    return re.findall(r"- ([a-z][a-z0-9-]*)", listed.group(1)) if listed else []


AGGREGATE_DEPENDENCIES = ("verify-history", "verify-contracts", "verify-adapter", "verify-tooling")


class CIContractRoutingTests(unittest.TestCase):
    """Each suite has one owner, and the required gate demands all owners pass."""

    def test_verify_contracts_runs_the_umbrella_with_both_external_suite_options(self):
        steps = workflow_job_steps("verify-contracts")
        direct = [step for step in steps if "scripts/check-agent-contracts.py" in step["run"]]
        self.assertEqual(
            direct,
            [],
            f"standalone complete-contract steps must not exist: {[step['name'] for step in direct]}",
        )
        for owner, command in (
            ("verify-adapter", "bun test adapter/opencode"),
            ("verify-tooling", "scripts/test-agent-contracts.py"),
        ):
            duplicated = [step for step in steps if command in step["run"]]
            self.assertEqual(
                duplicated,
                [],
                f"{command} belongs to {owner}; verify-contracts must pass the matching external-suite option instead",
            )
        umbrella = [step for step in steps if "scripts/check-json.py" in step["run"]]
        self.assertEqual(len(umbrella), 1, "expected exactly one JSON umbrella step in verify-contracts")
        for flag in ("--adapter-tests-external", "--contract-tests-external"):
            self.assertIn(
                flag,
                umbrella[0]["run"],
                f"the umbrella step must pass {flag}: that suite runs as its own CI step",
            )
        self.assertEqual(
            umbrella[0]["env"].get("CONCORD_REQUIRE_BUN"),
            "1",
            "the umbrella must run under CONCORD_REQUIRE_BUN=1 so the nested check fails closed",
        )

    def test_verify_adapter_runs_the_adapter_suite_directly(self):
        # The adapter_test evidence anchors resolve only against a direct
        # workflow invocation, so deleting the direct step would strand them.
        direct = [step for step in workflow_job_steps("verify-adapter-rest") if "bun test adapter/opencode" in step["run"]]
        self.assertTrue(direct, "verify-adapter-rest must run the adapter suite directly")
        aggregate = workflow_job_block("verify-adapter")
        self.assertEqual(job_needs(aggregate), ["verify-adapter-rest", "verify-adapter-dispatch"])
        self.assertRegex(aggregate, r"(?m)^ {4}if: always\(\)$")
        gate = workflow_job_steps("verify-adapter")
        self.assertEqual(len(gate), 1)
        self.assertEqual(gate[0]["env"], {
            "REST_RESULT": "${{ needs.verify-adapter-rest.result }}",
            "DISPATCH_RESULT": "${{ needs.verify-adapter-dispatch.result }}",
        })
        self.assertEqual(gate[0]["run"].strip().splitlines(), ['test "$REST_RESULT" = success', 'test "$DISPATCH_RESULT" = success'])

    def test_verify_tooling_runs_the_contract_selftest_directly(self):
        direct = [step for step in workflow_job_steps("verify-tooling") if "scripts/test-agent-contracts.py" in step["run"]]
        self.assertTrue(direct, "verify-tooling must run scripts/test-agent-contracts.py directly")

    def test_the_aggregate_verify_job_gates_every_dependency_on_success(self):
        block = workflow_job_block("verify")
        needs = job_needs(block)
        for dependency in AGGREGATE_DEPENDENCIES:
            self.assertIn(dependency, needs, f"the aggregate verify job must depend on {dependency}")
        self.assertRegex(
            block,
            r"(?m)^ {4}if: always\(\)$",
            "the aggregate must run its gates even when a dependency failed",
        )
        gated: set[str] = set()
        for step in workflow_job_steps("verify"):
            text = " ".join(step["env"].values()) + " " + step["run"]
            referenced = {dependency for dependency in AGGREGATE_DEPENDENCIES if f"needs.{dependency}.result" in text}
            if not referenced:
                continue
            self.assertIn(
                "success",
                step["run"],
                f"step {step['name']!r} reads a dependency result but does not refuse non-success results",
            )
            gated |= referenced
        self.assertEqual(
            gated,
            set(AGGREGATE_DEPENDENCIES),
            f"dependencies without a success gate: {sorted(set(AGGREGATE_DEPENDENCIES) - gated)}",
        )

    def test_the_real_gate_refuses_each_non_success_owner(self):
        steps = workflow_job_steps("verify")
        bindings = {
            key: dependency
            for step in steps
            for key, value in step["env"].items()
            for dependency in AGGREGATE_DEPENDENCIES
            if value == f"${{{{ needs.{dependency}.result }}}}"
        }
        self.assertEqual(set(bindings.values()), set(AGGREGATE_DEPENDENCIES))
        command = "\n".join(step["run"] for step in steps)
        environment = dict(os.environ, **{key: "success" for key in bindings})
        passed = subprocess.run(["bash", "-e", "-c", command], env=environment, capture_output=True)
        self.assertEqual(passed.returncode, 0, passed.stderr)
        for key, dependency in bindings.items():
            for result in ("failure", "cancelled", "skipped"):
                with self.subTest(owner=dependency, result=result):
                    failed = subprocess.run(
                        ["bash", "-e", "-c", command],
                        env=dict(environment, **{key: result}), capture_output=True,
                    )
                    self.assertNotEqual(failed.returncode, 0, "a non-success owner must fail verify")

    def test_each_direct_suite_has_exactly_one_owner(self):
        text = (ROOT / ".github/workflows/ci.yml").read_text()
        jobs = re.findall(r"^  ([a-z][a-z0-9-]*):$", text, re.MULTILINE)
        runs = [step["run"] for job in jobs for step in workflow_job_steps(job)]
        for command in ("bun test adapter/opencode", "scripts/test-agent-contracts.py", "scripts/check-json.py"):
            self.assertEqual(sum(run.count(command) for run in runs), 1, command)
        self.assertFalse(any("scripts/check-project-tooling.py" in run for run in runs),
                         "the JSON umbrella owns project tooling validation")


class _Completed:
    def __init__(self, returncode=0, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


class _FakeSubprocess:
    """Answers the checker's subprocess orchestration without spawning it.

    Each control below targets one failure branch. The checker's own
    subprocesses (the tamper suite, the generators, the Bun invocations) are
    routed through this fake so the control reaches its branch regardless of
    the host; every validator that runs before those subprocesses is real.
    """

    def __init__(self, results=()):
        self.results = list(results)  # (predicate over the argument words, result)
        self.calls: list[list[str]] = []

    def run(self, args, **kwargs):
        words = [str(arg) for arg in args]
        self.calls.append(words)
        for predicate, result in self.results:
            if predicate(words):
                return result
        return _Completed()


class StrictFailureControls(unittest.TestCase):
    """The umbrella is exactly as strict as its failure branches.

    CONCORD_REQUIRE_BUN=1 turns two contributor-laptop tolerances (Bun not
    installed; the pinned host declarations not installable) into required-
    check failures. Each control exercises the real main() up to its branch,
    faking only the subprocess layer, and each tolerant twin proves the
    environment variable is what fails the check rather than the condition
    alone.
    """

    def _main(self, *, fake, staging, require_bun, which="/fake/bun"):
        environment = {"CONCORD_REQUIRE_BUN": "1"} if require_bun else {}
        patches = [
            unittest.mock.patch.object(lane_checker, "subprocess", fake),
            unittest.mock.patch.object(lane_checker, "stage_host_workspace", staging),
            unittest.mock.patch("shutil.which", return_value=which),
        ]
        if require_bun:
            patches.append(unittest.mock.patch.dict(os.environ, environment))
        else:
            patches.append(unittest.mock.patch.dict(os.environ))
        out, err = io.StringIO(), io.StringIO()
        with ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            with redirect_stdout(out), redirect_stderr(err):
                if not require_bun:
                    os.environ.pop("CONCORD_REQUIRE_BUN", None)
                code = lane_checker.main([])
        return code, out.getvalue(), err.getvalue()

    def test_missing_bun_fails_closed_under_the_strict_environment(self):
        code, _, err = self._main(fake=_FakeSubprocess(), staging=lambda *a: None, require_bun=True, which=None)
        self.assertEqual(code, 1)
        self.assertIn("Bun is not installed", err)

    def test_missing_bun_stays_tolerable_on_a_contributor_laptop(self):
        code, out, _ = self._main(fake=_FakeSubprocess(), staging=lambda *a: None, require_bun=False, which=None)
        self.assertEqual(code, 0)
        self.assertIn("non-Bun checks only", out)
        self.assertIn("adapter build", out)
        self.assertIn("host typecheck", out)
        self.assertIn("NOT run", out)

    def test_missing_bun_still_validates_the_host_pin(self):
        finding = "adapter host pin unreadable: simulated"

        def broken(findings):
            findings.append(finding)
            return None

        with unittest.mock.patch.object(lane_checker, "load_host_pin", broken):
            code, _, err = self._main(fake=_FakeSubprocess(), staging=lambda *a: None, require_bun=False, which=None)
        self.assertEqual(code, 1)
        self.assertIn(finding, err)

    def test_the_no_bun_fallback_runs_the_real_generator_check(self):
        # PATH must hide Bun from the actual generator subprocess, not only
        # from the parent checker's patched discovery function.
        out, err = io.StringIO(), io.StringIO()
        with tempfile.TemporaryDirectory() as tools, \
                unittest.mock.patch.dict(os.environ):
            os.environ.pop("CONCORD_REQUIRE_BUN", None)
            for name in ("go", "gofmt", "git"):
                executable = _REAL_WHICH(name)
                self.assertIsNotNone(executable, f"{name} is required for the non-Bun generator checks")
                (Path(tools) / name).symlink_to(executable)
            os.environ["PATH"] = tools
            with redirect_stdout(out), redirect_stderr(err):
                code = lane_checker.main(["--contract-tests-external"])
        self.assertEqual(code, 0, err.getvalue())
        self.assertIn("non-Bun checks only", out.getvalue())

    def test_an_unreachable_registry_fails_closed_under_the_strict_environment(self):
        error = "host declarations could not be installed: simulated registry unreachable"
        code, _, err = self._main(fake=_FakeSubprocess(), staging=lambda *a: error, require_bun=True)
        self.assertEqual(code, 1)
        self.assertIn(error, err)

    def test_an_unreachable_registry_stays_tolerable_on_a_contributor_laptop(self):
        code, out, _ = self._main(
            fake=_FakeSubprocess(),
            staging=lambda *a: "host declarations could not be installed: simulated",
            require_bun=False,
        )
        self.assertEqual(code, 0)
        self.assertIn("host typecheck NOT run", out)

    def test_a_host_typecheck_failure_fails_the_check(self):
        # TS9999 is not a code the TypeScript compiler emits, so no recorded
        # allowance can absorb the simulated diagnostic; reconcile_diagnostics
        # must report it and main() must propagate the failure.
        typecheck = _Completed(1, "src/concord.ts(1,1): error TS9999: simulated host type failure\n", "")
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "x", typecheck)])
        code, _, err = self._main(fake=fake, staging=lambda *a: None, require_bun=True)
        self.assertEqual(code, 1)
        self.assertIn("TS9999", err)
        self.assertIn("does not satisfy the pinned host declarations", err)

    def test_a_failed_registry_install_reports_a_staging_error(self):
        # The staging mechanism itself, not main()'s handling of it: a Bun
        # binary that cannot reach the registry must surface as the staging
        # error string the strict branch above prints.
        with tempfile.TemporaryDirectory() as directory:
            fake_bun = Path(directory) / "bun"
            fake_bun.write_text("#!/bin/sh\necho 'npm ERR! network unreachable' >&2\nexit 1\n", encoding="utf-8")
            os.chmod(fake_bun, 0o755)
            pin = json.loads((ROOT / ".concord/docs/adapter-host-pin.v1.json").read_text())
            error = lane_checker.stage_host_workspace(str(fake_bun), pin, Path(directory) / "workspace")
        self.assertIsNotNone(error)
        self.assertTrue(error.startswith("host declarations could not be installed"), error)
        self.assertIn("network unreachable", error)

    def test_host_sources_refresh_without_reinstalling_dependencies(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repository"
            origin = root / "adapter"
            origin.mkdir(parents=True)
            (origin / "current.ts").write_text("export const value = 1;\n")
            (origin / "removed.ts").write_text("export const old = true;\n")
            workspace = Path(directory) / "workspace"
            dependencies = workspace / "node_modules"
            dependencies.mkdir(parents=True)
            marker = dependencies / "installed"
            marker.write_text("pinned dependencies\n")
            package = workspace / "package.json"
            package.write_text('{"private":true}\n')
            lockfile = workspace / "bun.lock"
            lockfile.write_text("pinned lockfile\n")
            pin = {"sources": ["adapter"], "compiler_options": {"strict": True, "noEmit": True}}
            with unittest.mock.patch.object(lane_checker, "ROOT", root), unittest.mock.patch.object(lane_checker.subprocess, "run") as run:
                self.assertIsNone(lane_checker.stage_host_sources(pin, workspace))
                (origin / "current.ts").write_text("export const value = 2;\n")
                (origin / "removed.ts").unlink()
                self.assertIsNone(lane_checker.stage_host_sources(pin, workspace))
                run.assert_not_called()
            self.assertEqual((workspace / "src/current.ts").read_text(), "export const value = 2;\n")
            self.assertFalse((workspace / "src/removed.ts").exists())
            self.assertEqual(marker.read_text(), "pinned dependencies\n")
            self.assertEqual(package.read_text(), '{"private":true}\n')
            self.assertEqual(lockfile.read_text(), "pinned lockfile\n")
            self.assertEqual(json.loads((workspace / "tsconfig.json").read_text()), {"compilerOptions": pin["compiler_options"], "include": ["src/*.ts"]})

    def test_a_plain_js_source_module_stages_beside_the_typescript(self):
        # The adapter ships plain JavaScript source modules that the
        # TypeScript imports directly (worker-report-protocol.js); a staging
        # glob that drops them breaks the import graph the pinned typecheck
        # walks. The .js enters through allowJs when a .ts root imports it,
        # so the include list stays TypeScript-only.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repository"
            origin = root / "adapter"
            origin.mkdir(parents=True)
            (origin / "entry.ts").write_text('import { a } from "./plain.js"\n', encoding="utf-8")
            (origin / "plain.js").write_text("export const a = 1\n", encoding="utf-8")
            (origin / "notes.json").write_text("{}\n", encoding="utf-8")
            workspace = Path(directory) / "workspace"
            pin = {"sources": ["adapter"], "compiler_options": {"strict": True, "noEmit": True, "allowJs": True}}
            with unittest.mock.patch.object(lane_checker, "ROOT", root):
                self.assertIsNone(lane_checker.stage_host_sources(pin, workspace))
            self.assertTrue((workspace / "src/entry.ts").is_file())
            self.assertTrue((workspace / "src/plain.js").is_file())
            self.assertTrue((workspace / "src/notes.json").is_file())
            self.assertEqual(json.loads((workspace / "tsconfig.json").read_text()), {"compilerOptions": pin["compiler_options"], "include": ["src/*.ts"]})

    def test_a_broken_contract_fixture_fails_the_check(self):
        # A digest that no longer matches its definition makes a valid-marked
        # fixture invalid; the corpus validators are real, so main() must
        # fail before any subprocess orchestration is reached.
        real_load = lane_checker._load_json

        def broken_fixture(path):
            value = real_load(path)
            if getattr(path, "name", str(path)) == "workflow-engine.fixtures.json":
                for case in value["cases"]:
                    if case["id"] == "definition-implementation-valid":
                        case["instance"]["digest"] = "sha256:" + "0" * 64
            return value

        out, err = io.StringIO(), io.StringIO()
        with unittest.mock.patch.object(lane_checker, "_load_json", broken_fixture):
            with redirect_stdout(out), redirect_stderr(err):
                code = lane_checker.main([])
        self.assertEqual(code, 1)
        self.assertIn("expected valid instance", err.getvalue())


class ExternalSuiteRoutingOptionsTests(unittest.TestCase):
    """CON-893: the external-suite options omit exactly one subprocess each.

    CI splits the old verify job so each suite runs as its own step, and the
    nested complete check must not duplicate them. Each option removes only
    its suite subprocess: every other obligation — the selftest, the
    generated checks, the lane validators, the Bun builds, the fixture probe,
    the expected-suite presence check, the host typecheck, and both strict
    failures — still runs and still propagates failure. A default invocation
    runs the complete check with both suites. --skip-host-typecheck omits
    host staging, installation, probes, and compilation, not other checks.
    """

    MINIMAL_PIN = {
        "sources": ["adapter/opencode"],
        "packages": [],
        "typescript": "5.9.3",
        "compiler_options": {},
        "allowances": [],
        "runtime_probe": {},
    }

    def _main(self, argv, fake=None, which="/fake/bun", staging=lambda *args: None, require_bun=False, pin_loader=None):
        fake = fake if fake is not None else _FakeSubprocess()
        patches = [
            unittest.mock.patch.object(lane_checker, "subprocess", fake),
            unittest.mock.patch.object(lane_checker, "stage_host_workspace", staging),
            unittest.mock.patch.object(
                lane_checker, "load_host_pin",
                pin_loader if pin_loader is not None else lambda findings: copy.deepcopy(self.MINIMAL_PIN),
            ),
            unittest.mock.patch("shutil.which", return_value=which),
        ]
        patches.append(unittest.mock.patch.dict(os.environ, {"CONCORD_REQUIRE_BUN": "1"} if require_bun else {}))
        out, err = io.StringIO(), io.StringIO()
        with ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            if not require_bun:
                os.environ.pop("CONCORD_REQUIRE_BUN", None)
            with redirect_stdout(out), redirect_stderr(err):
                code = lane_checker.main(list(argv))
        return code, fake.calls, out.getvalue(), err.getvalue()

    @staticmethod
    def _selftest_calls(calls):
        return [call for call in calls if any("test-agent-contracts.py" in word for word in call)]

    @staticmethod
    def _adapter_suite_calls(calls):
        return [call for call in calls if call[1:3] == ["test", "adapter/opencode"]]

    def test_a_default_invocation_runs_both_suites(self):
        code, calls, out, _ = self._main([])
        self.assertEqual(code, 0)
        self.assertTrue(self._selftest_calls(calls), "a default invocation must run the contract selftest")
        self.assertTrue(self._adapter_suite_calls(calls), "a default invocation must run the adapter suite")
        self.assertIn("agent contract check passed", out)

    def test_adapter_tests_external_omits_only_the_adapter_suite(self):
        code, calls, out, _ = self._main(["--adapter-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._adapter_suite_calls(calls), "the adapter suite subprocess must be omitted")
        self.assertTrue(self._selftest_calls(calls), "the contract selftest must still run")
        self.assertTrue(any(call[1] == "build" for call in calls), "the Bun builds must still run")

    def test_contract_tests_external_omits_only_the_selftest(self):
        code, calls, _, _ = self._main(["--contract-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._selftest_calls(calls), "the contract selftest subprocess must be omitted")
        self.assertTrue(self._adapter_suite_calls(calls), "the adapter suite must still run")

    def test_both_options_retain_every_other_subprocess(self):
        code, calls, out, _ = self._main(["--adapter-tests-external", "--contract-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._selftest_calls(calls))
        self.assertFalse(self._adapter_suite_calls(calls))
        self.assertEqual(len([call for call in calls if call[1] == "build"]), 2, "both Bun builds must still run")
        self.assertTrue(any(call[1] == "run" for call in calls), "the fixture probe and host schema probe must still run")
        self.assertTrue(any(call[1] == "x" for call in calls), "the host typecheck must still run")
        self.assertTrue(any(any("generate-agent-contracts.py" in word for word in call) for call in calls))
        self.assertTrue(any(any("generate-agent-lanes.py" in word for word in call) for call in calls))

    def test_the_expected_suite_presence_check_survives_adapter_tests_external(self):
        # The option skips the suite subprocess, never the obligation that
        # the suite files exist: a job running the suite externally still has
        # to carry it.
        real_glob = Path.glob

        def hiding_glob(self, pattern, **kwargs):
            return iter(()) if pattern == "*.test.ts" else real_glob(self, pattern, **kwargs)

        with unittest.mock.patch.object(Path, "glob", hiding_glob):
            code, _, _, err = self._main(["--adapter-tests-external"])
        self.assertEqual(code, 1)
        self.assertIn("adapter test suite missing", err)

    def test_a_selftest_failure_propagates_under_adapter_tests_external(self):
        fake = _FakeSubprocess(results=[(
            lambda words: any("test-agent-contracts.py" in word for word in words),
            _Completed(1, "", "simulated selftest failure"),
        )])
        code, _, _, _ = self._main(["--adapter-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_an_adapter_suite_failure_propagates_under_contract_tests_external(self):
        fake = _FakeSubprocess(results=[(
            lambda words: words[1:3] == ["test", "adapter/opencode"],
            _Completed(1),
        )])
        code, _, _, _ = self._main(["--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_a_generated_contract_check_failure_propagates_under_both_options(self):
        fake = _FakeSubprocess(results=[(
            lambda words: any("generate-agent-contracts.py" in word for word in words),
            _Completed(1),
        )])
        code, _, _, _ = self._main(["--adapter-tests-external", "--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_a_bun_build_failure_propagates_under_both_options(self):
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "build", _Completed(1))])
        code, _, _, _ = self._main(["--adapter-tests-external", "--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)

    def test_a_host_typecheck_failure_propagates_under_both_options(self):
        # TS9999 is not a code the TypeScript compiler emits, so no recorded
        # allowance can absorb the simulated diagnostic.
        typecheck = _Completed(1, "src/concord.ts(1,1): error TS9999: simulated host type failure\n", "")
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "x", typecheck)])
        code, _, _, err = self._main(["--adapter-tests-external", "--contract-tests-external"], fake=fake)
        self.assertEqual(code, 1)
        self.assertIn("TS9999", err)
        self.assertIn("does not satisfy the pinned host declarations", err)

    def test_missing_bun_fails_closed_under_both_options(self):
        code, _, _, err = self._main(["--adapter-tests-external", "--contract-tests-external"], which=None, require_bun=True)
        self.assertEqual(code, 1)
        self.assertIn("Bun is not installed", err)

    def test_an_unreachable_registry_fails_closed_under_both_options(self):
        error = "host declarations could not be installed: simulated registry unreachable"
        code, _, _, err = self._main(
            ["--adapter-tests-external", "--contract-tests-external"],
            staging=lambda *args: error,
            require_bun=True,
        )
        self.assertEqual(code, 1)
        self.assertIn(error, err)

    def test_skip_host_typecheck_runs_no_staging_or_host_subprocess(self):
        staging_calls = []
        code, calls, _, _ = self._main(["--skip-host-typecheck"], staging=lambda *args: staging_calls.append(args))
        self.assertEqual(code, 0)
        self.assertEqual(staging_calls, [], "the host workspace must never be staged")
        self.assertEqual(
            [call for call in calls if len(call) > 1 and call[1] == "x"], [],
            "the host TypeScript compiler must not run",
        )
        self.assertFalse(
            any("host_schema_probe" in word for call in calls for word in call),
            "the host schema probe must not run",
        )
        self.assertFalse(
            any("--host-merge" in word for call in calls for word in call),
            "the host permission-merge probe must not run",
        )
        self.assertTrue(any(len(call) > 1 and call[1] == "build" for call in calls), "the Bun builds must still run")
        self.assertTrue(any(len(call) > 1 and call[1] == "run" for call in calls), "the fixture probe must still run")

    def test_skip_host_typecheck_names_the_omitted_host_checks(self):
        code, _, out, _ = self._main(["--skip-host-typecheck"])
        self.assertEqual(code, 0)
        self.assertIn("host typecheck NOT run", out)

    def test_skip_host_typecheck_retains_external_suite_routing(self):
        code, calls, _, _ = self._main(
            ["--skip-host-typecheck", "--adapter-tests-external", "--contract-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(self._selftest_calls(calls))
        self.assertFalse(self._adapter_suite_calls(calls))
        self.assertEqual(
            len([call for call in calls if len(call) > 1 and call[1] == "build"]), 2,
            "both Bun builds must still run",
        )

    def test_skip_host_typecheck_fails_closed_under_the_strict_environment(self):
        # The refusal must come before the workload and every subprocess.
        def unreachable():
            raise AssertionError("the strict refusal must precede the contract workload")

        staging_calls = []
        with unittest.mock.patch.object(lane_checker, "check_workflow_contracts", unreachable):
            code, calls, _, err = self._main(
                ["--skip-host-typecheck"], require_bun=True, staging=lambda *args: staging_calls.append(args))
        self.assertEqual(code, 1)
        self.assertEqual(calls, [], "the strict refusal must precede every subprocess")
        self.assertEqual(staging_calls, [])
        self.assertTrue(err.strip())

    def test_skip_host_typecheck_still_validates_the_host_pin(self):
        finding = "adapter host pin unreadable: simulated"

        def broken(findings):
            findings.append(finding)
            return None

        code, _, _, err = self._main(["--skip-host-typecheck"], pin_loader=broken)
        self.assertEqual(code, 1)
        self.assertIn(finding, err)

    def test_a_bun_build_failure_propagates_under_skip_host_typecheck(self):
        fake = _FakeSubprocess(results=[(lambda words: len(words) > 1 and words[1] == "build", _Completed(1))])
        code, _, _, _ = self._main(["--skip-host-typecheck"], fake=fake)
        self.assertEqual(code, 1)


class WorkflowActionVariantCorpusTests(unittest.TestCase):
    """Registry-derived deterministic corpus over every published workflow
    action variant (CON-412).

    The corpus derives its cases from the live registry projection (the same
    `go run ./scripts/workflow-action-contracts` output the generator reads),
    not from the frozen JSON, so an action the registry adds without a
    generated closed variant fails here. For each action it proves:

    - a valid sample built from the variant's own required/properties admits
      against the variant schema (published shape), the core input union, and
      the public variant when one exists;
    - missing-required, unknown-field, cross-variant-field, bad-enum,
      bad-type, and bad-range mutants are refused by the variant schema AND
      by the core input union AND by the public variant, so the published
      closed branch and the core cannot admit differently;
    - the legal input combinations the store's guards enforce (the registry
      projection's combinations, authored beside the guards) are exact: each
      discriminator value's required fields are required and its forbidden
      fields are refused, in every published and core shape.
    """

    @classmethod
    def setUpClass(cls):
        cls.actions, cls.workflows, cls.teaching, cls.schema_refs = generator.load_workflow_action_contracts()
        cls.defs = payload_schema["$defs"]

    def _variants(self):
        for action in self.actions:
            name = f"work_transition_action_variant_{action['id']}"
            self.assertIn(name, self.defs, f"registry action {action['id']} has no closed variant")
            yield action, self.defs[name]

    def _schema_names(self, action, shape="core"):
        if shape == "core":
            names = [f"work_transition_action_variant_{action['id']}", "work_transition_action_input"]
        else:
            names = [f"work_transition_action_public_variant_{action['id']}", "work_transition_action_public_input"]
        return names

    def _shapes(self, action):
        # The core payload answers to the core union; a divergent public
        # payload (dispatch_worker, whose core attempt identity the adapter
        # authors) answers to the public union the agent boundary validates.
        shapes = [("core", f"work_transition_action_variant_{action['id']}", self._schema_names(action, "core"))]
        public = f"work_transition_action_public_variant_{action['id']}"
        if public in self.defs:
            shapes.append(("public", public, self._schema_names(action, "public")))
        return shapes

    def _assert_verdict(self, value, schema_name, should_admit, label):
        try:
            generator.schema_validate(value, self.defs[schema_name], payload_schema, schema_name)
        except ValueError as err:
            if should_admit:
                self.fail(f"{label}: {schema_name} refused a payload its contract admits ({err})")
            return
        if not should_admit:
            self.fail(f"{label}: {schema_name} admitted a payload its contract refuses")

    # Node-derived mutant walks. Each returns (label, value, path) mutants
    # for one schema node against the sample value that reaches it.
    def _enum_mutants(self, schema, value, path=()):
        if isinstance(schema, dict) and "$ref" in schema:
            schema = self.defs.get(schema["$ref"].removeprefix("#/$defs/"), schema)
        if not isinstance(schema, dict):
            return
        if "enum" in schema and path and isinstance(value, (str, int)) and not isinstance(value, bool):
            yield "; ".join(path) + " bad enum", "__corpus_not_an_enum_value__", path
        kind = schema.get("type")
        kind = next((candidate for candidate in (kind if isinstance(kind, list) else [kind]) if candidate != "null"), None) if kind else None
        if isinstance(value, dict):
            for name, child in (schema.get("properties") or {}).items():
                if name not in value:
                    continue
                yield from self._enum_mutants(child, value[name], path + (name,))
        elif isinstance(value, list) and value:
            yield from self._enum_mutants(schema.get("items", {}), value[0], path + ("0",))

    def _type_mutants(self, schema, value, path=()):
        if isinstance(schema, dict) and "$ref" in schema:
            schema = self.defs.get(schema["$ref"].removeprefix("#/$defs/"), schema)
        if not isinstance(schema, dict):
            return
        kind = schema.get("type")
        if kind and path:
            primary = next((candidate for candidate in (kind if isinstance(kind, list) else [kind]) if candidate != "null"), None)
            if primary == "string":
                yield "; ".join(path) + " bad type", 17, path
            elif primary is not None:
                yield "; ".join(path) + " bad type", "corpus-wrong-type", path
        if isinstance(value, dict):
            for name, child in (schema.get("properties") or {}).items():
                if name not in value:
                    continue
                yield from self._type_mutants(child, value[name], path + (name,))
        elif isinstance(value, list) and value:
            yield from self._type_mutants(schema.get("items", {}), value[0], path + ("0",))

    def _range_mutants(self, schema, value, path=()):
        if isinstance(schema, dict) and "$ref" in schema:
            schema = self.defs.get(schema["$ref"].removeprefix("#/$defs/"), schema)
        if not isinstance(schema, dict):
            return
        if isinstance(value, str):
            if schema.get("minLength", 0) > 0 and len(value) >= schema["minLength"]:
                yield "; ".join(path) + " under minLength", "v" * (schema["minLength"] - 1), path
            if "maxLength" in schema and len(value) <= schema["maxLength"]:
                yield "; ".join(path) + " over maxLength", "v" * (schema["maxLength"] + 1), path
            return
        if isinstance(value, list):
            items = schema.get("items", {})
            if schema.get("minItems", 0) > 0 and len(value) >= schema["minItems"]:
                under = [self._sample(items)] * (schema["minItems"] - 1)
                yield "; ".join(path) + " under minItems", under, path
            if "maxItems" in schema and len(value) <= schema["maxItems"]:
                over = [self._sample(items)] * (schema["maxItems"] + 1)
                yield "; ".join(path) + " over maxItems", over, path
            if value:
                yield from self._range_mutants(items, value[0], path + ("0",))
            return
        if isinstance(value, dict):
            for name, child in (schema.get("properties") or {}).items():
                if name not in value:
                    continue
                yield from self._range_mutants(child, value[name], path + (name,))
            return
        if isinstance(value, int) and not isinstance(value, bool):
            if "minimum" in schema and value >= schema["minimum"]:
                yield "; ".join(path) + " under minimum", schema["minimum"] - 1, path
            if "maximum" in schema and value <= schema["maximum"]:
                yield "; ".join(path) + " over maximum", schema["maximum"] + 1, path

    def test_valid_samples_admit_and_mutants_refuse(self):
        own_fields = {action["id"]: {field["name"] for variant in action["variants"] for field in variant["payload"]["fields"]} for action in self.actions}
        for action, variant in self._variants():
            for kind, shape_name, names in self._shapes(action):
                valid = self._sample(self.defs[shape_name])
                for name in names:
                    self._assert_verdict(valid, name, True, f"{action['id']} {kind} valid sample")
                required = [key for key in self.defs[shape_name].get("required", []) if key != "action_id"]
                if required:
                    mutant = copy.deepcopy(valid); del mutant[required[0]]
                    for name in names:
                        self._assert_verdict(mutant, name, False, f"{action['id']} {kind} missing required {required[0]}")
                mutant = copy.deepcopy(valid); mutant["unknown_field"] = True
                for name in names:
                    self._assert_verdict(mutant, name, False, f"{action['id']} {kind} unknown field")
                fields = valid.get("fields")
                if isinstance(fields, dict) and fields:
                    mutant = copy.deepcopy(valid); del mutant["fields"][next(iter(fields))]
                    cross = next((name for other in self.actions if other["id"] != action["id"]
                                  for name in own_fields[other["id"]]
                                  if name not in own_fields[action["id"]]), None)
                    if cross is not None:
                        mutant["fields"][cross] = "cross-variant"
                    for name in names:
                        self._assert_verdict(mutant, name, False, f"{action['id']} {kind} cross-variant or unknown fields field")
                # Node-derived enum, type, and range mutants: every one must
                # be refused by the published shape and the core union alike.
                for walker in (self._enum_mutants, self._type_mutants, self._range_mutants):
                    for label, replacement, path in walker(self.defs[shape_name], valid):
                        path = tuple(int(step) if isinstance(step, str) and step.isdigit() else step for step in path)
                        mutant = copy.deepcopy(valid)
                        node = mutant
                        for step in path[:-1]:
                            node = node[int(step)] if isinstance(node, list) else node[step]
                        if path:
                            final = path[-1]
                            if isinstance(node, list): node[int(final)] = replacement
                            else: node[final] = replacement
                        for name in names:
                            self._assert_verdict(mutant, name, False, f"{action['id']} {kind} {label}")

    def test_cross_field_rule_mutants_refuse_everywhere(self):
        # The registry payload declarations state the cross-field rules core
        # admission enforces: legal input combinations (one branch per enum
        # value) and alternative field groups (exactly one supplied). The
        # published variant must refuse every rule's own invalid mutant
        # exactly — a forbidden field under one discriminator value, a
        # missing required field under another, a withheld alternative group,
        # and two groups supplied together — and admit each rule's legal
        # payload. A rule the canonical payload narrows to one group with no
        # kind match collapses into the flat closed shape, whose own
        # required set already refuses every mutant the rule refused.
        for action, variant in self._variants():
            cross = action.get("cross_field") or {}
            combinations = cross.get("combinations", [])
            alternatives = cross.get("alternatives", [])
            kind_matches = cross.get("kind_matches", [])
            if not combinations and not alternatives:
                continue
            fields_schema = variant["properties"]["fields"]
            if len(combinations) + len(alternatives) == 1 and not kind_matches:
                group = (combinations + alternatives)[0]
                # Different workflow fieldsets can still form an outer union;
                # the single rule group must collapse within each fieldset.
                for condition in _condition_branches(variant):
                    flat = _fields_object(condition)
                    self.assertNotIn("oneOf", flat, f"{action['id']} publishes a nested union for a single rule group")
                    self.assertIs(flat.get("additionalProperties"), False)
                    for name in group["fields"]:
                        self.assertIn(name, flat.get("required", []), f"{action['id']} single rule group {group['fields']} loses its required field")
                    for name in group.get("forbids", []):
                        self.assertNotIn(name, flat.get("properties", {}), f"{action['id']} single rule group publishes forbidden {name}")
                continue
            self.assertIn("oneOf", fields_schema, f"{action['id']} declares cross-field rules but its variant publishes none")

            def branch_property(name):
                # A combination field lives in the branch that declares it;
                # a forbidden-under-this-value field lives in a sibling branch.
                for branch in fields_schema["oneOf"]:
                    property = branch.get("properties", {}).get(name)
                    if property is not None:
                        return property
                return {"type": "string"}

            def envelope_with(fields):
                base = {key: self._sample(variant["properties"][key]) for key in variant.get("required", []) if key != "fields"}
                base["fields"] = fields
                return base

            by_value = {combination["value"]: combination for combination in combinations}
            # A kind-match declaration expands one alternative group into
            # per-kind branches (outcome_kind consts on supersede_contract):
            # each is registry-declared through the match, not through a
            # combination, so it carries no requires/forbids to expand.
            kind_match_fields = {match["field"] for match in cross.get("kind_matches", [])}
            for branch in fields_schema["oneOf"]:
                discriminator = next((name for name, property in branch.get("properties", {}).items() if "const" in property), None)
                if discriminator is None:
                    continue
                value = branch["properties"][discriminator]["const"]
                if discriminator in kind_match_fields:
                    continue
                combination = by_value.get(value)
                self.assertIsNotNone(combination, f"{action['id']} publishes a branch for {discriminator}={value} the registry does not declare")
                base = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                base["fields"][discriminator] = value
                for _kind, _shape_name, names in self._shapes(action):
                    for forbidden in combination.get("forbids", []):
                        if forbidden == discriminator:
                            continue
                        mutant = copy.deepcopy(base)
                        mutant["fields"][forbidden] = self._sample(branch_property(forbidden))
                        for name in names:
                            self._assert_verdict(mutant, name, False, f"{action['id']} {discriminator}={value} carries forbidden {forbidden}")
                        # Stripping the forbidden field leaves a legal payload.
                        legal = copy.deepcopy(mutant); legal["fields"].pop(forbidden)
                        for name in names:
                            self._assert_verdict(legal, name, True, f"{action['id']} {discriminator}={value} without {forbidden}")
                    for required_under_value in combination.get("requires", []):
                        if required_under_value == discriminator:
                            continue
                        mutant = copy.deepcopy(base)
                        mutant["fields"].pop(required_under_value, None)
                        for name in names:
                            self._assert_verdict(mutant, name, False, f"{action['id']} {discriminator}={value} drops required {required_under_value}")
                        legal = copy.deepcopy(mutant)
                        legal["fields"][required_under_value] = self._sample(branch_property(required_under_value))
                        for name in names:
                            self._assert_verdict(legal, name, True, f"{action['id']} {discriminator}={value} with {required_under_value}")

            # Alternative groups: each declared group names the fields its
            # closed branch requires; a branch answers to the group whose
            # names it carries. A kind-matched group expands into one closed
            # branch per declared kind, so only the plain groups map 1:1 to
            # const-free branches (CON-412).
            group_branches = [branch for branch in fields_schema["oneOf"] if not any("const" in property for property in branch.get("properties", {}).values())]

            def branch_for(group):
                for branch in fields_schema["oneOf"]:
                    required = set(branch.get("required", []))
                    if all(name in required for name in group["fields"]):
                        return branch
                self.fail(f"{action['id']} declares alternative group {group['fields']} with no published branch")

            if alternatives:
                plain_groups = [group for group in alternatives if not (kind_match_fields & set(group["fields"]))]
                kind_groups = [group for group in alternatives if kind_match_fields & set(group["fields"])]
                self.assertEqual(len(group_branches), len(plain_groups), f"{action['id']} alternative branches and declaration groups disagree")
                for group in kind_groups:
                    match = next(match for match in cross.get("kind_matches", []) if match["field"] in group["fields"])
                    branches = [branch for branch in fields_schema["oneOf"] if match["field"] in branch.get("properties", {}) and "const" in branch["properties"][match["field"]]]
                    self.assertGreater(len(branches), 0, f"{action['id']} kind-matched group {group['fields']} publishes no per-kind branch")
                    for branch in branches:
                        self.assertEqual(
                            branch["properties"][match["field"]]["const"],
                            branch["properties"][match["object"]]["properties"][match["discriminator"]]["const"],
                            f"{action['id']} per-kind branch does not bind {match['field']} to {match['object']}.{match['discriminator']}",
                        )
                for group in alternatives:
                    branch = branch_for(group)
                    for _kind, _shape_name, names in self._shapes(action):
                        legal = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                        for name in names:
                            self._assert_verdict(copy.deepcopy(legal), name, True, f"{action['id']} alternative group {group['fields']} supplied")
                        withheld = {key: value for key, value in legal["fields"].items()}
                        for name in group["fields"]:
                            withheld.pop(name, None)
                        for name in names:
                            self._assert_verdict(envelope_with(withheld), name, False, f"{action['id']} alternative group {group['fields']} withheld")
                    # A field the group forbids beside it is omitted from the
                    # branch entirely and refused when carried — one negative
                    # mutant for each newly owned cross-field rule.
                    for forbidden in group.get("forbids", []):
                        self.assertNotIn(forbidden, branch.get("properties", {}), f"{action['id']} branch for {group['fields']} publishes forbidden {forbidden}")
                        mutant = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                        mutant["fields"][forbidden] = self._sample(branch_property(forbidden))
                        for _kind, _shape_name, names in self._shapes(action):
                            for name in names:
                                self._assert_verdict(copy.deepcopy(mutant), name, False, f"{action['id']} group {group['fields']} carries forbidden {forbidden}")
                    if len(alternatives) > 1:
                        for other_group in alternatives:
                            if other_group is group:
                                continue
                            other = branch_for(other_group)
                            for exclusive in other_group["fields"]:
                                if exclusive in branch.get("properties", {}):
                                    continue
                                mutant = envelope_with(self._sample(branch, fallback_properties=branch.get("properties")))
                                mutant["fields"][exclusive] = self._sample(other["properties"][exclusive])
                                for _kind, _shape_name, names in self._shapes(action):
                                    for name in names:
                                        self._assert_verdict(copy.deepcopy(mutant), name, False, f"{action['id']} alternative groups carry {exclusive} together")

    def test_each_exact_fieldset_is_authorable_in_both_input_unions(self):
        for action, variant in self._variants():
            for _kind, shape_name, names in self._shapes(action):
                envelope = self.defs[shape_name]
                fields = envelope["properties"].get("fields")
                if fields is None:
                    continue
                for condition in _condition_branches(envelope):
                    branch = _fields_object(condition)
                    value = self._sample(envelope)
                    value["fields"] = self._sample(branch)
                    for name in names:
                        self._assert_verdict(value, name, True, f"{action['id']} exact fieldset")
                    public_name = f"work_transition_action_public_variant_{action['id']}"
                    if public_name not in self.defs:
                        self._assert_verdict(value, "work_transition_action_public_input", True, f"{action['id']} shared public fieldset")

    def test_guard_owned_teaching_is_published(self):
        # CON-412: the items close per kind; every branch teaches the ordinal
        # position rule and the outcome_kind/outcome_payload.kind equality
        # rule from the guard-owned projection.
        items = self.defs["workflow_action_outcome_predicates"]["items"]["oneOf"]
        self.assertEqual(len(items), 4)
        for branch in items:
            self.assertEqual(branch["properties"]["ordinal"]["description"], self.teaching["predicate_ordinal_rule"])
            self.assertEqual(branch["properties"]["outcome_kind"]["description"], self.teaching["outcome_kind_equality_rule"])
            self.assertEqual(branch["properties"]["outcome_kind"]["const"], branch["properties"]["outcome_payload"]["properties"]["kind"]["const"])
        for condition in _condition_branches(self.defs["work_transition_action_variant_approve_contract"]):
            approve = _fields_object(condition)["properties"]
            if "required_evidence" in approve:
                field = approve["required_evidence"]
                original, marker, guidance = field["description"].partition(" Accepted shape:")
                self.assertEqual(original, self.teaching["obligation_membership_rule"])
                self.assertEqual(marker, " Accepted shape:")
                self.assertIn("example:", guidance)
                self.assertEqual(len(field["examples"]), 1)
                generator.schema_validate(field["examples"][0], field, {"$defs": self.defs})

    def test_every_registry_action_has_one_closed_variant(self):
        core_refs = [{"$ref": f"#/$defs/work_transition_action_variant_{action['id']}"} for action in self.actions]
        public_refs = [{"$ref": f"#/$defs/work_transition_action_{'public_variant' if any(variant['payload'] != variant['public_payload'] for variant in action['variants']) else 'variant'}_{action['id']}"} for action in self.actions]
        self.assertCountEqual(self.defs["work_transition_action_input"]["anyOf"], core_refs)
        self.assertNotIn("work_transition_action_legacy_input", self.defs)
        self.assertCountEqual(self.defs["work_transition_action_public_input"]["anyOf"], public_refs)

    def _sample(self, schema, path="$", fallback_properties=None):
        if "$ref" in schema:
            schema = self.defs[schema["$ref"].removeprefix("#/$defs/")] | {key: value for key, value in schema.items() if key != "$ref"}
        if "const" in schema: return schema["const"]
        if "enum" in schema: return schema["enum"][0]
        if "anyOf" in schema:
            return self._sample(schema["anyOf"][0], path, fallback_properties)
        if "oneOf" in schema:
            base = {key: self._sample(self._property(schema, fallback_properties, key), f"{path}.{key}") for key in schema.get("required", [])}
            branch_value = self._sample(schema["oneOf"][0], f"{path}.oneOf[0]", schema.get("properties"))
            if isinstance(branch_value, dict):
                for key, value in branch_value.items():
                    base.setdefault(key, value)
            return base
        if "allOf" in schema:
            base = self._sample({k: v for k, v in schema.items() if k != "allOf"}, path, fallback_properties)
            for member in schema["allOf"]:
                if "if" in member:
                    keyword = "then" if generator._valid(base, member["if"], payload_schema, path) else "else"
                    member = member.get(keyword)
                    if member is None:
                        continue
                    for key, constraint in member.get("properties", {}).items():
                        if key in base:
                            declared = self._property(schema, fallback_properties, key)
                            base[key] = self._sample(declared | constraint, f"{path}.{key}")
                merged = self._sample(member, path, fallback_properties)
                if isinstance(base, dict) and isinstance(merged, dict): base.update(merged)
            return base
        kind = schema.get("type")
        if isinstance(kind, list): kind = next((candidate for candidate in kind if candidate != "null"), kind[0])
        if kind == "null": return None
        if kind is None and ("properties" in schema or "required" in schema): kind = "object"
        if kind == "object":
            result = {}
            for key in schema.get("required", []):
                result[key] = self._sample(self._property(schema, fallback_properties, key), f"{path}.{key}")
            return result
        if kind == "array":
            items = schema.get("items", {"type": "string"})
            values = [self._sample(items, path) for _ in range(schema.get("minItems", 0))]
            if items.get("$ref") == "#/$defs/workflow_member_job_binding":
                for index, member in enumerate(values):
                    member["job_id"] = f"job-{index + 1}"
            return values
        if kind == "integer": return schema.get("minimum", 0)
        if kind == "boolean": return True
        if schema.get("format") == "date-time": return "2026-08-08T00:00:00Z"
        return _schema_sample(schema, payload_schema, path)

    def _property(self, schema, fallback_properties, key):
        properties = schema.get("properties") or fallback_properties or {}
        return properties.get(key, {"type": "string"})


class ResearchContextProjectionTests(unittest.TestCase):
    """The selected-research projection is canonical, not copied.

    The lane packet contract owns the research member and its closed defs;
    the payloads document projects them under the shared names through
    project_native_oracle_metadata, the current public view carries the same
    projected member, and the frozen packet3/view3/group3 identities anchor
    the research-enabled closure beside the byte-exact packet2 identities.
    """

    @classmethod
    def setUpClass(cls):
        actions, workflows, teaching, _ = generator.load_workflow_action_contracts()
        cls.projected, _ = generator.project_workflow_action_schema(copy.deepcopy(payload_schema), actions, workflows, teaching)

    def validate(self, value, name):
        generator.schema_validate(value, self.projected["$defs"][name], self.projected)

    _RESEARCH_ALIASES = {
        "lane_work_context_research_selection": "work_context_research_selection",
        "lane_work_context_research_finding": "work_context_research_finding",
        "lane_work_context_research_finding_view": "work_context_research_finding_view",
        "lane_research_scopes": "research_scopes",
        "lane_research_source": "research_source",
    }

    def _remap(self, node):
        """The canonical lane node under its shared payload name: every
        lane-local $ref renamed through the projection aliases."""
        def remap(node):
            if isinstance(node, list):
                return [remap(value) for value in node]
            if not isinstance(node, dict):
                return node
            result = {key: remap(value) for key, value in node.items()}
            if "$ref" in result:
                result["$ref"] = "#/$defs/" + self._RESEARCH_ALIASES[result["$ref"].removeprefix("#/$defs/")]
            return result
        return remap(copy.deepcopy(node))

    def _remapped_def(self, name):
        return self._remap(packet_schema["$defs"][name])

    def test_the_shipped_mirror_projects_the_canonical_research_defs(self):
        for lane, shared in (("lane_work_context_research_selection", "work_context_research_selection"),
                             ("lane_work_context_research_finding", "work_context_research_finding"),
                             ("lane_work_context_research_finding_view", "work_context_research_finding_view")):
            self.assertEqual(self.projected["$defs"][shared], self._remapped_def(lane))
        # The current public view carries the same projected member the
        # canonical view declares, under the shared def names. Its retained
        # twin replays stored selections, so its items name the retained twin.
        lane_member = packet_schema["$defs"]["lane_work_context_view"]["properties"]["research_findings"]
        self.assertEqual(self.projected["$defs"]["work_context_view"]["properties"]["research_findings"],
                         self._remap(lane_member))
        retained = self._remap(lane_member)
        retained["items"]["$ref"] = "#/$defs/" + generator.retained_twin_name("work_context_research_finding_view")
        twin = self.projected["$defs"][generator.retained_twin_name("work_context_view")]
        self.assertEqual(twin["properties"]["research_findings"], retained)
        # Packet dispatch keeps the lane's bounded scope id; only the retained
        # read twin carries the store's wider knowledge ids.
        retained_scopes = self.projected["$defs"][generator.retained_twin_name("research_scopes")]
        for kind in ("product_ids", "project_ids", "domain_ids", "tag_ids"):
            self.assertEqual(self.projected["$defs"]["research_scopes"]["properties"][kind]["items"], {"$ref": "#/$defs/id"})
            self.assertEqual(retained_scopes["properties"][kind]["items"], {"$ref": "#/$defs/knowledge_id"})

    def _project_with_lane_packet(self, lane_document):
        """Project against a temporary canonical lane packet on disk: the
        projection owner reads the canonical file, so a canonical-change test
        mutates the file and restores its exact bytes."""
        lane_path = generator.LANE_PACKET
        original = lane_path.read_bytes()
        lane_path.write_text(json.dumps(lane_document, ensure_ascii=False, indent=2) + "\n")
        try:
            actions, workflows, teaching, _ = generator.load_workflow_action_contracts()
            return generator.project_workflow_action_schema(copy.deepcopy(payload_schema), actions, workflows, teaching)[0]
        finally:
            lane_path.write_bytes(original)

    def test_a_canonical_research_change_lands_on_regeneration(self):
        canonical = copy.deepcopy(packet_schema)
        canonical["$defs"]["lane_work_context_view"]["properties"]["research_findings"]["maxItems"] = 17
        regenerated = self._project_with_lane_packet(canonical)
        self.assertEqual(regenerated["$defs"]["work_context_view"]["properties"]["research_findings"]["maxItems"], 17)
        self.assertNotEqual(self.projected["$defs"]["work_context_view"]["properties"]["research_findings"]["maxItems"], 17)

    def test_an_unprojected_research_reference_fails_loudly(self):
        canonical = copy.deepcopy(packet_schema)
        canonical["$defs"]["lane_work_context_research_finding"]["properties"]["scopes"] = {"$ref": "#/$defs/lane_plan"}
        with self.assertRaises(ValueError) as ctx:
            self._project_with_lane_packet(canonical)
        self.assertIn("lane_plan", str(ctx.exception))

    def test_current_research_identity_matches_its_declared_source(self):
        declared = payload_schema["$defs"]
        for name in ("worker_packet", "work_context_view", "work_context_domain_group"):
            self.assertEqual(self.projected["$defs"][name], declared[name],
                             f"the {name} identity differs from its declared source")
        work_context = self.projected["$defs"]["worker_packet"]["properties"]["inputs"]["properties"]["work_context"]
        self.assertEqual(work_context["$ref"], "#/$defs/work_context_view")
        group_ref = self.projected["$defs"]["work_context_view"]["properties"]["domain_groups"]["items"]["$ref"]
        self.assertEqual(group_ref, "#/$defs/work_context_domain_group")
        self.assertEqual(
            self.projected["$defs"]["work_context_domain_group"]["properties"]["finding_ids"]["items"]["pattern"],
            "^(finding|research):[0-9]+:[0-9]+$")

    def test_the_closed_research_entry_branches_decide_the_verdict(self):
        selection = {"pack_id": "pack-schema", "revision": 1, "finding_id": "f-schema", "domain_id": "root"}
        self.validate(selection, "work_context_research_selection")
        with self.assertRaises(ValueError):
            self.validate(selection | {"unknown_member": True}, "work_context_research_selection")
        provenance = {"selection": selection, "view_id": "research:9:0", "selection_event_id": "event:9:semantic",
                      "selection_event_seq": 9, "ordinal": 0, "required": True}
        finding = {"pack_id": "pack-schema", "revision": 1, "finding_id": "f-schema", "kind": "observation",
                   "statement": "the selected finding", "confidence": "high", "freshness": "current",
                   "status": "active", "scopes": {"mode": "home"}}
        source = {"pack_id": "pack-schema", "revision": 1, "source_id": "src-schema", "kind": "source_code",
                  "locator": "internal/store", "title": "the selected source",
                  "publisher_or_author": "concord", "accessed_at": "2026-01-01T00:00:00Z"}
        self.validate(provenance | {"state": "resolved", "finding": finding, "sources": [source]},
                      "work_context_research_finding_view")
        self.validate(provenance | {"state": "missing"}, "work_context_research_finding_view")
        for invalid in (
            # A resolved-state entry without its body, and an unresolved-state
            # entry carrying a body, are outside the closed branches.
            provenance | {"state": "resolved"},
            provenance | {"state": "missing", "finding": finding, "sources": []},
            provenance | {"state": "fabricated"},
            # An unsourced native finding stays accurate: sources may be
            # empty, but the scopes member the Go reader always serializes
            # can never drop off the finding.
            provenance | {"state": "resolved", "finding": {k: v for k, v in finding.items() if k != "scopes"}, "sources": []},
        ):
            with self.assertRaises(ValueError):
                self.validate(invalid, "work_context_research_finding_view")


class RetryResponsePacketUnionTests(unittest.TestCase):
    """CD-0242 D14: retry transports the exact current admitted packet."""

    def member(self):
        return payload_schema["$defs"]["mutation_result"]["properties"]["retry_worker_packet"]

    def validate(self, value):
        generator.schema_validate(value, self.member(), payload_schema)

    def plain_packet(self, name):
        return _schema_sample(payload_schema["$defs"][name], payload_schema)

    def research_packet(self):
        packet = self.plain_packet("worker_packet")
        finding = {"pack_id": "pack-schema", "revision": 1, "finding_id": "f-schema", "kind": "observation",
                   "statement": "the selected finding", "confidence": "high", "freshness": "current",
                   "status": "active", "scopes": {"mode": "home"}}
        source = {"pack_id": "pack-schema", "revision": 1, "source_id": "src-schema", "kind": "source_code",
                  "locator": "internal/store", "title": "the selected source",
                  "publisher_or_author": "concord", "accessed_at": "2026-01-01T00:00:00Z"}
        entry = {"selection": {"pack_id": "pack-schema", "revision": 1, "finding_id": "f-schema", "domain_id": "root"},
                 "view_id": "research:9:0", "selection_event_id": "event:9:research", "selection_event_seq": 9,
                 "ordinal": 0, "required": True, "state": "resolved", "finding": finding, "sources": [source]}
        view = packet["inputs"].get("work_context")
        if view is None:
            view = _schema_sample(payload_schema["$defs"]["work_context_view"], payload_schema)
            packet["inputs"]["work_context"] = view
        view["research_findings"] = [entry]
        view["domain_groups"] = [{"domain_id": "root", "required_reading_ordinals": [],
                                  "finding_ids": ["research:9:0"]}]
        return packet

    def test_the_retry_response_uses_the_current_packet(self):
        member = self.member()
        self.assertEqual(member["$ref"], "#/$defs/worker_packet")
        self.assertNotIn("anyOf", member)
        self.assertNotIn("oneOf", member)

    def test_plain_packet_admits(self):
        self.validate(self.plain_packet("worker_packet"))

    def test_research_bearing_packet_transports_and_malformed_refuses(self):
        self.validate(self.research_packet())
        malformed = self.research_packet()
        malformed["inputs"]["work_context"]["research_findings"][0]["selection"]["unknown_member"] = True
        with self.assertRaises(ValueError):
            self.validate(malformed)


class _WorkloadReached(Exception):
    """Marks that a generator main entered its workload during a reach test."""


class GeneratorCLISafetyTests(unittest.TestCase):
    """Shared CLI contract for all nine generate-* main entry points.

    Help must exit 0 with usage on stdout and perform no main-workload read,
    write, mkdir, or subprocess work. Unrecognized arguments — abbreviations
    of --check, bare positionals, unknown flags, and unknown flags mixed with
    check or the contracts freeze flag — must exit 2 with an error on stderr
    and perform no main work. Valid no-argument and --check invocations must
    still reach the workload, and the contracts freeze flag must keep
    dispatching the freeze, with its schema refs, before check.
    """

    # (script name, module, the first workload input the main reads)
    GENERATORS = (
        ("generate-agent-contracts.py", generator, "MANIFEST"),
        ("generate-agent-lanes.py", lanes_generator, "MANIFEST"),
        ("generate-work-kinds.py", work_kinds_generator, "VOCABULARY"),
        ("generate-native-run-statuses.py", native_run_statuses_generator, "VOCABULARY"),
        ("generate-work-sizes.py", work_sizes_generator, "VOCABULARY"),
        ("generate-relation-vocabulary.py", relation_vocabulary_generator, "VOCABULARY"),
        ("generate-durable-tier-budget.py", durable_tier_budget_generator, "BUDGET"),
        ("generate-branch-freshness.py", branch_freshness_generator, "DECLARATION"),
        ("generate-lane-step-dispatch.py", lane_step_dispatch_generator, "JOIN"),
    )
    REFUSAL_ARGSETS = (
        ("unknown-flag", ["--unknown-option"]),
        ("positional", ["unexpected-positional"]),
        ("check-abbreviation", ["--ch"]),
        ("freeze-abbreviation", ["--freeze"]),
        ("check-with-unknown", ["--check", "--unknown-option"]),
    )

    def _argv(self, script: str, args: list[str]):
        return unittest.mock.patch.object(sys, "argv", [script, *args])

    @staticmethod
    def _refuse(action: str):
        def guard(*args, **kwargs):
            raise AssertionError(f"generator main attempted {action} before argument handling")
        return guard

    def _guard_main_work(self):
        stack = ExitStack()
        for target, action in (
            ("pathlib.Path.read_text", "a file read"),
            ("pathlib.Path.write_text", "a file write"),
            ("pathlib.Path.mkdir", "a directory create"),
            ("subprocess.run", "a subprocess"),
        ):
            stack.enter_context(unittest.mock.patch(target, self._refuse(action)))
        return stack

    def test_help_exits_zero_with_usage_and_no_main_work(self):
        for script, module, _ in self.GENERATORS:
            for flag in ("-h", "--help"):
                with self.subTest(generator=script, flag=flag):
                    with (
                        redirect_stdout(io.StringIO()) as stdout,
                        redirect_stderr(io.StringIO()),
                        self._guard_main_work(),
                        self._argv(script, [flag]),
                        self.assertRaises(SystemExit) as exit,
                    ):
                        module.main()
                    self.assertEqual(exit.exception.code, 0)
                    self.assertIn("usage", stdout.getvalue().lower())

    def test_invalid_arguments_exit_two_with_no_main_work(self):
        for script, module, _ in self.GENERATORS:
            for name, args in self.REFUSAL_ARGSETS:
                with self.subTest(generator=script, args=name):
                    with (
                        redirect_stdout(io.StringIO()),
                        redirect_stderr(io.StringIO()) as stderr,
                        self._guard_main_work(),
                        self._argv(script, args),
                        self.assertRaises(SystemExit) as exit,
                    ):
                        module.main()
                    self.assertEqual(exit.exception.code, 2)
                    self.assertTrue(stderr.getvalue().strip())

    def test_contracts_freeze_mode_refuses_without_work(self):
        args = ["--freeze-historical-decoder"]
        with (
            redirect_stdout(io.StringIO()),
            redirect_stderr(io.StringIO()),
            self._guard_main_work(),
            self._argv("generate-agent-contracts.py", args),
            self.assertRaises(SystemExit) as exit,
        ):
            generator.main()
        self.assertEqual(exit.exception.code, 2)

    def test_valid_modes_reach_the_workload(self):
        for script, module, first_input in self.GENERATORS:
            for args in ([], ["--check"]):
                with self.subTest(generator=script, args=args):
                    stub = unittest.mock.MagicMock()
                    stub.read_text.side_effect = _WorkloadReached("workload reached")
                    with (
                        self._guard_main_work(),
                        unittest.mock.patch.object(module, first_input, stub),
                        self._argv(script, args),
                        self.assertRaises(_WorkloadReached),
                    ):
                        module.main()


if __name__ == "__main__": unittest.main()
