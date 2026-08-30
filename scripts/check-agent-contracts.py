#!/usr/bin/env python3
"""Run deterministic public contract checks and optional Bun validation, including the adapter typecheck."""
from __future__ import annotations
import copy, hashlib, importlib.util, json, os
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

# The installer owns which adapter files ship. Import that list rather than
# restating it: a restated copy drifts, which is how an adapter shipped an
# import to a module the installer never placed.
_installer_spec = importlib.util.spec_from_file_location("concord_installer", ROOT / "scripts/install.py")
if _installer_spec is None or _installer_spec.loader is None:
    raise RuntimeError("unable to load the installer adapter file list")
_installer = importlib.util.module_from_spec(_installer_spec)
# Registered before execution because the installer defines dataclasses, whose
# field resolution looks the defining module up in sys.modules.
sys.modules[_installer_spec.name] = _installer
_installer_spec.loader.exec_module(_installer)
ADAPTER_FILES = _installer.ADAPTER_FILES

HOST_PIN = ROOT / "docs/adapter-host-pin.v1.json"
HOST_PIN_SCHEMA = ROOT / "contracts/adapter-host-pin.schema.json"
DIAGNOSTIC = re.compile(r"^(?P<path>[^(\n]+)\((?P<line>\d+),(?P<column>\d+)\): error (?P<code>TS\d+):")


def load_host_pin(findings: list[str]) -> dict | None:
    """Return the validated host pin, or None with findings appended."""
    try:
        pin = json.loads(HOST_PIN.read_text(encoding="utf-8"))
        schema = json.loads(HOST_PIN_SCHEMA.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        findings.append(f"adapter host pin unreadable: {exc}")
        return None
    return validate_host_pin(pin, schema, findings)


def validate_host_pin(pin: object, schema: dict, findings: list[str]) -> dict | None:
    """Validate the pin against its contract and its own cross-field rules.

    The schema carries the shape. What it cannot carry is agreement between two
    of its own fields: a `types` entry names an ambient package the compiler
    loads globally, and one that is not in `packages` would resolve from
    whatever the staged install happened to pull in transitively. That is the
    unpinned surface this manifest exists to remove, so it is rejected here.
    """
    try:
        schema_validate(pin, schema, schema)
    except ValueError as exc:
        findings.append(f"adapter host pin does not satisfy its contract: {exc}")
        return None
    if not isinstance(pin, dict):
        findings.append("adapter host pin is not an object")
        return None

    pinned = {package["name"] for package in pin["packages"]}
    for name in pin["compiler_options"]["types"]:
        if name not in pinned:
            findings.append(f"adapter host pin: types entry '{name}' names no pinned package")
    for allowance in pin["allowances"]:
        outstanding = allowance["state"] == "outstanding"
        if outstanding and "issue" not in allowance:
            findings.append(f"adapter host pin: outstanding allowance {allowance['file']} {allowance['code']} carries no tracking issue")
        if not outstanding and "issue" in allowance:
            findings.append(f"adapter host pin: {allowance['state']} allowance {allowance['file']} {allowance['code']} must not carry an issue")
    runtime_version = pin["runtime_probe"]["host_version"]
    plugin_versions = [package["version"] for package in pin["packages"] if package["name"] == "@opencode-ai/plugin"]
    if plugin_versions and runtime_version != plugin_versions[0]:
        findings.append(f"adapter host pin: runtime probe version does not match @opencode-ai/plugin ({runtime_version} != {plugin_versions[0]})")
    return None if findings else pin


def stage_host_workspace(bun: str, pin: dict, workspace: Path) -> str | None:
    """Install the pinned declarations and stage the sources beside them.

    Returns an error string when the install cannot be completed. The sources
    are copied rather than typechecked in place because the published packages
    must resolve from a directory that is not the repository: installing them
    into the working tree would put an unversioned node_modules inside the
    adapter, and pointing the compiler at the repository from outside it
    resolves neither the packages nor the adapter's JSON imports.
    """
    source = workspace / "src"
    source.mkdir(parents=True)
    for directory in pin["sources"]:
        origin = ROOT / directory
        if not origin.is_dir():
            return f"pinned source directory is missing: {directory}"
        for path in sorted(origin.glob("*.ts")) + sorted(origin.glob("*.json")):
            shutil.copy2(path, source / path.name)

    (workspace / "package.json").write_text(json.dumps({"name": "concord-adapter-host-typecheck", "private": True}), encoding="utf-8")
    (workspace / "tsconfig.json").write_text(json.dumps({"compilerOptions": pin["compiler_options"], "include": ["src/*.ts"]}), encoding="utf-8")

    # --exact refuses bun's default caret range. A range would reintroduce the
    # drift this manifest removes: the reviewed declarations and the installed
    # ones would coincide only until the next publish.
    specifiers = [f"{package['name']}@{package['version']}" for package in pin["packages"]]
    try:
        installed = subprocess.run([bun, "add", "--exact", *specifiers], cwd=workspace, capture_output=True, text=True, timeout=300)
    except (OSError, subprocess.SubprocessError) as exc:
        return f"host declarations could not be installed: {exc}"
    if installed.returncode:
        return f"host declarations could not be installed: {(installed.stderr or installed.stdout).strip()[:600]}"
    return None


def reconcile_diagnostics(output: str, allowances: list[dict], findings: list[str], exit_code: int = 0) -> None:
    """Subtract the recorded divergences and report whatever is left.

    An allowance is matched by file and diagnostic code rather than by position,
    so an unrelated edit above it does not invalidate it. The count must match
    exactly in both directions: a divergence that spread is a regression, and
    one that shrank means the manifest overstates what the adapter owes.

    A diagnostic the parser does not recognise is a finding rather than a
    silence. The compiler reports some failures without a source position at
    all - no inputs matched, a `types` entry it could not resolve - and those
    are precisely the failures that would otherwise leave the check reporting
    success over a typecheck that examined nothing.
    """
    observed: dict[tuple[str, str], int] = {}
    unattributed = 0
    for line in output.splitlines():
        matched = DIAGNOSTIC.match(line)
        if matched:
            key = (Path(matched.group("path")).name, matched.group("code"))
            observed[key] = observed.get(key, 0) + 1
        elif re.search(r"\berror TS\d+:", line):
            unattributed += 1
            findings.append(f"typecheck reported a diagnostic with no source position: {line.strip()[:200]}")

    if exit_code and not observed and not unattributed:
        findings.append(f"typecheck exited {exit_code} without reporting a diagnostic: {output.strip()[:400] or 'no output'}")

    for allowance in allowances:
        key = (allowance["file"], allowance["code"])
        actual = observed.pop(key, 0)
        if actual == allowance["count"]:
            continue
        if actual == 0:
            findings.append(f"stale allowance: {key[0]} no longer emits {key[1]}; the allowance is spent and should be deleted")
        else:
            findings.append(f"allowance drift: {key[0]} emits {actual} {key[1]} diagnostic(s), the manifest records {allowance['count']}")

    for (name, code), count in sorted(observed.items()):
        findings.append(f"adapter does not satisfy the pinned host declarations: {name} emits {count} unrecorded {code} diagnostic(s)")

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
    pending = {item["error_kind"] for item in corpus["pending_amendments"]}
    findings: list[str] = []
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

def _structured_scenario_check(corpus: dict) -> list[str]:
    """Validate executable fixture vocabulary beyond JSON shape."""
    findings: list[str] = []
    fixture = corpus["fixtures"]
    work_ids = {item["id"] for item in fixture["work_items"]}
    actor_refs = {item["actor_ref"] for item in fixture["actors"]}
    definition_refs = {item["ref"] for item in fixture["definitions"]}
    evidence_ids = {item["id"] for item in fixture["evidence"]}
    relation_ids = {item["id"] for item in fixture["relations"]}
    required = {
        "capture": {"payload", "title", "value_statement", "project_ids"},
        "approve_contract": {"payload", "contract_version", "premise", "outcome", "route_conventions"},
        "revise_candidates": {"payload", "contract_version", "candidate_kind", "candidate_refs", "added", "removed"},
        "supersede_contract": {"payload", "new_contract_version", "supersede_reason", "audit_evidence"},
        "replace_outcome": {"payload", "outcome", "evidence_refs"},
        "link_successor": {"payload", "successor_work_id", "relation", "relation_data"},
        "complete_external_step": {"payload", "operation_id", "attempt_epoch", "current_claim"},
        "rebuild_after_interrupt": {"payload", "operation_id", "event_stream"},
        "retry_same_action": {"payload", "operation_id"},
        "takeover_attempt": {"payload", "operation_id", "current_claim"},
        "resolve_conditions": {"payload", "condition_id", "resolver_authority", "resolution_evidence"},
        "explicit_resolve": {"payload", "condition_id", "resolver_authority", "resolution_evidence"},
        "start_downstream": {"payload", "successor_work_id", "relation"},
        "link_and_complete": {"payload", "successor_work_id", "relation"},
        "rebuild": {"payload", "event_stream"}, "reconstruct_subject": {"payload", "event_stream"},
        "start_execution": {"payload"}, "workflow_action": {"payload"},
        "concurrent_reads_and_writes": {"payload"}, "repair_and_rebuild": {"payload", "event_stream"},
        "replay": {"payload", "event_stream"},
    }
    def walk(item: dict) -> None:
        action = item.get("action")
        request = item.get("request") or {}
        setup = item.get("setup") or {}
        fields = request.get("fields") or {}
        missing = required.get(action, {"payload"}) - set(fields)
        if missing:
            findings.append(f"{item.get('id')}: mandatory structured request field(s) missing: {sorted(missing)}")
        if request.get("actor_ref") not in actor_refs:
            findings.append(f"{item.get('id')}: dangling fixture reference actor {request.get('actor_ref')}")
        if (request.get("definition_pin") or {}).get("ref") not in definition_refs:
            findings.append(f"{item.get('id')}: dangling fixture reference definition pin")
        approval_evidence = set((request.get("approval") or {}).get("evidence_refs", []))
        operation_evidence = set((request.get("operation") or {}).get("evidence_refs", []))
        if not approval_evidence <= evidence_ids or not operation_evidence <= evidence_ids:
            findings.append(f"{item.get('id')}: dangling fixture reference request evidence")
        refs = setup.get("fixture_refs") or {}
        if refs.get("work_item") not in work_ids:
            findings.append(f"{item.get('id')}: dangling fixture reference work {refs.get('work_item')}")
        if not set(refs.get("definitions", [])) <= definition_refs:
            findings.append(f"{item.get('id')}: dangling fixture reference definition")
        if not set(refs.get("evidence", [])) <= evidence_ids:
            findings.append(f"{item.get('id')}: dangling fixture reference evidence")
        if not set(refs.get("relations", [])) <= relation_ids:
            findings.append(f"{item.get('id')}: dangling fixture reference relation")
        for event in setup.get("event_history", []):
            if event.get("actor_ref") not in actor_refs or event.get("work_id") not in work_ids:
                findings.append(f"{item.get('id')}: dangling fixture reference event identity")
        fault_kinds = {"none", "commit_after_verdict_fails", "unreadable_authority", "removed_authority", "projection_corruption", "event_poison", "newer_event_version", "missing_registry", "stale_attempt", "mismatched_commit"}
        for fault in setup.get("faults", []):
            if fault.get("kind") not in fault_kinds:
                findings.append(f"{item.get('id')}: unknown structured fault kind {fault.get('kind')}")
        for observation in (item.get("observations") or {}).get("expected_reads", []):
            if observation.get("op") not in {"eq", "not_eq", "contains", "absent", "nonempty"}:
                findings.append(f"{item.get('id')}: unknown structured observation op {observation.get('op')}")
        for case in item.get("cases", []):
            walk(case)
    for item in corpus["scenarios"]:
        walk(item)
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
        "staleness_rules", "composition_rules", "evaluator_independence",
    })
    outcome = ROOT / "contracts/workflow-outcome.schema.json"
    findings += _check_closed_schema(outcome, set())
    scenario_schema = ROOT / "contracts/workflow-engine-scenarios.schema.json"
    findings += _check_closed_schema(scenario_schema, {"$schema", "schema_version", "contract", "contract_status", "engine_status", "pending_amendments", "fixtures", "assertion_contract", "runner_requirements", "scenarios"})
    fixture_path = ROOT / "contracts/workflow-engine.fixtures.json"
    try:
        corpus = _load_json(ROOT / "scenarios/workflow-engine.v1.json")
        corpus_error = _validate_instance(scenario_schema, corpus)
        if corpus_error:
            findings.append(f"scenarios/workflow-engine.v1.json: schema validation failed: {corpus_error}")
        findings.extend(_structured_scenario_check(corpus))
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
            elif case["mutation"] == "removed_surface_version_rejection":
                # Rejection test: removed agent-surface metadata must remain unknown.
                mutated["surface_version"] = "5.0.0"
            elif case["mutation"] == "wrong_notice_id":
                for assertion in mutated["scenarios"][28]["expected"]["assertions"]:
                    if assertion.get("path") == "impact_notices.0.notice_id":
                        assertion["value"] = "notice:0000000000000000000000000000000000000000000000000000000000000000"
            elif case["mutation"] == "literal_backslash_zero_actor":
                actor = mutated["fixtures"]["actors"][0]
                fields = [(key, actor[key]) for key in ("principal_ref", "client_ref", "agent_ref", "session_ref")]
                actor["actor_ref"] = _derive_actor_ref(fields, separator="\\0")
            scenario_error = _validate_instance(scenario_schema, mutated)
            if case["mutation"] in {"actor_ref", "invalid_float", "removed_surface_version_rejection"}:
                if scenario_error is None or case["expected_error"] not in scenario_error:
                    findings.append(f"{case['id']}: wrong schema validation reason: {scenario_error}")
            elif scenario_error is not None:
                findings.append(f"{case['id']}: mutation should reach semantic checks, got schema error: {scenario_error}")
            else:
                semantic_findings = _scenario_error_check(mutated) + _notice_oracle_check(mutated) + _actor_oracle_check(mutated, fixtures)
                if not any(case["expected_error"] in finding for finding in semantic_findings):
                    findings.append(f"{case['id']}: wrong validation reason: {semantic_findings}")
        for case in fixtures.get("structured_scenario_negative_cases", []):
            mutated = copy.deepcopy(corpus)
            scenario = mutated["scenarios"][0]
            if case["mutation"] == "missing_payload":
                del scenario["request"]["fields"]["payload"]
            elif case["mutation"] == "unknown_request_field":
                scenario["request"]["fields"]["unknown_field"] = True
            elif case["mutation"] == "dangling_fixture_ref":
                scenario["setup"]["fixture_refs"]["evidence"] = ["evidence-does-not-exist"]
            elif case["mutation"] == "unknown_fault_kind":
                scenario["setup"]["faults"] = [{"kind": "not-a-fault", "input": {}}]
            elif case["mutation"] == "unknown_observation_op":
                scenario["observations"]["expected_reads"][0]["op"] = "not-an-observation-op"
            schema_error = _validate_instance(scenario_schema, mutated)
            semantic_findings = _structured_scenario_check(mutated)
            if case["mutation"] in {"missing_payload", "unknown_request_field", "unknown_fault_kind", "unknown_observation_op"}:
                if schema_error is None:
                    findings.append(f"{case['id']}: schema accepted malformed structured fixture")
            elif not any(case["expected_error"] in finding for finding in semantic_findings):
                findings.append(f"{case['id']}: wrong validation reason: {semantic_findings}")
        contract = corpus["assertion_contract"]
        scenarios = corpus["scenarios"]
        if corpus.get("contract") != "CD-0013" or corpus.get("contract_status") != "accepted":
            findings.append("scenarios/workflow-engine.v1.json: contract metadata is not accepted CD-0013")
        if len(scenarios) != 48:
            findings.append(f"scenarios/workflow-engine.v1.json: expected 48 scenarios, got {len(scenarios)}")
        ids = [item.get("id") for item in scenarios]
        numbers = [item.get("scenario_number") for item in scenarios]
        if len(set(ids)) != len(ids) or any(not isinstance(item, str) or not item for item in ids):
            findings.append("scenarios/workflow-engine.v1.json: scenario IDs must be unique and nonempty")
        if numbers != list(range(1, 49)):
            findings.append("scenarios/workflow-engine.v1.json: scenario numbers must be ordered 1..48")
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

def _check_cd0043_lane_methodology() -> list[str]:
    """Prove the shipped artifact carries no lane methodology (CD-0043 D1/D3).

    CD-0043 made three repository facts decided state rather than accident:
    the lane packet has no methodology property (a rejected alternative, kept
    rejected), the lane registry version stays at the version the decision
    preserved, and `skills/` ships as README-only emptiness. Until #461 no
    check asserted any of them, so the first drifted edit would have passed
    silently. Methodology reaching a worker is host instructions through
    `CONCORD_HOST_INSTRUCTIONS` (D2), bound by CD-0034's provenance gate, so
    nothing here re-checks that channel.
    """
    packet = json.loads((ROOT / "contracts/agent-lane-packet.schema.json").read_text(encoding="utf-8"))
    registry = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text(encoding="utf-8"))
    skills_dir = ROOT / "skills"
    entries = sorted(entry.name for entry in skills_dir.iterdir()) if skills_dir.is_dir() else []
    return cd0043_lane_methodology_findings(packet, registry, entries)

def cd0043_lane_methodology_findings(packet_schema: object, registry: object, skills_entries: list[str]) -> list[str]:
    """Pure form of the CD-0043 D1/D3 check, over already-parsed documents."""
    findings: list[str] = []
    properties = packet_schema.get("properties", {}) if isinstance(packet_schema, dict) else {}
    if not isinstance(properties, dict):
        findings.append("contracts/agent-lane-packet.schema.json: properties must be an object")
    elif "methodology" in properties:
        findings.append(
            "contracts/agent-lane-packet.schema.json: a 'methodology' property is rejected by CD-0043; "
            "host instructions through CONCORD_HOST_INSTRUCTIONS are the sole channel"
        )
    version = registry.get("version") if isinstance(registry, dict) else None
    if version != 1:
        findings.append(
            f"contracts/agent-lanes.v1.json: registry version {version!r} moves; CD-0043 pinned version 1 "
            "and a bump needs a superseding decision"
        )
    if sorted(skills_entries) != ["README.md"]:
        findings.append(
            f"skills/: CD-0043 D3 ships the boundary as README.md only (a decided emptiness); found {sorted(skills_entries)}"
        )
    return findings

def _check_evidence_obligation_vocabulary() -> list[str]:
    """Prove the lane evidence obligation join (CD-0056 D2).

    The vocabulary is declared twice: `agent-lanes.schema.json` bounds what a
    lane may owe, and `agent-lane-report.schema.json` bounds what a report may
    discharge. Two enums that must agree are a join, and an unvalidated join
    fails open on the first divergence — a lane could declare an obligation no
    report is able to name, making that lane undispatchable at the moment its
    manifest is edited rather than at the moment the schemas drift.
    """
    return evidence_obligation_findings(
        json.loads((ROOT / "contracts/agent-lanes.schema.json").read_text(encoding="utf-8")),
        json.loads((ROOT / "contracts/agent-lane-report.schema.json").read_text(encoding="utf-8")),
        json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text(encoding="utf-8")),
    )

def _check_lane_agent_selectability() -> list[str]:
    """Prove a lane definition is dispatchable but not operator-selectable (CD-0070).

    Two frontmatter keys carry this and they pull in opposite directions. Run
    mode refuses a subagent-mode target and silently substitutes the default
    agent, so CD-0064 D1 requires a primary-capable mode. The host then cycles
    every primary-capable agent whose hidden flag is unset, which is how a
    worker lane became selectable as the operator's session agent.

    The generator's own `--check` proves the definitions match the generator,
    which is self-consistency rather than proof: a change to both sides passes
    it. These assertions read the definitions directly, so dropping either key
    fails here regardless of what the generator emits.
    """
    findings: list[str] = []
    manifest = json.loads((ROOT / "contracts/agent-lanes.v1.json").read_text(encoding="utf-8"))
    for lane in manifest.get("lanes", []):
        relative = Path(".opencode/agents") / f"concord-{lane['id']}.md"
        try:
            frontmatter = (ROOT / relative).read_text(encoding="utf-8").split("---\n", 2)[1]
        except (OSError, IndexError):
            findings.append(f"{relative}: lane definition is absent or carries no frontmatter block")
            continue
        if "\nhidden: true\n" not in f"\n{frontmatter}":
            findings.append(f"{relative}: lane definition must declare `hidden: true` (CD-0070 D1)")
        if "\nmode: all\n" not in f"\n{frontmatter}":
            findings.append(f"{relative}: lane definition must declare `mode: all` (CD-0064 D1)")
    return findings

def _obligation_enum(document: object, label: str, findings: list[str]) -> list[str] | None:
    node = document
    for key in ("$defs", "evidence_obligation", "enum"):
        if not isinstance(node, dict) or key not in node:
            findings.append(f"{label}: missing $defs/evidence_obligation/enum")
            return None
        node = node[key]
    if not isinstance(node, list) or not node or len(set(node)) != len(node):
        findings.append(f"{label}: $defs/evidence_obligation/enum must be a nonempty duplicate-free array")
        return None
    return node

def evidence_obligation_findings(lanes_schema: object, report_schema: object, registry: object) -> list[str]:
    """Pure form of the CD-0056 D2 check, over already-parsed documents."""
    findings: list[str] = []
    lanes_label = "contracts/agent-lanes.schema.json"
    report_label = "contracts/agent-lane-report.schema.json"
    declared = _obligation_enum(lanes_schema, lanes_label, findings)
    dischargeable = _obligation_enum(report_schema, report_label, findings)
    if declared is None or dischargeable is None:
        return findings
    if set(declared) != set(dischargeable):
        findings.append(
            f"evidence obligation vocabulary differs between {lanes_label} and {report_label}: "
            f"{sorted(set(declared) ^ set(dischargeable))}"
        )
        return findings
    if declared != dischargeable:
        # Same members, different order. Harmless to a validator and confusing
        # to a reader diffing the two contracts, so it is a finding rather than
        # a tolerated difference.
        findings.append(
            f"evidence obligation vocabulary is ordered differently in {lanes_label} and {report_label}"
        )
        return findings
    vocabulary = set(declared)
    lanes = registry.get("lanes", []) if isinstance(registry, dict) else []
    for lane in lanes:
        undeclared = sorted(set(lane.get("evidence_obligations", [])) - vocabulary)
        if undeclared:
            findings.append(
                f"contracts/agent-lanes.v1.json: lane {lane.get('id')!r} declares "
                f"evidence obligation(s) outside the closed vocabulary: {undeclared}"
            )
    return findings

def _check_envelope_operation_vocabulary() -> list[str]:
    """Prove the envelope tool/operation join (issue #352).

    `$defs/base` and `$defs/nextIntent` each conjoin `$defs/toolOperation` with
    their own `operation` enum and `query_id` pattern. A conjunction is only
    satisfiable where the terms agree, so a pair declared in `toolOperation`
    whose operation the enum omits — or whose query_id the pattern rejects — is
    dead on arrival: no envelope naming it can ever validate, and the adapter
    turns every core response for it into `malformed_response`.
    """
    envelope = json.loads((ROOT / "contracts/agent-tool-envelope.schema.json").read_text(encoding="utf-8"))
    manifest = json.loads((ROOT / "contracts/agent-tool-surface.v1.json").read_text(encoding="utf-8"))
    return envelope_operation_findings(envelope) + envelope_operation_coverage_findings(envelope, manifest)

def _declared_tool_operations(envelope: object, findings: list[str]) -> list[tuple[str, str, str | None]] | None:
    """Flatten `$defs/toolOperation` into (tool, operation, query_id) triples."""
    branches = envelope.get("$defs", {}).get("toolOperation", {}).get("oneOf") if isinstance(envelope, dict) else None
    if not isinstance(branches, list) or not branches:
        findings.append("contracts/agent-tool-envelope.schema.json: missing $defs/toolOperation/oneOf")
        return None
    declared: list[tuple[str, str, str | None]] = []
    for index, branch in enumerate(branches):
        properties = branch.get("properties") if isinstance(branch, dict) else None
        if not isinstance(properties, dict) or "tool" not in properties or "operation" not in properties:
            findings.append(f"contracts/agent-tool-envelope.schema.json: $defs/toolOperation/oneOf[{index}] must constrain tool and operation")
            return None
        tool = properties["tool"].get("const")
        operation = properties["operation"]
        operations = [operation["const"]] if "const" in operation else operation.get("enum")
        query_id = properties.get("query_id", {}).get("const")
        if not isinstance(tool, str) or not isinstance(operations, list) or not operations:
            findings.append(f"contracts/agent-tool-envelope.schema.json: $defs/toolOperation/oneOf[{index}] must name one tool const and at least one operation")
            return None
        declared.extend((tool, name, query_id) for name in operations)
    return declared

def envelope_operation_findings(envelope: object) -> list[str]:
    """Pure form of the issue #352 check, over an already-parsed envelope schema.

    Containment is one-way. Every operation `toolOperation` pairs must appear in
    the conjoined enum, but the enum legitimately carries operations that no
    read branch pairs, so this asserts a subset rather than equality.
    """
    findings: list[str] = []
    label = "contracts/agent-tool-envelope.schema.json"
    declared = _declared_tool_operations(envelope, findings)
    if declared is None:
        return findings
    for definition in ("base", "nextIntent"):
        properties = envelope.get("$defs", {}).get(definition, {}).get("properties")
        if not isinstance(properties, dict):
            findings.append(f"{label}: missing $defs/{definition}/properties")
            continue
        enum = properties.get("operation", {}).get("enum")
        if not isinstance(enum, list) or not enum or len(set(enum)) != len(enum):
            findings.append(f"{label}: $defs/{definition}/properties/operation/enum must be a nonempty duplicate-free array")
            continue
        unsatisfiable = sorted({f"{tool}.{operation}" for tool, operation, _ in declared if operation not in set(enum)})
        if unsatisfiable:
            findings.append(
                f"{label}: $defs/{definition}/properties/operation/enum omits operation(s) that "
                f"$defs/toolOperation declares, making them unsatisfiable: {unsatisfiable}"
            )
        pattern = properties.get("query_id", {}).get("pattern")
        if not isinstance(pattern, str) or not pattern:
            findings.append(f"{label}: $defs/{definition}/properties/query_id/pattern must be a nonempty string")
            continue
        try:
            compiled = re.compile(pattern)
        except re.error as exc:
            findings.append(f"{label}: $defs/{definition}/properties/query_id/pattern is not a valid regular expression: {exc}")
            continue
        rejected = sorted({f"{tool}.{operation} ({query_id})" for tool, operation, query_id in declared if query_id is not None and not compiled.fullmatch(query_id)})
        if rejected:
            findings.append(
                f"{label}: $defs/{definition}/properties/query_id/pattern rejects query_id(s) that "
                f"$defs/toolOperation declares, making them unsatisfiable: {rejected}"
            )
    return findings

def envelope_operation_coverage_findings(envelope: object, manifest: object) -> list[str]:
    """Assert the other direction: every declared operation is enumerated.

    `envelope_operation_findings` checks that what `toolOperation` pairs stays
    satisfiable against the conjoined terms. Nothing checked that the manifest
    is covered, so an operation added to the surface without a matching branch
    left `$defs/toolOperation` with no satisfiable member. The adapter runs the
    generated validator on every core response, so such an operation answers
    `malformed_core_response` for a well-formed result and cannot be called.
    """
    findings: list[str] = []
    label = "contracts/agent-tool-envelope.schema.json"
    declared = _declared_tool_operations(envelope, findings)
    if declared is None:
        return findings
    operations = manifest.get("operations") if isinstance(manifest, dict) else None
    if not isinstance(operations, list) or not operations:
        findings.append("contracts/agent-tool-surface.v1.json: missing operations")
        return findings
    paired = {(tool, operation): query_id for tool, operation, query_id in declared}
    uncovered, mismatched = [], []
    for operation in operations:
        key = (operation.get("tool"), str(operation.get("id", "")).split(".", 1)[-1])
        if key not in paired:
            uncovered.append(f"{key[0]}.{key[1]}")
            continue
        if paired[key] != operation.get("query_id"):
            mismatched.append(f"{key[0]}.{key[1]} pairs query_id {paired[key]!r}, the manifest declares {operation.get('query_id')!r}")
    if uncovered:
        findings.append(
            f"{label}: $defs/toolOperation enumerates no branch for declared operation(s), "
            f"so a core response for them cannot satisfy the envelope: {sorted(uncovered)}"
        )
    if mismatched:
        findings.append(f"{label}: $defs/toolOperation disagrees with the manifest: {sorted(mismatched)}")
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
    lane_findings: list[str] = []
    lane_schema_expectations = {
        ROOT / "contracts/agent-lanes.schema.json": {"schema_version", "registry", "version", "lanes"},
        ROOT / "contracts/agent-lane-packet.schema.json": {"schema_version", "attempt_id", "lane_id", "lane_version", "lane_digest", "work_id", "step_id", "inputs"},
        ROOT / "contracts/agent-lane-report.schema.json": {"schema_version", "attempt_id", "lane_id", "lane_version", "lane_digest", "readback_model", "status", "evidence"},
    }
    for path, required in lane_schema_expectations.items():
        lane_findings.extend(_check_closed_schema(path, required))
    lane_findings.extend(_check_evidence_obligation_vocabulary())
    lane_findings.extend(_check_cd0043_lane_methodology())
    lane_findings.extend(_check_envelope_operation_vocabulary())
    lane_findings.extend(_check_lane_agent_selectability())
    lane_generator = subprocess.run([sys.executable, str(ROOT / "scripts/generate-agent-lanes.py"), "--check"], cwd=ROOT, capture_output=True, text=True)
    if lane_generator.returncode:
        lane_findings.append(lane_generator.stderr.strip() or lane_generator.stdout.strip() or "lane generator failed")
    if lane_findings:
        for finding in lane_findings:
            print(finding, file=sys.stderr)
        return 1
    bun = shutil.which("bun")
    # The adapter is the agent-facing surface, so "Bun was unavailable" is a
    # tolerable answer on a contributor laptop and an unacceptable one in CI:
    # the fallback below checks generated-file markers, which cannot fail for
    # any behavioural change to concord.ts or dispatch.ts. CONCORD_REQUIRE_BUN
    # makes the required check fail closed if the toolchain step is ever
    # dropped, rather than passing while verifying nothing.
    if not bun and os.environ.get("CONCORD_REQUIRE_BUN") == "1":
        print(
            "CONCORD_REQUIRE_BUN=1 but Bun is not installed: the adapter "
            "build, contract fixtures, and adapter tests would be skipped",
            file=sys.stderr,
        )
        return 1
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
            # Build the file set the installer places, not the repository tree.
            # Building in ROOT resolves every sibling module whether or not it
            # ships, so it proves the adapter compiles here and says nothing
            # about what an operator receives.
            staged = Path(out) / "shipped-adapter"
            staged.mkdir()
            for name in ADAPTER_FILES:
                shutil.copy2(ROOT / "adapter/opencode" / name, staged / name)
            adapter = subprocess.run([bun, "build", "concord.ts", "--outdir", str(staged / "out"), "--external", "@opencode-ai/plugin", "--target", "bun"], cwd=staged)
            if adapter.returncode:
                print("shipped adapter file set is not import-closed; see scripts/install.py ADAPTER_FILES", file=sys.stderr)
                return adapter.returncode
            # The whole adapter suite, not one file: dispatch.test.ts and
            # dispatch_identity.test.ts cover the CD-0017 worker-evidence
            # append path, which no Go test reaches.
            expected_suites = {"concord.test.ts", "dispatch.test.ts", "dispatch_identity.test.ts", "envelope_operation.test.ts"}
            present_suites = {path.name for path in (ROOT / "adapter/opencode").glob("*.test.ts")}
            if not expected_suites.issubset(present_suites):
                print(f"adapter test suite missing: {sorted(expected_suites - present_suites)}", file=sys.stderr); return 1
            adapter_tests = subprocess.run([bun, "test", "adapter/opencode"], cwd=ROOT)
            if adapter_tests.returncode: return adapter_tests.returncode
            # Typecheck the adapter against the host's published declarations,
            # installed at the exact versions docs/adapter-host-pin.v1.json
            # pins. The adapter previously carried a hand-written mirror of that
            # surface, which could only be wrong in the direction nothing
            # checked: upstream removes or narrows a declaration, the mirror
            # keeps declaring it, and the adapter compiles against a host
            # surface that does not exist. Run after the test suite so
            # behavioural failures surface first.
            summary = "Bun syntax/build/typecheck"
            pin_findings: list[str] = []
            pin = load_host_pin(pin_findings)
            if pin is None:
                for finding in pin_findings: print(finding, file=sys.stderr)
                return 1
            workspace = Path(out) / "host-typecheck"
            staging_error = stage_host_workspace(bun, pin, workspace)
            if staging_error:
                # Installing the declarations needs the package registry. On a
                # contributor laptop that is a tolerable answer, for the same
                # reason a missing Bun is; in CI it is not, and the variable
                # that makes a missing toolchain fail closed makes an
                # unreachable registry fail closed too.
                if os.environ.get("CONCORD_REQUIRE_BUN") == "1":
                    print(staging_error, file=sys.stderr); return 1
                print(f"adapter host typecheck skipped: {staging_error}")
                summary = "Bun syntax/build; host typecheck NOT run"
            else:
                schema_probe = subprocess.run([bun, "run", "src/host_schema_probe.ts"], cwd=workspace, capture_output=True, text=True)
                if schema_probe.returncode:
                    print((schema_probe.stderr or schema_probe.stdout).strip(), file=sys.stderr)
                    print("adapter host schema probe failed", file=sys.stderr)
                    return schema_probe.returncode
                typecheck = subprocess.run([bun, "x", f"typescript@{pin['typescript']}", "-p", "tsconfig.json"], cwd=workspace, capture_output=True, text=True)
                reconcile_diagnostics(typecheck.stdout + typecheck.stderr, pin["allowances"], pin_findings, typecheck.returncode)
                if pin_findings:
                    print(typecheck.stdout.strip(), file=sys.stderr)
                    for finding in pin_findings: print(finding, file=sys.stderr)
                    return 1
            source = (ROOT / "adapter/opencode/concord.ts").read_text(encoding="utf-8")
            exports = re.findall(r"export const ([A-Za-z_][A-Za-z0-9_]*) = tool\(", source)
            if exports != ["product_view", "work_browse", "work_trace", "knowledge", "work_define", "domain", "work_initiative", "work_transition", "work_relate", "work_compact", "work_start"]:
                print(f"adapter export drift: {exports}", file=sys.stderr); return 1
        print(f"agent contract check passed ({summary})")
    else:
        for path in (ROOT / "adapter/opencode/generated-contracts.ts", ROOT / "adapter/opencode/generated-contract-tests.ts"):
            text = path.read_text(encoding="utf-8")
            if "Code generated by scripts/generate-agent-contracts.py" not in text or "manifestDigest" not in text and path.name == "generated-contracts.ts":
                print(f"generated TypeScript marker missing: {path}", file=sys.stderr); return 1
        print("agent contract check passed (generated-marker fallback only; Bun unavailable, adapter build and tests NOT run)")
    return 0
if __name__ == "__main__": raise SystemExit(main())
