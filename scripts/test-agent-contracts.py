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
envelope_schema = json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text())
payload_schema = json.loads((ROOT / "contracts/agent-tool-surface-payloads.schema.json").read_text())


class RetainedWorkflowPayloadTests(unittest.TestCase):
    def test_evidence_binding_accepts_optional_producer_watermark(self):
        schema = payload_schema["$defs"]["work_transition_action_variant_bind_evidence"]
        call = {"work_id": "work-1", "expected_version": 1, "action_id": "bind_evidence",
                "idempotency_key": "key-1", "fields": {"producer_watermark": "watermark:one"}}
        generator.schema_validate(call, schema, payload_schema)
        generator.schema_validate(call | {"fields": {}}, schema, payload_schema)

    def test_review_requires_complete_step_attempt(self):
        schema = payload_schema["$defs"]["work_transition_action_variant_record_review"]
        fields = {"step_id": "review", "note": "Reviewed.", "reviewer": "reviewer:one",
                  "result": "pass", "commit": "commit:one", "link": "review:one"}
        call = {"work_id": "work-1", "expected_version": 1, "action_id": "record_review",
                "idempotency_key": "key-1", "fields": fields}
        generator.schema_validate(call, schema, payload_schema)
        for key in fields:
            with self.subTest(missing=key), self.assertRaises(ValueError):
                generator.schema_validate(call | {"fields": {k: v for k, v in fields.items() if k != key}}, schema, payload_schema)

    def test_legislative_subject_capacity_and_uniqueness(self):
        defs = payload_schema["$defs"]
        schemas = [defs["workflow_contract_update"]["properties"]["legislative_subjects"],
                   defs["work_transition_action_variant_approve_contract"]["properties"]["fields"]["properties"]["legislative_subjects"]]
        subjects = [f"spec:{i}" for i in range(800)]
        for schema in schemas:
            with self.subTest(schema=schema):
                generator.schema_validate([], schema, payload_schema)
                generator.schema_validate(subjects, schema, payload_schema)
                with self.assertRaises(ValueError):
                    generator.schema_validate(subjects + ["spec:extra"], schema, payload_schema)
                with self.assertRaises(ValueError):
                    generator.schema_validate(["spec:one", "spec:one"], schema, payload_schema)


class WorkflowTemplateBootstrapTests(unittest.TestCase):
    def test_check_reports_drift_and_write_projects_before_helper(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "internal/store/generated_workflow_templates.go"
            target.parent.mkdir(parents=True)
            target.write_text("stale")
            with unittest.mock.patch.object(generator, "ROOT", root), unittest.mock.patch.object(
                generator, "go_workflow_templates_projection", return_value="projected"
            ):
                with self.assertRaisesRegex(ValueError, "generated contract drift: internal/store/generated_workflow_templates.go"):
                    generator.prepare_workflow_templates(True)
                self.assertEqual(target.read_text(), "stale")
                generator.prepare_workflow_templates(False)
                self.assertEqual(target.read_text(), "projected")
                generator.prepare_workflow_templates(True)


class NativeTaskContractTests(unittest.TestCase):
    def setUp(self):
        self.payload = copy.deepcopy(payload_schema)
        self.defs = self.payload["$defs"]

    def test_pruning_keeps_transitive_definitions(self):
        self.defs["root"] = {"$ref": "#/$defs/leaf"}
        self.defs["leaf"] = {"type": "string"}
        generator.prune_payload_definitions(self.payload, {"schemas": {"root": {"ref": "#/$defs/root"}}}, [])
        for name in ("root", "leaf"):
            self.assertIn(name, self.payload["$defs"])
        broken = copy.deepcopy(self.payload)
        broken["$defs"]["root"] = {"$ref": "#/$defs/missing"}
        with self.assertRaisesRegex(ValueError, "missing payload schema missing"):
            generator.prune_payload_definitions(broken, {"schemas": {"root": {"ref": "#/$defs/root"}}}, [])

    def test_pruning_respects_the_schema_document_owner(self):
        self.defs["root"] = {"$ref": "#/$defs/leaf"}
        self.defs["leaf"] = {"type": "string"}
        schemas = {
            "root": {"ref": "contracts/agent-tool-surface-payloads.schema.json#/$defs/root"},
            "base_envelope": {"ref": "contracts/agent-tool-envelope.schema.json#/$defs/base"},
        }
        generator.prune_payload_definitions(self.payload, {"schemas": schemas}, [])
        self.assertIn("root", self.payload["$defs"])
        self.assertIn("leaf", self.payload["$defs"])
        self.assertNotIn("base", self.payload["$defs"])

    def test_pruning_keeps_current_action_schema_seeds(self):
        self.defs["engine_entry"] = {"$ref": "#/$defs/engine_leaf"}
        self.defs["engine_leaf"] = {"type": "string"}
        generator.prune_payload_definitions(self.payload, {"schemas": {}}, [], ["engine_entry"])
        self.assertIn("engine_entry", self.payload["$defs"])
        self.assertIn("engine_leaf", self.payload["$defs"])

# CD-0218 D1/D2 absence witnesses, not a production action catalog.
RETIRED_PUBLISHED_ACTIONS = {
    "accept_decision", "approve_operation",
    "checkpoint_action", "checkpoint_analysis", "checkpoint_poc", "checkpoint_run",
    "cleanup_run", "declare_scope", "discard_poc", "frame_question", "record_decision",
    "record_health", "record_option", "record_research", "rollback_run",
    "run_analysis", "start_action", "start_poc", "start_run", "record_reproduction",
    "record_root_cause", "start_repair", "start_refine", "record_worker_failure", "record_discovery",
}


class MutationMetadataTests(unittest.TestCase):
    def test_mutations_preserve_capability_consequence_and_request_metadata(self):
        for identifier, capability, consequence, context in [
            ("concord_work_transition.remove", "work_transition", "lifecycle", "ambient"),
            ("concord_work_relate.set_memberships", "work_relate", "scope", "explicit"),
        ]:
            with self.subTest(operation=identifier):
                operation = next(op for op in manifest["operations"] if op["id"] == identifier)
                self.assertEqual(operation["kind"], "mutation")
                self.assertEqual(operation["capability"], capability)
                self.assertEqual(operation["consequence"], consequence)
                self.assertEqual(operation["metadata"]["idempotency"], "required")
                self.assertEqual(operation["metadata"]["context"], context)


class WorkflowPlanTemplateSchemaTests(unittest.TestCase):
    def test_template_steps_form_valid_initial_plans(self):
        contract = json.loads((ROOT / "contracts/workflow-step-templates.v1.json").read_text())
        schema = contract["plan_schema"]
        types = [template["id"] for template in contract["types"]]
        self.assertEqual(types, schema["properties"]["type"]["enum"])
        self.assertEqual(len(types), len(set(types)))
        for template in contract["types"]:
            steps = []
            for index, step in enumerate(template["steps"]):
                self.assertTrue(step["actions"])
                self.assertEqual(len(step["actions"]), len(set(step["actions"])))
                steps.append({"id": step["id"], "required": True,
                              "after": [template["steps"][index - 1]["id"]] if index else [],
                              "skill": step["skill"], "helper": step["helper"], "reviewed": step["reviewed"]})
            plan = {"schema_version": contract["schema_version"], "type": template["id"], "steps": steps}
            with self.subTest(plan_type=template["id"]):
                generator.schema_validate(plan, schema, schema)
                with self.assertRaises(ValueError):
                    generator.schema_validate(plan | {"unexpected": True}, schema, schema)


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
        corpus["scenarios"] = [item for item in corpus["scenarios"] if item["id"] != "WF19-completion-one-transaction"]
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

    def test_native_requests_keep_their_required_fields(self):
        original = lane_checker._load_json(ROOT / ".concord/scenarios/workflow-engine.v1.json")
        approval = copy.deepcopy(original["scenarios"][0])
        approval["action"] = approval["request"]["action_id"] = "approve_contract"
        approval["request"]["fields"] = {}
        original["scenarios"].append(approval)
        self.assertEqual(lane_checker._structured_scenario_check(original), [])
        for action, field in (("complete", "impact_verdict"),):
            with self.subTest(action=action, field=field):
                corpus = copy.deepcopy(original)
                scenario = next(item for item in corpus["scenarios"] if item["action"] == action)
                del scenario["request"]["fields"][field]
                findings = lane_checker._structured_scenario_check(corpus)
                self.assertTrue(any("mandatory structured request field" in finding and field in finding for finding in findings), findings)
        corpus = copy.deepcopy(original)
        corpus["fixtures"]["workflow_types"].remove("removal")
        findings = lane_checker._structured_scenario_check(corpus)
        self.assertIn("scenario workflow types differ from the template contract", findings)
        corpus = copy.deepcopy(original)
        corpus["scenarios"][0]["setup"]["fixture_refs"]["workflow_types"] = ["change"]
        findings = lane_checker._structured_scenario_check(corpus)
        self.assertTrue(any("unpicked workflow type has type references" in finding for finding in findings), findings)
        corpus = copy.deepcopy(original)
        scenario = next(item for item in corpus["scenarios"] if item["request"]["workflow_type"] == "change")
        scenario["setup"]["current_state"]["step"] = "diagnose"
        findings = lane_checker._structured_scenario_check(corpus)
        self.assertTrue(any("current step is not in the selected workflow template" in finding for finding in findings), findings)

    def test_checker_mutations_reach_current_safeguards(self):
        corpus_path = ROOT / ".concord/scenarios/workflow-engine.v1.json"
        original = lane_checker._load_json(corpus_path)
        for mutation, expected in (
            ("actor", "actor-ID derivation mismatch"),
            ("scope", "request scope mismatch"),
            ("workflow_type", "dangling fixture reference workflow type"),
            ("error", "undeclared scenario error kind"),
        ):
            with self.subTest(mutation=mutation):
                corpus = copy.deepcopy(original)
                scenario = corpus["scenarios"][0]
                if mutation == "actor":
                    scenario["request"]["grant"]["agent_ref"] = "agent:other"
                elif mutation == "scope":
                    scenario["request"]["grant"]["scope"] = "project:other"
                elif mutation == "workflow_type":
                    scenario["request"]["workflow_type"] = "missing"
                else:
                    refused = next(item for item in corpus["scenarios"] if item["id"] == "WF19-completion-one-transaction")
                    refused["expected"]["assertions"][0]["value"] = "not_a_real_error_kind"
                load = lane_checker._load_json
                with unittest.mock.patch.object(lane_checker, "_load_json", side_effect=lambda path: corpus if path == corpus_path else load(path)):
                    findings = lane_checker.check_workflow_contracts()
                self.assertTrue(any(expected in finding for finding in findings), findings)


class ContractGuidanceProjectionTests(unittest.TestCase):
    def test_spec_mandate_teaches_law_references(self):
        actions, teaching, _, updates = generator.load_workflow_action_contracts()
        projected, _ = generator.project_workflow_action_schema(payload_schema, actions, teaching, updates)
        defs = projected["$defs"]
        fields = [defs["work_transition_action_variant_approve_contract"]["properties"]["fields"]["properties"]["spec_mandate"],
                  defs["workflow_contract_update"]["properties"]["spec_mandate"]]
        for field in fields:
            for phrase in ("law_id", "project_id/law_id", "record_plan.touches", "intent", "empty"):
                self.assertIn(phrase, field.get("description", ""))

    def test_guidance_keeps_schema_key_order(self):
        document = {"title": "Example", "$defs": {"code": {"type": "string", "minLength": 2}}}
        projected = generator.project_schema_guidance(document, ["code"])
        self.assertEqual(list(projected), list(document))
        self.assertEqual(list(projected["$defs"]["code"])[:2], ["type", "minLength"])

    def test_examples_are_instances_not_subschemas(self):
        generator.check_schema_keywords({"type": "object", "examples": [{"example_field": "sample"}]})
        with self.assertRaisesRegex(ValueError, "unsupported JSON Schema keywords"):
            generator.check_schema_keywords({"items": {"unknown_constraint": True}})

    def test_contract_fields_publish_schema_derived_examples(self):
        actions, teaching, _, contract_updates = generator.load_workflow_action_contracts()
        projected, _ = generator.project_workflow_action_schema(payload_schema, actions, teaching, contract_updates)
        field = projected["$defs"]["workflow_contract_update"]["properties"]["spec_mandate"]
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


profiles_generator = _load_cli_module("cli_safety_profiles", "generate-agent-profiles.py")
work_kinds_generator = _load_cli_module("cli_safety_work_kinds", "generate-work-kinds.py")
native_run_statuses_generator = _load_cli_module("cli_safety_native_run_statuses", "generate-native-run-statuses.py")
relation_vocabulary_generator = _load_cli_module("cli_safety_relation_vocabulary", "generate-relation-vocabulary.py")
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


class ProducerFailureStderrBoundTests(unittest.TestCase):
    """A failed Go producer surfaces its stderr, bounded.

    An unbounded print buries the one-line root cause (for example
    `undefined: containsString`) under every compiler error a broken merge
    produced, so the failure message keeps a bounded head and says when it
    truncated.
    """

    CAUSE = "internal/store/workflow_operator.go:438:12: undefined: containsString"
    RUNAWAY = "noise line\n" * 2000

    @staticmethod
    def _failing_producer(stderr_text: str):
        def failing(args, **kwargs):
            if list(args[:2]) == ["go", "run"]:
                raise subprocess.CalledProcessError(1, args, stderr=stderr_text)
            return _REAL_SUBPROCESS_RUN(args, **kwargs)
        return failing

    def _check_main(self, stderr_text: str):
        error = io.StringIO()
        with (
            unittest.mock.patch.object(generator.subprocess, "run", self._failing_producer(stderr_text)),
            redirect_stdout(io.StringIO()),
            redirect_stderr(error),
            unittest.mock.patch.object(sys, "argv", ["generate-agent-contracts.py", "--check"]),
        ):
            code = generator.main()
        return code, error.getvalue()

    def test_a_failing_producer_failure_message_carries_bounded_stderr(self):
        code, error = self._check_main(self.CAUSE + "\n" + self.RUNAWAY)
        self.assertEqual(code, 1)
        self.assertIn("agent contract generation failed", error)
        self.assertIn("workflow-action-contracts", error, "the failure must keep naming the failed command")
        self.assertIn(self.CAUSE, error, "the leading compiler cause must survive the bound")
        self.assertIn("truncated", error, "a bounded print must say it truncated")
        self.assertLessEqual(
            len(error), generator.PRODUCER_STDERR_LIMIT + 300,
            "the failure message must stay bounded when the producer stderr is huge",
        )

    def test_a_failing_producer_with_short_stderr_prints_it_whole(self):
        code, error = self._check_main(self.CAUSE + "\n")
        self.assertEqual(code, 1)
        self.assertIn(self.CAUSE, error)
        self.assertNotIn("truncated", error)

    def test_a_failing_schema_guidance_run_bounds_its_embedded_stderr(self):
        completed = subprocess.CompletedProcess(
            ["go", "run", "./scripts/schema-guidance"], 1, stdout="", stderr=self.CAUSE + "\n" + self.RUNAWAY)
        with unittest.mock.patch.object(generator.subprocess, "run", return_value=completed):
            with self.assertRaises(ValueError) as raised:
                generator.project_schema_guidance({"type": "object"}, ["a"])
        message = str(raised.exception)
        self.assertIn("schema guidance generation failed", message)
        self.assertIn(self.CAUSE, message)
        self.assertIn("truncated", message)
        self.assertLessEqual(len(message), generator.PRODUCER_STDERR_LIMIT + 200)


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
        ])
        self.assertEqual(tool["description"],
                          "Navigate Product architecture: current Domains and current law.")


class ResultRequirednessTests(unittest.TestCase):
    def validate_result(self, result):
        payload = copy.deepcopy(payload_schema)
        payload["$defs"]["product_context"] = result
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "payload.json"
            path.write_text(json.dumps(payload))
            with unittest.mock.patch.object(generator, "PAYLOAD", path):
                return generator.validate(manifest)

    def test_versioned_branches_without_a_common_field_are_required(self):
        self.validate_result(self.result_union())

    def result_union(self):
        return {"type": "object", "additionalProperties": False, "properties": {}, "oneOf": [
            {"type": "object", "additionalProperties": False, "required": ["left"], "properties": {"left": {"type": "string"}}},
            {"type": "object", "additionalProperties": False, "required": ["right"], "properties": {"right": {"type": "string"}}},
        ]}

    def test_one_branch_without_required_fields_is_refused(self):
        result = self.result_union()
        result["oneOf"][1].pop("required")
        with self.assertRaisesRegex(ValueError, "result schema lacks required fields"):
            self.validate_result(result)

    def test_root_without_required_fields_or_branches_is_refused(self):
        result = self.result_union()
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
            {"$ref": "#/$defs/product_context"}, payload_schema["$defs"]))


class EvidenceFixedBudgetCeilingTests(unittest.TestCase):
    """CD-0038 D2: the surface ceiling is uniform except where accepted
    evidence fixes a different value. The generator admits only the
    declared exception (workflow_action TS1 30); any other non-uniform value, including one that mimics an
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
        # The shipped manifest declares workflow_action's non-uniform ceiling;
        # validate() enforces
        # that, and the rest of the surface must stay at the uniform 300.
        try:
            generator.validate(manifest)
        except ValueError as err:
            self.fail(f"shipped manifest violates its own uniformity rule: {err}")
        non_uniform = {op["id"]: op["supported_budget_seconds"] for op in manifest["operations"]
                       if op["supported_budget_seconds"] != 300}
        self.assertEqual(non_uniform, {
            "concord_work_transition.workflow_action": 30,
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
        # The workflow_action ceiling is not transferable to another operation.
        drifted = self._replace_supported_budget("concord_work_transition.lifecycle", 30)
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
        document = (ROOT / ".concord/law/specs/agent-tool-surface-budget.md").read_text()
        matches = re.findall(r"always_visible_tools: (\d+)", document)
        self.assertEqual(len(matches), 1, "TS2 must state the budget exactly once")
        declared = int(matches[0])
        self.assertEqual(manifest["surface"]["tool_count"], declared,
                         "manifest surface.tool_count disagrees with the TS2 budget")
        self.assertEqual(len(manifest["tools"]), declared,
                         "manifest tool list disagrees with the TS2 budget")


class CurrentSkillReferenceTests(unittest.TestCase):
    CURRENT = "https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md"

    def test_shipped_law_urls_name_current_main_law_and_existing_targets(self):
        count = 0
        for path in sorted((ROOT / "skills").rglob("*")):
            if not path.is_file():
                continue
            for target in lane_checker.skill_reference_targets(path.read_text()):
                if ".concord/" not in target:
                    continue
                with self.subTest(path=path.relative_to(ROOT), target=target):
                    match = re.fullmatch(r"https://github\.com/Sharper-Flow/concord/(?:tree|blob)/main/(\.concord/law/[^#]+)(?:#[a-z0-9-]+)?", target)
                    self.assertIsNotNone(match)
                    self.assertTrue((ROOT / match[1]).is_file())
                count += 1
        self.assertGreater(count, 0)

    def test_current_law_url_is_accepted_without_git_history(self):
        with unittest.mock.patch.object(lane_checker.subprocess, "run", side_effect=AssertionError("history is not current law")):
            self.assertEqual(lane_checker.shipped_skill_reference_findings(ROOT, f"[Law]({self.CURRENT})"), [])

    def test_stale_law_urls_are_rejected_in_every_link_form(self):
        for target in (
            self.CURRENT.replace("/main/", "/" + "a" * 40 + "/"),
            self.CURRENT.replace(".concord/law/", ".concord/docs/"),
            self.CURRENT.replace("/main/", "/release/"),
            self.CURRENT.replace("/law/decisions/", "/law/../decisions/"),
            "https://github.com/Sharper-Flow/concord/tree/" + "a" * 40 + "/.concord/docs/priorities.md",
        ):
            for content in (f"[Law]({target})", f"[law]: {target}", f"<{target}>", target):
                with self.subTest(content=content):
                    findings = lane_checker.shipped_skill_reference_findings(ROOT, content)
                    self.assertTrue(any("current main canonical law" in finding for finding in findings), findings)

    def test_missing_current_law_target_is_rejected(self):
        target = self.CURRENT.rsplit("/", 1)[0] + "/missing-rule.md"
        findings = lane_checker.shipped_skill_reference_findings(ROOT, target)
        self.assertTrue(any("does not exist" in finding for finding in findings), findings)

    def test_readme_and_support_file_law_urls_cannot_bypass_shipped_check(self):
        stale = self.CURRENT.replace("/main/", "/" + "a" * 40 + "/")
        read_text = Path.read_text
        for relative in ("skills/README.md", "skills/concord-diagnosis/LICENSE"):
            def mutate(path, *args, **kwargs):
                content = read_text(path, *args, **kwargs)
                return content + "\n" + stale if path == ROOT / relative else content
            with self.subTest(path=relative), unittest.mock.patch.object(Path, "read_text", mutate):
                findings = lane_checker._check_shipped_skills()
                self.assertTrue(any(relative in finding and "current main canonical law" in finding for finding in findings), findings)


class ShippedSkillTests(unittest.TestCase):
    NAME = "concord-law-writing"
    COMMANDS = {"product-knowledge-home-designate"}
    OPERATIONS = {
        "concord_domain.list",
        "concord_product_view.resolve",
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

    def test_unquoted_colon_space_in_description_is_rejected(self):
        content = re.sub(r"(?m)^description:.*$", "description: Use when a defect blocks your item: run the repair route", self.content())
        findings = self.findings(content)
        self.assertTrue(any("valid YAML" in finding for finding in findings), findings)

    def test_description_must_load_as_a_nonempty_string(self):
        for description in ("[Use when x]", "42", "true", '""', "|"):
            with self.subTest(description=description):
                content = re.sub(r"(?m)^description:.*$", "description: " + description, self.content())
                findings = self.findings(content)
                self.assertTrue(any("non-empty YAML string" in finding for finding in findings), findings)

    def test_private_content_uses_existing_public_content_patterns(self):
        for forbidden in ("/" + "home" + "/synthetic/notes", "127." + "0.0.1", "gh" + "p_" + "a" * 32):
            with self.subTest(forbidden=forbidden):
                self.assertTrue(any("private" in f or "credential" in f for f in self.findings(self.content() + "\n" + forbidden)))

    def test_host_tools_are_rejected(self):
        for invocation in ("lgrep_search_semantic", "context7_query_docs", "exa_web_search", "github_get_file_contents"):
            with self.subTest(invocation=invocation):
                self.assertTrue(any("host tool" in f for f in self.findings(self.content() + "\n`" + invocation + "`")))

    def test_unknown_concord_commands_and_operations_are_rejected(self):
        for invocation in ("concord invented", "concord_domain.invented", "concord product-knowledge-home-designate --product alpha"):
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
        content = self.content() + '\n`concord --help`\n`concord product-knowledge-home-designate --help`\n`kind: knowledge`\n```json\n{"product_id":"alpha"}\n```\n'
        self.assertEqual(self.findings(content), [])

    def test_canonical_references_cannot_mix_current_and_pinned_law(self):
        content = self.content().replace("/main/", "/" + "a" * 40 + "/", 1)
        self.assertTrue(any("current main canonical law" in f for f in self.findings(content)))

    def test_empty_concord_invocation_is_a_finding_not_a_crash(self):
        self.assertTrue(any("command" in f for f in self.findings(self.content() + "\n`concord `")))

    def test_field_spelling_cannot_hide_an_executable_invocation(self):
        self.assertTrue(any("executable" in f for f in self.findings(self.content() + "\n`run: arbitrary-tool apply -f x`")))

    def test_host_description_bound_is_checked(self):
        content = re.sub(r"(?m)^description:.*$", "description: " + "x" * 1025, self.content())
        self.assertTrue(any("frontmatter" in f for f in self.findings(content)))

    def test_reference_resolution_requires_existing_repository_paths(self):
        with unittest.mock.patch.object(lane_checker.subprocess, "run", side_effect=AssertionError("must not read historical law")):
            self.assertEqual(lane_checker.shipped_skill_reference_findings(ROOT, self.content()), [])
        is_file = Path.is_file
        missing_doc = ROOT / ".concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md"
        with unittest.mock.patch.object(Path, "is_file", lambda path: False if path == missing_doc else is_file(path)):
            findings = lane_checker.shipped_skill_reference_findings(ROOT, self.content())
        self.assertEqual(findings, [f"reference path {missing_doc.relative_to(ROOT)} does not exist in this repository"])

    def test_checkout_relative_nonlaw_and_unrelated_links_are_rejected(self):
        for target in (
            "../../.concord/docs/decisions/rule.md", "../references/schema.json",
            "https://github.com/Sharper-Flow/concord/tree/main/README.md",
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

    def test_canonical_ownership_reference_is_required(self):
        text = re.sub(r"\[Domain ownership\]\([^)]*\)", "Domain ownership", self.content())
        self.assertNotEqual(text, self.content())
        self.assertTrue(any("missing canonical ownership reference" in f for f in self.findings(text)))

    def test_admitted_inventory_is_exact_and_rejects_missing_extra_or_symlinked_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            skills = Path(temporary) / "skills"
            shutil.copytree(ROOT / "skills", skills)
            entry = skills / self.NAME
            content = entry / "SKILL.md"
            self.assertEqual(lane_checker.shipped_skill_inventory_findings(skills), [])
            extra_root = skills / "review-rubric.md"
            extra_root.write_text("Not admitted")
            self.assertTrue(any("review-rubric.md" in f for f in lane_checker.shipped_skill_inventory_findings(skills)))
            extra_root.unlink()
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
            shutil.rmtree(skills)
            skills.mkdir()
            self.assertTrue(any("missing admitted file" in f for f in lane_checker.shipped_skill_inventory_findings(skills)))

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
        "concord_product_view.resolve", "concord_domain.list",
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

    def test_law_approval_reference_is_required(self):
        content = re.sub(r"\[Operator approval boundary\]\([^)]*\)", "Approval", self.content())
        self.assertTrue(any("missing canonical approval reference" in finding for finding in self.findings(content)))

    def test_law_references_resolve_in_current_repository(self):
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


class MethodSkillTests(unittest.TestCase):
    """CD-0246 D1 and D3: portable method skills ship like any admitted skill."""

    NAMES = ("concord-adoption", "concord-diagnosis", "concord-codebase-design", "concord-work-shaping", "concord-retrospective", "concord-instruction-writing", "concord-review", "concord-improve-codebase-architecture", "concord-architecture-writing", "concord-readme-writing", "concord-ui-design", "concord-ticket-writing", "concord-project-updates", "concord-pr-writing", "concord-research-writing", "concord-handoff-writing", "concord-test-design", "concord-diagram-writing")
    # Skills whose method text adapts Matt Pocock's MIT-licensed upstream and
    # keep his notice; concord-diagram-writing keeps its own upstream notices;
    # concord-project-updates and other original Concord skills carry the
    # repository license.
    UPSTREAM_ADAPTED_SKILLS = (frozenset(NAMES) | {"concord-law-writing"}) - {"concord-project-updates", "concord-diagram-writing"}
    OPERATIONS = {"concord_domain.list", "concord_domain.detail", "concord_product_view.resolve"}

    def content(self, name):
        return (ROOT / "skills" / name / "SKILL.md").read_text(encoding="utf-8")

    def findings(self, name, content):
        return lane_checker.shipped_skill_content_findings(
            content, skill_name=name, commands=set(), operations=self.OPERATIONS,
        )

    def test_method_skills_are_admitted_portable_and_resolve_in_current_repository(self):
        for name in self.NAMES:
            with self.subTest(skill=name):
                self.assertIn(name, lane_checker.ADMITTED_SKILLS)
                self.assertEqual(self.findings(name, self.content(name)), [])
                self.assertEqual(lane_checker.shipped_skill_reference_findings(ROOT, self.content(name)), [])

    def test_a_host_tool_in_a_method_skill_is_refused(self):
        content = self.content("concord-diagnosis") + "\nSearch with github_search_code first.\n"
        self.assertTrue(any("host tool" in finding for finding in self.findings("concord-diagnosis", content)))

    def test_instruction_writing_description_names_each_instruction_surface(self):
        description = self.content("concord-instruction-writing").split("description: ", 1)[1].split("\n", 1)[0]
        for surface in ("agent instructions", "agent and role profiles", "shared instruction files", "skills", "AGENTS.md", "agent-definition manifests"):
            with self.subTest(surface=surface):
                self.assertIn(surface, description)

    def test_law_writing_routes_to_the_general_method(self):
        content = self.content("concord-law-writing")
        self.assertIn("Load the shipped `concord-instruction-writing` skill", content)

    def test_research_findings_use_linked_linear_documents(self):
        expectations = {
            "concord-research-writing": (
                "research with a durable result as one cited research record under `.concord/docs/` in the Product's designated Git knowledge home",
                "Where no knowledge home is designated, place it where the requester or the repository's documentation places such notes",
                "During development, the driving session copies research, audit and inventory findings into a Linear Document",
                "attached to the owning issue or project",
                "That session links the Document from the issue and the issue from the Document",
                "development-time copy, not a second long-term home",
                "rather than leaving them only in host files or long issue comments",
                "law, architecture reference and product vision in Git",
                "assignment briefs, quoted decisions and completion reports in comments",
                "private data out of Linear and public repository content",
            ),
            "concord-project-updates": (
                "copy coordination research, audit and inventory findings into a Linear Document",
                "attached to the owning issue or project",
                "Link the Document from the issue and the issue from the Document",
                "development-time copy, not a second long-term home",
                "`concord-research-writing`",
            ),
        }
        for name, phrases in expectations.items():
            content = " ".join(self.content(name).split())
            for phrase in phrases:
                with self.subTest(skill=name, meaning=phrase):
                    self.assertIn(phrase, content)

    def test_linear_writing_methods_carry_the_standard_and_placement(self):
        for name in ("concord-ticket-writing", "concord-project-updates", "concord-handoff-writing", "concord-pr-writing"):
            content = " ".join(self.content(name).split())
            for phrase in (
                "two or three plain sentences", "one heading that names the event",
                "+++ Agent detail", "tables for comparisons", "active voice",
                "code formatting only for copyable commands", "not per-session issue comments",
                "rare: start, blocker and done", "Linear Document linked from the issue",
                "### Example:",
            ):
                with self.subTest(skill=name, meaning=phrase):
                    self.assertIn(phrase, content)
        frontend = " ".join(self.content("concord-ticket-frontend").split())
        self.assertIn("question, recommendation, options and consequences visible outside collapsed detail", frontend)

    def test_every_skill_keeps_its_license_notice(self):
        with tempfile.TemporaryDirectory() as temporary:
            skills = Path(temporary) / "skills"
            shutil.copytree(ROOT / "skills", skills)
            for name in sorted(lane_checker.ADMITTED_SKILLS):
                with self.subTest(skill=name):
                    license_path = skills / name / "LICENSE"
                    if name == "concord-ui-design":
                        expected_notices = ("Copyright 2026 Anthropic, PBC.", "Copyright (c) 2025 Vercel Labs", "Copyright (c) 2026 Matt Pocock")
                    elif name == "concord-diagram-writing":
                        expected_notices = (
                            "Copyright (c) 2026 Beautiful-Mermaid Contributors",
                            "Copyright GitHub, Inc.",
                            "Copyright (c) 2024 Seth Hobson",
                            "Copyright (c) 2025 Axton Liu",
                        )
                    else:
                        expected_notices = (
                            "Copyright (c) 2026 Matt Pocock"
                            if name in self.UPSTREAM_ADAPTED_SKILLS
                            else "Copyright (c) 2026 Concord contributors",
                        )
                    for notice in expected_notices:
                        self.assertIn(notice, license_path.read_text(encoding="utf-8"))
                    license_path.unlink()
                    findings = lane_checker.shipped_skill_inventory_findings(skills)
                    self.assertTrue(any(f"{name}/LICENSE: missing admitted file" in finding for finding in findings))
                    shutil.copy(ROOT / "skills" / name / "LICENSE", license_path)


class TicketWritingStepTableTests(unittest.TestCase):
    def setUp(self):
        self.content = (ROOT / "skills/concord-ticket-writing/SKILL.md").read_text(encoding="utf-8")
        self.templates = lane_checker._load_json(ROOT / "contracts/workflow-step-templates.v1.json")

    def test_shipped_table_agrees_with_the_contract(self):
        self.assertEqual(lane_checker.check_ticket_writing_step_table(), [])

    def test_planted_plan_row_for_removal_is_rejected(self):
        content = self.content.replace("| change, fix | plan |", "| change, fix, removal | plan |")
        self.assertNotEqual(content, self.content)
        findings = lane_checker.ticket_writing_step_table_findings(content, self.templates)
        self.assertEqual(findings, ["step table has pair absent from contract: removal / plan"])

    def test_missing_table_row_is_rejected(self):
        content = re.sub(r"(?m)^\| removal \| list \|.*\n", "", self.content)
        self.assertNotEqual(content, self.content)
        findings = lane_checker.ticket_writing_step_table_findings(content, self.templates)
        self.assertEqual(findings, ["step table omits contract pair: removal / list"])

    def test_unknown_type_token_is_rejected(self):
        content = self.content.replace("| fix | diagnose |", "| bogus | diagnose |")
        self.assertNotEqual(content, self.content)
        findings = lane_checker.ticket_writing_step_table_findings(content, self.templates)
        self.assertTrue(any("bogus" in f and "unknown" in f for f in findings), findings)

    def test_a_removed_or_unrecognizable_table_is_rejected(self):
        content = self.content.replace("| Type | Step |", "| Kind | Step |")
        self.assertNotEqual(content, self.content)
        findings = lane_checker.ticket_writing_step_table_findings(content, self.templates)
        self.assertTrue(any("table" in f and ("missing" in f or "recognizable" in f) for f in findings), findings)

    def test_unreadable_inputs_are_findings(self):
        for path in (ROOT / "skills/concord-ticket-writing/SKILL.md", ROOT / "contracts/workflow-step-templates.v1.json"):
            read_text = Path.read_text
            def unreadable(candidate, *args, **kwargs):
                if candidate == path:
                    raise OSError("unreadable fixture")
                return read_text(candidate, *args, **kwargs)
            with self.subTest(path=path), unittest.mock.patch.object(Path, "read_text", unreadable):
                findings = lane_checker.check_ticket_writing_step_table()
                self.assertTrue(any("unreadable fixture" in f for f in findings), findings)


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


class EnvelopeOperationVocabularyTests(unittest.TestCase):
    """Issue #352: every tool/operation pair the envelope declares must be satisfiable."""

    def findings(self, envelope=None):
        return lane_checker.envelope_operation_findings(
            copy.deepcopy(envelope if envelope is not None else envelope_schema),
        )

    def test_shipped_contracts_agree(self):
        self.assertEqual(self.findings(), [])

    def test_operation_removed_from_the_base_enum_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["operation"]["enum"].remove("resume")
        found = self.findings(value)
        self.assertTrue(any("unsatisfiable" in f and "concord_work_trace.resume" in f for f in found), found)

    def test_operation_removed_from_the_next_intent_enum_is_rejected(self):
        value = copy.deepcopy(envelope_schema); value["$defs"]["nextIntent"]["properties"]["operation"]["enum"].remove("list")
        found = self.findings(value)
        self.assertTrue(any("nextIntent" in f and "concord_work_browse.list" in f for f in found), found)

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
            "properties": {"tool": {"const": "concord_work_browse"}, "operation": {"const": "list"}, "query_id": {"const": "PM1.Q99"}},
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
        value = copy.deepcopy(envelope_schema); value["$defs"]["base"]["properties"]["operation"]["enum"].append("list")
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
        alternatives = node.get("oneOf", node.get("anyOf", [{}]))
        required = dict.fromkeys(node.get("required", []) + alternatives[0].get("required", []))
        properties = node.get("properties", {}) | alternatives[0].get("properties", {})
        for name in required:
            value[name] = _schema_sample(properties[name], root, f"{path}.{name}")
        generator.schema_validate(value, node, root)
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


class SchemaSampleTests(unittest.TestCase):
    def test_sampler_preserves_selected_alternative_properties(self):
        schema = {
            "type": "object", "additionalProperties": False,
            "properties": {"applicable": {"type": "boolean"}},
            "oneOf": [{"required": ["applicable"], "properties": {"applicable": {"const": True}}}],
        }
        self.assertEqual(_schema_sample(schema, {}), {"applicable": True})


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
        cls.actions, cls.teaching, cls.schema_refs, cls.contract_updates = generator.load_workflow_action_contracts()

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

    def test_historical_layouts_are_authorable_nowhere(self):
        defs = payload_schema["$defs"]
        self.assertNotIn("work_transition_action_legacy_input", defs)
        live_ids = {action["id"] for action in self.actions}
        for action_id in ("dispatch_worker", "record_worker_job", "accept_worker_result", "reject_worker_result", "record_worker_failure", "request_correction"):
            self.assertNotIn(action_id, live_ids)
            envelope = {"work_id": "work-1", "expected_version": 1, "action_id": action_id, "idempotency_key": "retired-1", "fields": {}}
            for name in ("work_transition_action_input", "work_transition_action_public_input"):
                with self.assertRaises(ValueError, msg=f"{name} admits a historical layout"):
                    generator.schema_validate(envelope, defs[name], payload_schema, name)

    def test_sampler_preserves_declared_string_bounds(self):
        digest = {"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"}
        self.assertEqual(_schema_sample(digest, payload_schema), "sha256:" + "0" * 64)
        with self.assertRaises(AssertionError):
            _schema_sample(digest | {"maxLength": 64}, payload_schema)

    def test_no_merged_union_admits_combinations_no_definition_allows(self):
        defs = payload_schema["$defs"]
        branches = _condition_branches(_action_condition(defs, "record_review"))
        broken = _schema_sample(_fields_object(branches[0]), payload_schema)
        broken["reviewer"] = 42
        call = {"work_id": "work-1", "expected_version": 1, "action_id": "record_review", "idempotency_key": "review-1", "fields": broken}
        for name in ("work_transition_action_input", "work_transition_action_public_input"):
            with self.assertRaises(ValueError, msg=f"{name} admits a broken Review"):
                generator.schema_validate(call, defs[name], payload_schema, "broken")
        sample = _schema_sample(_fields_object(branches[0]), payload_schema)
        sample.pop("reviewer")
        for branch in branches:
            with self.assertRaises(ValueError):
                generator.schema_validate(sample, _fields_object(branch), payload_schema, "missing-required")

    def test_unknown_fields_are_refused_by_every_alternative(self):
        defs = payload_schema["$defs"]
        for action_id in ("record_review",):
            branches = _condition_branches(_action_condition(defs, action_id))
            sample = _schema_sample(_fields_object(branches[0]), payload_schema)
            sample["mystery"] = True
            for branch in branches:
                with self.assertRaises(ValueError):
                    generator.schema_validate(sample, _fields_object(branch), payload_schema, "unknown")

    def test_single_variant_actions_keep_one_unwrapped_condition(self):
        then = _action_condition(payload_schema["$defs"], "record_review")
        self.assertNotIn("anyOf", then["properties"]["fields"], "an action with one current contract must stay a single branch")

    def test_review_payload_is_identical_at_core_and_host_boundaries(self):
        action = self.action("record_review")
        self.assertEqual(len(action["variants"]), 1)
        for variant in action["variants"]:
            self.assertEqual(variant["payload"], variant["public_payload"])
            self.assertEqual({field["name"] for field in variant["payload"]["fields"]}, {"step_id", "note", "reviewer", "result", "commit", "link"})
        fields = _fields_object(_action_condition(payload_schema["$defs"], "record_review"))
        self.assertEqual(set(fields["properties"]), {"step_id", "note", "reviewer", "result", "commit", "link"})


class LiveAuthoringClosureTests(unittest.TestCase):
    """Public actions and outcome tokens derive from the live plan policy."""

    @classmethod
    def setUpClass(cls):
        cls.actions, cls.teaching, cls.schema_refs, cls.contract_updates = generator.load_workflow_action_contracts()

    def test_the_live_projection_is_the_approved_closed_set(self):
        live_ids = {action["id"] for action in self.actions}
        self.assertNotIn("record_worker_failure", live_ids)
        for action_id in RETIRED_PUBLISHED_ACTIONS:
            self.assertNotIn(action_id, live_ids, f"retired action {action_id} is authorable")
        self.assertIn("record_diagnosis", live_ids)

    def test_current_schema_refs_resolve_in_the_public_document(self):
        self.assertEqual(set(self.schema_refs), {"current"})
        generator.validate_current_schema_refs(self.schema_refs, payload_schema["$defs"])

    def test_current_schema_refs_refuse_missing_or_dangling_definitions(self):
        for defs in ({}, {"root": {"$ref": "#/$defs/missing"}}):
            with self.subTest(defs=defs), self.assertRaises(ValueError):
                generator.validate_current_schema_refs({"current": ["root"]}, defs)

    def test_main_validates_projected_clarification_definition(self):
        seen = []
        def capture(_refs, definitions):
            seen.append(definitions["clarification_answer_text"])
            raise RuntimeError("captured")
        with unittest.mock.patch.object(sys, "argv", ["generator", "--check"]), \
             unittest.mock.patch.object(generator, "prepare_workflow_templates"), \
             unittest.mock.patch.object(generator, "validate_current_schema_refs", side_effect=capture), \
             self.assertRaisesRegex(RuntimeError, "captured"):
            generator.main()
        self.assertEqual(seen, [{"type": "string", "minLength": 1, "maxLength": 2048, "pattern": r"\S"}])


class ProjectionAdmissionTests(unittest.TestCase):
    """The projection loader checks structure, not a second action catalog."""

    @staticmethod
    def canned(actions):
        teaching = {rule: "rule" for rule in (
            "spec_mandate_rule", "law_addition_mandate_rule", "obligation_membership_rule",
            "architecture_domain_rule", "alignment_combination_rule")}
        teaching["action_rules"] = {}
        return json.dumps({
            "schema_version": "2.0", "actions": actions,
            "teaching": teaching, "schema_refs": {"current": []},
            "contract_updates": [],
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

    def test_the_projection_refuses_a_teaching_rule_for_an_unknown_action(self):
        fake = json.loads(self.canned([{"id": "known-action"}]))
        fake["teaching"]["action_rules"] = {"missing-action": "rule"}
        with self.assertRaises(ValueError) as caught:
            self.load(json.dumps(fake))
        self.assertIn("unknown actions: missing-action", str(caught.exception))

    def test_the_projection_refuses_an_old_projection_version(self):
        fake = json.dumps({
            "schema_version": "1.0",
            "actions": [{"id": "old-action", "variants": [], "legacy_payloads": [], "cross_field": {}}],
            "workflows": [], "teaching": {}, "schema_refs": {"current": []},
        })
        with self.assertRaises(ValueError) as caught:
            self.load(fake)
        self.assertIn("projection is invalid", str(caught.exception))


class HostSafeWorkflowFieldUnionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        actions, teaching, _, contract_updates = generator.load_workflow_action_contracts()
        cls.projected, _ = generator.project_workflow_action_schema(payload_schema, actions, teaching, contract_updates)
        cls.actions = actions
        cls.teaching = teaching
        cls.contract_updates = contract_updates

    def test_retired_decision_fields_are_absent_and_refused(self):
        for action in self.actions:
            schema = self.projected["$defs"]["work_transition_action_variant_" + action["id"]]
            for field in ("selected_choice", "decision_context_digest"):
                self.assertNotIn(field, schema["properties"], action["id"])
            sample = _schema_sample(schema, self.projected)
            generator.schema_validate(sample, schema, self.projected)
            for field, value in (("selected_choice", "confirm"), ("decision_context_digest", "sha256:" + "a" * 64)):
                with self.assertRaises(ValueError, msg=action["id"] + ":" + field):
                    generator.schema_validate(sample | {field: value}, schema, self.projected)

    def test_optional_extension_subsumes_the_required_base_without_union(self):
        base_schema = {"type": "object", "additionalProperties": False,
                       "required": ["work_id", "version"], "properties": {
                           "work_id": {"type": "string", "minLength": 1},
                           "version": {"type": "integer", "minimum": 1}}}
        extended = copy.deepcopy(base_schema)
        extended["properties"].update({"delivery_artifact": {"type": "string", "minLength": 1},
                                       "delivery_state": {"type": "string", "const": "asserted"}})
        fields = generator.closed_field_union([base_schema, extended], {}, "delivery")
        self.assertNotIn("anyOf", fields)
        self.assertNotIn("oneOf", fields, "the two-field contract is contained in the optional-delivery contract")
        base = {"work_id": "work-1", "version": 1}
        for delivery in ({}, {"delivery_artifact": "artifact:1"}, {"delivery_state": "asserted"},
                         {"delivery_artifact": "artifact:1", "delivery_state": "asserted"}):
            generator.schema_validate(base | delivery, fields, self.projected)
        for bad in ({"work_id": "work-1"}, {"version": 1}, base | {"version": 0},
                    base | {"delivery_state": "pending"}, base | {"delivery_artifact": ""}, base | {"unknown": True}):
            with self.assertRaises(ValueError):
                generator.schema_validate(bad, fields, self.projected)

    def test_retired_published_actions_project_nowhere(self):
        # A retired published action (CD-0218 D1/D2) has no authoring
        # variant after projection, even when the projected document starts
        # from a previous generation that still carried one: the drop is
        # explicit, and the closed core input names only live action IDs.
        projected, _ = generator.project_workflow_action_schema(payload_schema, self.actions, self.teaching, self.contract_updates)
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
        actions, teaching, _, contract_updates = generator.load_workflow_action_contracts()
        cls.projected, _ = generator.project_workflow_action_schema(payload_schema, actions, teaching, contract_updates)

    def validate(self, value, name):
        generator.schema_validate(value, self.projected["$defs"][name], self.projected)

    def test_projection_is_idempotent_and_keeps_native_navigation(self):
        actions, teaching, _, contract_updates = generator.load_workflow_action_contracts()
        repeated, _ = generator.project_workflow_action_schema(self.projected, actions, teaching, contract_updates)
        self.assertEqual(repeated, self.projected)
        generator.check_schema_keywords(self.projected)
        generator.check_payload_closed(self.projected)

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
        self.assertEqual(job_needs(aggregate), ["verify-adapter-rest"])
        self.assertRegex(aggregate, r"(?m)^ {4}if: always\(\)$")
        gate = workflow_job_steps("verify-adapter")
        self.assertEqual(len(gate), 1)
        self.assertEqual(gate[0]["env"], {
            "REST_RESULT": "${{ needs.verify-adapter-rest.result }}",
        })
        self.assertEqual(gate[0]["run"].strip().splitlines(), ['test "$REST_RESULT" = success'])

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
        real_load = lane_checker._load_json

        def broken_fixture(path):
            value = real_load(path)
            if getattr(path, "name", str(path)) == "workflow-engine.v1.json":
                value["scenarios"][0]["scenario_number"] = 0
            return value

        out, err = io.StringIO(), io.StringIO()
        with unittest.mock.patch.object(lane_checker, "_load_json", broken_fixture):
            with redirect_stdout(out), redirect_stderr(err):
                code = lane_checker.main([])
        self.assertEqual(code, 1)
        self.assertIn("schema validation failed", err.getvalue())


class ExternalSuiteRoutingOptionsTests(unittest.TestCase):
    """CON-893: the external-suite options omit exactly one subprocess each.

    CI splits the old verify job so each suite runs as its own step, and the
    nested complete check must not duplicate them. Each option removes only
    its suite subprocess: every other obligation — the selftest, the
    generated checks, the lane validators, the Bun builds, the fixture probe,
    the expected-suite presence check, the host typecheck, and both strict
    failures — still runs and still propagates failure. A default invocation
    runs the complete check with both suites. --skip-host-typecheck omits
    host staging, installation, and compilation, not other checks.
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
        self.assertTrue(any(len(call) > 2 and Path(call[2]).name == "fixture-probe.ts" for call in calls), "the contract fixture probe must still run")
        self.assertTrue(any(call[1] == "x" for call in calls), "the host typecheck must still run")
        self.assertTrue(any(any("generate-agent-contracts.py" in word for word in call) for call in calls))
        self.assertTrue(any(any("generate-agent-profiles.py" in word for word in call) for call in calls))

    def test_schema_checks_need_no_separate_host_probe(self):
        with unittest.mock.patch.dict(os.environ, {"CONCORD_HOST_MERGE_PROBE": "1"}):
            code, calls, _, _ = self._main(["--adapter-tests-external", "--contract-tests-external"])
        self.assertEqual(code, 0)
        self.assertFalse(any("host_schema_probe" in word or "--host-merge" == word for call in calls for word in call))
        self.assertTrue(any(len(call) > 2 and Path(call[2]).name == "fixture-probe.ts" for call in calls))
        self.assertTrue(any(call[1] == "x" for call in calls), "the pinned host typecheck must still run")

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
        cls.actions, cls.teaching, cls.schema_refs, cls.contract_updates = generator.load_workflow_action_contracts()
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
        # Validate each published view against its own closed action union.
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
            # per-kind branches with outcome_kind consts:
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
            # const-free branches.
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
        for condition in _condition_branches(self.defs["work_transition_action_variant_approve_contract"]):
            approve = _fields_object(condition)["properties"]
            field = approve["architecture_binding"]
            original, marker, guidance = field["description"].partition(" Accepted shape:")
            self.assertEqual(original, self.teaching["architecture_domain_rule"])
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


class WorkContextProjectionTests(unittest.TestCase):

    def test_unresolved_context_reference_fails_loudly(self):
        canonical = copy.deepcopy(payload_schema)
        canonical["$defs"]["workflow_law_context"]["properties"]["registry_path"] = {"$ref": "#/$defs/missing_law_member"}
        with self.assertRaisesRegex(ValueError, "missing_law_member"):
            generator.prune_payload_definitions(canonical, {"schemas": {"context": {"ref": "#/$defs/workflow_law_context"}}}, [])

class _WorkloadReached(Exception):
    """Marks that a generator main entered its workload during a reach test."""


class GeneratorCLISafetyTests(unittest.TestCase):
    """Shared CLI contract for the retained generate-* main entry points.

    Help must exit 0 with usage on stdout and perform no main-workload read,
    write, mkdir, or subprocess work. Unrecognized arguments — abbreviations
    of --check, bare positionals, unknown flags, and unknown flags mixed with
    check — must exit 2 with an error on stderr and perform no main work.
    Valid no-argument and --check invocations must still reach the workload.
    """

    # (script name, module, the first workload input the main reads)
    GENERATORS = (
        ("generate-agent-contracts.py", generator, "MANIFEST"),
        ("generate-agent-profiles.py", profiles_generator, "UTILITY_MANIFEST"),
        ("generate-work-kinds.py", work_kinds_generator, "VOCABULARY"),
        ("generate-native-run-statuses.py", native_run_statuses_generator, "VOCABULARY"),
        ("generate-relation-vocabulary.py", relation_vocabulary_generator, "VOCABULARY"),
    )
    REFUSAL_ARGSETS = (
        ("unknown-flag", ["--unknown-option"]),
        ("positional", ["unexpected-positional"]),
        ("check-abbreviation", ["--ch"]),
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
