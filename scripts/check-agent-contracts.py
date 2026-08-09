#!/usr/bin/env python3
"""Run deterministic public contract checks and optional Bun validation."""
from __future__ import annotations
import copy, hashlib, importlib.util, json
import shutil, subprocess, sys, tempfile
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]

_generator_spec = importlib.util.spec_from_file_location("concord_contract_generator", ROOT / "scripts/generate-agent-contracts.py")
if _generator_spec is None or _generator_spec.loader is None:
    raise RuntimeError("unable to load shared JSON Schema validator")
_generator = importlib.util.module_from_spec(_generator_spec)
_generator_spec.loader.exec_module(_generator)
schema_validate = _generator.schema_validate

def _check_closed_schema(path: Path, required: set[str]) -> list[str]:
    findings: list[str] = []
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        return [f"{path.relative_to(ROOT)}: {exc}"]
    if not isinstance(value, dict) or (value.get("type") != "object" and "oneOf" not in value):
        findings.append(f"{path.relative_to(ROOT)}: root must be an object schema or discriminated union")
    if isinstance(value, dict) and value.get("type") == "object" and set(value.get("required", [])) != required:
        findings.append(f"{path.relative_to(ROOT)}: required keys do not match {sorted(required)}")

    def visit(node: object, location: str) -> None:
        if not isinstance(node, dict):
            return
        if node.get("type") == "object" and ("additionalProperties" not in node or node.get("additionalProperties") is True):
            findings.append(f"{path.relative_to(ROOT)}:{location}: object is not closed")
        if node.get("type") == "array":
            if not isinstance(node.get("minItems"), int) or not isinstance(node.get("maxItems"), int):
                findings.append(f"{path.relative_to(ROOT)}:{location}: array lacks explicit bounds")
            elif node["minItems"] < 0 or node["maxItems"] < node["minItems"]:
                findings.append(f"{path.relative_to(ROOT)}:{location}: invalid array bounds")
        if node.get("type") == "string" and "const" not in node and "enum" not in node:
            if not isinstance(node.get("minLength"), int) or not isinstance(node.get("maxLength"), int):
                findings.append(f"{path.relative_to(ROOT)}:{location}: string lacks explicit bounds")
        for key, child in node.items():
            visit(child, f"{location}/{key}")

    visit(value, "$")
    return findings

def _load_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))

def _scenario_error_check(corpus: dict) -> list[str]:
    envelope = _load_json(ROOT / "contracts/agent-tool-envelope.schema.json")
    current = set(envelope["$defs"]["typedError"]["properties"]["kind"]["enum"])
    manifest = _load_json(ROOT / "contracts/agent-tool-surface.v1.json")
    current_surface = manifest["surface"]["version"]
    pending = {item["error_kind"] for item in corpus["pending_amendments"]}
    findings: list[str] = []
    if corpus["surface_version"] != current_surface:
        findings.append(f"corpus surface version {corpus['surface_version']} does not match current manifest {current_surface}")
    if corpus["engine_status"] == "engine_shipped" and pending:
        findings.append("pending amendments remain after engine shipped")
    if current & pending:
        findings.append("pending amendment is already in current envelope")
    expected: set[str] = set()
    def walk(node: object) -> None:
        if isinstance(node, dict):
            if node.get("target") == "communication" and node.get("path") == "error.kind" and node.get("op") == "eq" and isinstance(node.get("value"), str):
                expected.add(node["value"])
            for child in node.values(): walk(child)
        elif isinstance(node, list):
            for child in node: walk(child)
    walk(corpus["scenarios"])
    undeclared = expected - current - pending
    if undeclared:
        findings.append(f"undeclared scenario error kind(s): {sorted(undeclared)}")
    return findings

def _derive_notice_id(fields: list[tuple[str, str]]) -> str:
    body = "".join(f"{name}={len(value.encode('utf-8'))}:{value}|" for name, value in fields)
    return "notice:" + hashlib.sha256(("notice-v1\0" + body).encode("utf-8")).hexdigest()

def _notice_oracle_check(corpus: dict) -> list[str]:
    scenario = next(item for item in corpus["scenarios"] if item["id"] == "WF29-impact-notice-identity")
    state = scenario["initial_state"]
    expected = next(assertion["value"] for assertion in scenario["expected"]["assertions"] if assertion["path"] == "impact_notices.0.notice_id")
    fields = [("source_work_id", state["work"]), ("source_contract_version", str(state["source_contract_version"])), ("entity_kind", state["entity_kind"]), ("entity_ref", state["entity_ref"]), ("target_work_id", state["target_work_id"]), ("severity", state["severity"])]
    derived = _derive_notice_id(fields)
    return [] if expected == derived else [f"notice-ID derivation mismatch: expected {derived}, corpus has {expected}"]

def _derive_actor_ref(fields: list[tuple[str, str]], separator: str = "\0") -> str:
    body = "".join(f"{name}={len(value.encode('utf-8'))}:{value}|" for name, value in fields)
    return "actor:" + hashlib.sha256(("actor-v1" + separator + body).encode("utf-8")).hexdigest()

def _actor_oracle_check(corpus: dict, fixtures: dict) -> list[str]:
    findings: list[str] = []
    derived: dict[tuple[str, str, str, str], str] = {}
    for actor in corpus["fixtures"]["actors"]:
        fields = tuple(actor[key] for key in ("principal_ref", "client_ref", "agent_ref", "session_ref"))
        expected = _derive_actor_ref(list(zip(("principal_ref", "client_ref", "agent_ref", "session_ref"), fields)))
        derived[fields] = expected
        if actor["actor_ref"] != expected:
            findings.append(f"actor-ID derivation mismatch: expected {expected}, corpus has {actor['actor_ref']}")
    executor = ("principal:operator", "client:concord-1", "agent-engineer", "session-executor")
    reviewer = ("principal:operator", "client:concord-1", "agent-reviewer", "session-reviewer")
    expected_executor = derived[executor]
    expected_reviewer = derived[reviewer]
    for scenario_id in ("WF12-self-authored-check", "WF13-verdict-actor-distinctness"):
        scenario = next(item for item in corpus["scenarios"] if item["id"] == scenario_id)
        if scenario["initial_state"].get("executor") != expected_executor:
            findings.append(f"actor-ID derivation mismatch: {scenario_id}.executor")
        if scenario_id == "WF12-self-authored-check" and scenario["initial_state"].get("requested_check_author") != expected_executor:
            findings.append(f"actor-ID derivation mismatch: {scenario_id}.requested_check_author")
        if scenario_id == "WF13-verdict-actor-distinctness":
            same_actor = next(case for case in scenario["cases"] if case["id"] == "WF13-same-actor")
            if same_actor["initial_state"].get("verdict_actor") != expected_executor:
                findings.append(f"actor-ID derivation mismatch: {same_actor['id']}.verdict_actor")
    outcome = next(case for case in fixtures["cases"] if case["id"] == "outcome-spike-record-valid")
    if outcome["instance"]["decision_record"]["reviewer_actor_ref"] != expected_reviewer:
        findings.append(f"actor-ID derivation mismatch: expected {expected_reviewer}, outcome reviewer differs")
    return findings

def _derive_definition_digest(definition: dict) -> str:
    body = copy.deepcopy(definition)
    body.pop("digest", None)
    return "sha256:" + hashlib.sha256(json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")).hexdigest()

def _validate_instance(schema_path: Path, value: object) -> str | None:
    schema = _load_json(schema_path)
    try:
        schema_validate(value, schema, schema, "instance")
    except ValueError as exc:
        return str(exc)
    return None

def _validate_definition_semantics(definition: dict) -> str | None:
    graph = definition["step_graph"]
    step_ids = [step["id"] for step in graph["steps"]]
    if len(step_ids) != len(set(step_ids)):
        return "duplicate step ID"
    step_set = set(step_ids)
    if graph["start_step"] not in step_set:
        return "graph start endpoint"
    if any(step not in step_set for step in graph["terminal_steps"]):
        return "graph terminal endpoint"
    action_ids = set(definition["available_actions"])
    action_definition_ids = [action["id"] for action in definition["action_definitions"]]
    if len(action_definition_ids) != len(set(action_definition_ids)):
        return "duplicate action ID"
    if action_ids != set(action_definition_ids):
        return "root action definitions do not match available actions"
    for step in graph["steps"]:
        if not set(step["actions"]).issubset(action_ids):
            return "step action not declared at root"
    for edge in graph["edges"]:
        if edge["from"] not in step_set or edge["to"] not in step_set:
            return "graph edge endpoint"
    adjacency = {step: [] for step in step_ids}
    for edge in graph["edges"]:
        if edge["kind"] != "retry":
            adjacency[edge["from"]].append(edge["to"])
    visiting: set[str] = set()
    visited: set[str] = set()
    def walk(node: str) -> bool:
        if node in visiting:
            return True
        if node in visited:
            return False
        visiting.add(node)
        if any(walk(child) for child in adjacency[node]):
            return True
        visiting.remove(node)
        visited.add(node)
        return False
    if any(walk(step) for step in step_ids):
        return "non-retry graph cycle"
    for action in definition["action_definitions"]:
        names = [field["name"] for field in action["payload"]["fields"]]
        if len(names) != len(set(names)):
            return "duplicate action payload field"
    if definition.get("digest") != _derive_definition_digest(definition):
        return "definition digest mismatch"
    return None

def _validate_outcome_semantics(work_kind: str, outcome: dict) -> str | None:
    if outcome.get("kind") != "outcome":
        return None
    tokens = set(outcome.get("allowed", []))
    spike_tokens = {"accepted_decision", "insufficient_evidence"}
    if work_kind == "architecture_spike" and tokens & spike_tokens and "decision_record" not in outcome:
        return "decision record required for architecture spike"
    if work_kind == "research" and tokens & spike_tokens:
        return "research cannot allow spike-only outcome"
    return None

def _validate_predicate_against_family(work_kind: str, predicate: dict) -> str | None:
    rules = {
        "implementation": ("check", {"exists", "absent", "check"}, set(), False),
        "break_fix": ("absent", {"exists", "absent", "check"}, set(), False),
        "research": ("outcome", {"outcome"}, {"no_change", "resolved", "report_recorded"}, False),
        "architecture_spike": ("outcome", {"outcome"}, {"accepted_decision", "insufficient_evidence"}, True),
        "ops_runbook": ("check", {"exists", "absent", "check"}, set(), False),
        "static_analysis": ("check", {"exists", "absent", "check"}, set(), False),
        "generic_one_off": ("outcome", {"exists", "absent", "outcome", "check"}, {"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, False),
    }
    _, allowed_kinds, allowed_tokens, requires_record = rules[work_kind]
    kind = predicate.get("kind")
    if kind not in allowed_kinds:
        return "predicate kind not allowed"
    if kind == "outcome":
        tokens = set(predicate.get("allowed", []))
        if not tokens.issubset(allowed_tokens):
            return "outcome token not allowed"
        if requires_record and tokens & {"accepted_decision", "insufficient_evidence"} and "decision_record" not in predicate:
            return "decision record required"
    return None

def check_workflow_contracts() -> list[str]:
    findings = []
    for schema_path in (ROOT / "contracts/workflow-definition.schema.json", ROOT / "contracts/workflow-outcome.schema.json", ROOT / "contracts/workflow-engine-scenarios.schema.json"):
        try:
            _generator.check_schema_keywords(_load_json(schema_path), str(schema_path.relative_to(ROOT)))
        except (OSError, json.JSONDecodeError, ValueError) as exc:
            findings.append(f"{schema_path.relative_to(ROOT)}: unsupported or invalid schema keyword: {exc}")
    findings += _check_closed_schema(ROOT / "contracts/workflow-definition.schema.json", {
        "schema_version", "ref", "version", "digest", "work_kind", "step_graph",
        "available_actions", "action_definitions", "required_evidence_kinds", "outcome_schema", "rigor_rules",
        "staleness_rules", "composition_rules",
    })
    outcome = ROOT / "contracts/workflow-outcome.schema.json"
    findings += _check_closed_schema(outcome, set())
    scenario_schema = ROOT / "contracts/workflow-engine-scenarios.schema.json"
    findings += _check_closed_schema(scenario_schema, {"$schema", "schema_version", "contract", "contract_status", "surface_version", "engine_status", "pending_amendments", "fixtures", "assertion_contract", "runner_requirements", "scenarios"})
    fixture_path = ROOT / "contracts/workflow-engine.fixtures.json"
    try:
        corpus = _load_json(ROOT / "scenarios/workflow-engine.v1.json")
        corpus_error = _validate_instance(scenario_schema, corpus)
        if corpus_error:
            findings.append(f"scenarios/workflow-engine.v1.json: schema validation failed: {corpus_error}")
        fixtures = _load_json(fixture_path)
        fixture_by_id = {case["id"]: case for case in fixtures["cases"]}
        for case in fixtures["cases"]:
            schema_path = ROOT / "contracts" / case["schema"]
            error = _validate_instance(schema_path, case["instance"])
            if error is None and case.get("work_kind"):
                error = _validate_outcome_semantics(case["work_kind"], case["instance"])
            if case["schema"] == "workflow-definition.schema.json" and error is None:
                error = _validate_definition_semantics(case["instance"])
            if case["valid"] and error:
                findings.append(f"{case['id']}: expected valid instance: {error}")
            if not case["valid"] and (error is None or case.get("expected_error") not in error):
                findings.append(f"{case['id']}: wrong validation reason: {error}")
            if case["schema"] == "workflow-definition.schema.json" and case["valid"]:
                semantic_error = _validate_definition_semantics(case["instance"])
                if semantic_error:
                    findings.append(f"{case['id']}: semantic validation failed: {semantic_error}")
        for case in fixtures.get("predicate_negative_cases", []):
            predicate_error = _validate_predicate_against_family(case["work_kind"], case["predicate"])
            if predicate_error is None or case["expected_error"] not in predicate_error:
                findings.append(f"{case['id']}: wrong predicate validation reason: {predicate_error}")
        base_definition = copy.deepcopy(fixture_by_id["definition-implementation-valid"]["instance"])
        for case in fixtures.get("semantic_negative_cases", []):
            mutated = copy.deepcopy(base_definition)
            mutation = case["mutation"]
            if mutation == "start_step":
                mutated["step_graph"]["start_step"] = "missing"
            elif mutation == "terminal_step":
                mutated["step_graph"]["terminal_steps"] = ["missing"]
            elif mutation == "edge_endpoint":
                mutated["step_graph"]["edges"][0]["to"] = "missing"
            elif mutation == "non_retry_cycle":
                mutated["step_graph"]["edges"].append({"from": "design", "to": "proposal", "kind": "forward"})
            elif mutation == "step_action":
                mutated["step_graph"]["steps"][0]["actions"] = ["ghost_action"]
            elif mutation == "root_action":
                mutated["available_actions"].append("ghost_action")
            elif mutation == "definition_digest":
                mutated["digest"] = "sha256:" + ("0" * 64)
            semantic_error = _validate_definition_semantics(mutated)
            if semantic_error is None or case["expected_error"] not in semantic_error:
                findings.append(f"{case['id']}: wrong semantic validation reason: {semantic_error}")
        negative_scenario = copy.deepcopy(corpus)
        negative_scenario["scenarios"][0]["scenario_number"] = 0
        negative_error = _validate_instance(scenario_schema, negative_scenario)
        if negative_error is None or "minimum" not in negative_error:
            findings.append(f"scenario-negative-number: wrong validation reason: {negative_error}")
        error_findings = _scenario_error_check(corpus)
        findings.extend(error_findings)
        findings.extend(_notice_oracle_check(corpus))
        findings.extend(_actor_oracle_check(corpus, fixtures))
        for case in fixtures.get("scenario_negative_cases", []):
            mutated = copy.deepcopy(corpus)
            if case["mutation"] == "actor_ref":
                mutated["fixtures"]["actors"][0]["actor_ref"] = "actor:" + ("g" * 64)
            elif case["mutation"] == "invalid_float":
                mutated["scenarios"][0]["initial_state"]["invalid_float"] = 1.5
            elif case["mutation"] == "unknown_error_kind":
                mutated["scenarios"][1]["expected"]["assertions"][1]["value"] = "not_a_real_error_kind"
            elif case["mutation"] == "engine_shipped_pending":
                mutated["engine_status"] = "engine_shipped"
            elif case["mutation"] == "wrong_notice_id":
                for assertion in mutated["scenarios"][28]["expected"]["assertions"]:
                    if assertion.get("path") == "impact_notices.0.notice_id":
                        assertion["value"] = "notice:0000000000000000000000000000000000000000000000000000000000000000"
            elif case["mutation"] == "literal_backslash_zero_actor":
                actor = mutated["fixtures"]["actors"][0]
                fields = [(key, actor[key]) for key in ("principal_ref", "client_ref", "agent_ref", "session_ref")]
                actor["actor_ref"] = _derive_actor_ref(fields, separator="\\0")
            scenario_error = _validate_instance(scenario_schema, mutated)
            if case["mutation"] in {"actor_ref", "invalid_float"}:
                if scenario_error is None or case["expected_error"] not in scenario_error:
                    findings.append(f"{case['id']}: wrong schema validation reason: {scenario_error}")
            elif scenario_error is not None:
                findings.append(f"{case['id']}: mutation should reach semantic checks, got schema error: {scenario_error}")
            else:
                semantic_findings = _scenario_error_check(mutated) + _notice_oracle_check(mutated) + _actor_oracle_check(mutated, fixtures)
                if not any(case["expected_error"] in finding for finding in semantic_findings):
                    findings.append(f"{case['id']}: wrong validation reason: {semantic_findings}")
        contract = corpus["assertion_contract"]
        scenarios = corpus["scenarios"]
        if corpus.get("contract") != "CD-0013" or corpus.get("contract_status") != "accepted":
            findings.append("scenarios/workflow-engine.v1.json: contract metadata is not accepted CD-0013")
        if len(scenarios) != 47:
            findings.append(f"scenarios/workflow-engine.v1.json: expected 47 scenarios, got {len(scenarios)}")
        ids = [item.get("id") for item in scenarios]
        numbers = [item.get("scenario_number") for item in scenarios]
        if len(set(ids)) != len(ids) or any(not isinstance(item, str) or not item for item in ids):
            findings.append("scenarios/workflow-engine.v1.json: scenario IDs must be unique and nonempty")
        if numbers != list(range(1, 48)):
            findings.append("scenarios/workflow-engine.v1.json: scenario numbers must be ordered 1..47")
        targets = set(contract.get("targets", []))
        ops = set(contract.get("ops", []))
        nested_ids: list[str] = []
        for item in scenarios:
            if not item.get("mechanism") or ("expected" not in item and "cases" not in item):
                findings.append(f"scenarios/workflow-engine.v1.json: incomplete scenario {item.get('id')}")
            assertion_groups = [item.get("expected", {}).get("assertions", [])]
            assertion_groups.extend(case.get("expected", {}).get("assertions", []) for case in item.get("cases", []))
            for assertions in assertion_groups:
                if not assertions:
                    continue
                durable = any(assertion.get("target") in {"effects", "authority"} or (assertion.get("target") == "state" and any(token in assertion.get("path", "") for token in ("projection", "event", "row"))) for assertion in assertions)
                if len(assertions) < 2 or len({assertion.get("target") for assertion in assertions}) < 2 or not {assertion.get("target") for assertion in assertions} & {"state", "effects", "authority"} or not durable:
                    findings.append(f"scenarios/workflow-engine.v1.json: stub-weak assertion set in {item.get('id')}")
                for assertion in assertions:
                    if assertion.get("target") not in targets or assertion.get("op") not in ops:
                        findings.append(f"scenarios/workflow-engine.v1.json: closed assertion violation in {item.get('id')}")
            for case in item.get("cases", []):
                nested_ids.append(case.get("id"))
                for assertion in case.get("expected", {}).get("assertions", []):
                    if assertion.get("target") not in targets or assertion.get("op") not in ops:
                        findings.append(f"scenarios/workflow-engine.v1.json: closed case assertion violation in {case.get('id')}")
        all_ids = ids + nested_ids
        if len(set(all_ids)) != len(all_ids) or any(not isinstance(item, str) or not item for item in all_ids):
            findings.append("scenarios/workflow-engine.v1.json: all scenario and case IDs must be unique and nonempty")
    except (OSError, json.JSONDecodeError, KeyError, TypeError) as exc:
        findings.append(f"scenarios/workflow-engine.v1.json: structural validation failed: {exc}")
    return findings

def main() -> int:
    workflow_findings = check_workflow_contracts()
    if workflow_findings:
        for finding in workflow_findings:
            print(finding, file=sys.stderr)
        return 1
    tamper = subprocess.run([sys.executable, str(ROOT / "scripts/test-agent-contracts.py")], cwd=ROOT)
    if tamper.returncode: return tamper.returncode
    generated = subprocess.run([sys.executable, str(ROOT / "scripts/generate-agent-contracts.py"), "--check"], cwd=ROOT)
    if generated.returncode: return generated.returncode
    bun = shutil.which("bun")
    if bun:
        with tempfile.TemporaryDirectory() as out:
            checked = subprocess.run([bun, "build", "adapter/opencode/generated-contracts.ts", "adapter/opencode/generated-contract-tests.ts", "--outdir", out], cwd=ROOT)
            if checked.returncode: return checked.returncode
            probe = Path(out) / "fixture-probe.ts"
            probe.write_text(f'''import {{ validateGeneratedPayload }} from {str(ROOT / "adapter/opencode/generated-contract-tests.ts").replace(chr(92), "/").__repr__()};
import corpus from {str(ROOT / "contracts/agent-tool-surface.fixtures.json").replace(chr(92), "/").__repr__()} with {{ type: "json" }};
for (const fixture of corpus.fixtures) {{ if (!validateGeneratedPayload(fixture.input_schema, fixture.input_valid) || fixture.input_invalid_cases.some((value: unknown) => validateGeneratedPayload(fixture.input_schema, value)) || !validateGeneratedPayload(fixture.result_schema, fixture.result_valid) || fixture.result_invalid_cases.some((value: unknown) => validateGeneratedPayload(fixture.result_schema, value))) throw new Error(`fixture failed: ${{fixture.operation}}`); }}
''')
            checked = subprocess.run([bun, "run", str(probe)], cwd=ROOT)
            if checked.returncode: return checked.returncode
            adapter = subprocess.run([bun, "build", "adapter/opencode/concord.ts", "--outdir", out, "--external", "@opencode-ai/plugin", "--target", "bun"], cwd=ROOT)
            if adapter.returncode: return adapter.returncode
            adapter_tests = subprocess.run([bun, "test", "adapter/opencode/concord.test.ts"], cwd=ROOT)
            if adapter_tests.returncode: return adapter_tests.returncode
            source = (ROOT / "adapter/opencode/concord.ts").read_text(encoding="utf-8")
            exports = re.findall(r"export const ([A-Za-z_][A-Za-z0-9_]*) = tool\(", source)
            if exports != ["product_view", "work_browse", "work_trace", "knowledge", "work_define", "work_transition", "work_relate", "work_compact"]:
                print(f"adapter export drift: {exports}", file=sys.stderr); return 1
        print("agent contract check passed (Bun syntax/build)")
    else:
        for path in (ROOT / "adapter/opencode/generated-contracts.ts", ROOT / "adapter/opencode/generated-contract-tests.ts"):
            text = path.read_text(encoding="utf-8")
            if "Code generated by scripts/generate-agent-contracts.py" not in text or "manifestDigest" not in text and path.name == "generated-contracts.ts":
                print(f"generated TypeScript marker missing: {path}", file=sys.stderr); return 1
        print("agent contract check passed (deterministic TypeScript fallback; Bun unavailable)")
    return 0
if __name__ == "__main__": raise SystemExit(main())
