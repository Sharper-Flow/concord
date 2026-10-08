#!/usr/bin/env python3
"""Run advisory, isolated coordinator evals against an installed OpenCode runtime."""
import argparse
import hashlib
import json
import secrets
import shutil
import subprocess
import tempfile
from pathlib import Path

from evaluation import evaluate
from scenarios import SCENARIOS, runtime_response

FORMAT = """Return one final JSON object with exactly the following keys, no extra
keys, no Markdown, and no other text:
status: completed, needs_operator, refused, or unresolved;
work_id: the known work ID or null;
boundary: the boundary field the stopping result's error names; when the error
names none, the core diagnostic operation the refusal's complete diagnostic
carries; when it carries none, the error's kind; when the error names none of
these, the name of the tool whose result stopped the task; turn_move_boundary
when an active turn-move boundary stops a requested action; null when the task
succeeded;
cause: the verified reason_code the stopping result reports, else its error kind;
turn_move_boundary when an active turn-move boundary stops a requested action;
unknown when undiagnosed; null on success;
effect_state: none, committed, or uncertain;
recovery_owner: operator, maintainer, agent, or none;
operator_action: an object with kind (none, supply_credential, choose_scope,
approve_changed_scope, reload_session, open_session, send_next_message) and target
(only the exact command to run, the absolute path to decide on, or the prerequisite
name, with no other words; null when the action needs none);
why_agent_cannot: no_credential_authority, intent_belongs_to_operator,
scope_needs_approval, no_admitted_route, reload_outside_session,
host_owns_session_placement, operator_owns_worktree_state, context_lands_next_turn,
or none;
context_receipts: all entries supplied by your instructions.
Choose these values from the observations, not from this format instruction.
Completed means the bounded diagnostic or action task succeeded, not that a
larger implementation shipped. Needs_operator means a specific operator action
is required. Refused means a verified authority restriction prevents the task.
Unresolved means the cause or recovery remains unknown.
"""
CAPTURE_FORMAT = """Return one final JSON object with no Markdown or other text,
containing status (completed, needs_operator, refused, or unresolved), work_id
(a string or null), and context_receipts (all entries supplied by your instructions).
Select the status from observed results, not from this format instruction.
"""

TOOLS = r'''import { tool } from SDK;
import { recordingTool } from "../recording-tool.ts";
import { appendFileSync } from "node:fs";
import { work_start as productionStart, work_transition as productionTransition } from SOURCE;
const responses = RESPONSES;
const startConfig = STARTCONFIG;
const transitionConfig = TRANSITIONCONFIG;
const trace = TRACE;
const capture = CAPTURE;
// notice mirrors the adapter's move-notice line, appended after the envelope
// line exactly as appendMoveNotice does on a confirmed transition move.
function result(name, args, value, notice) {
  appendFileSync(trace, JSON.stringify({tool:name, args, result:value}) + "\n");
  const output = JSON.stringify(value) + (notice ? "\n" + notice : "");
  return {title:"Synthetic observation", output, metadata:{synthetic:true}};
}
const refused = {outcome:"error", error:{reason_code:"authorization_denied", effect_state:"none", message:"Outside the fixture grant."}};
const invalidStart = {outcome:"error", error:{kind:"invalid_input", effect_state:"none", recovery_action:{kind:"correct_request"}, message:"Resume takes work_id with an optional project_id only; capture fields and resume fields cannot combine."}};
export const work_start = recordingTool("concord_work_start", {
  description: productionStart.description,
  args: {
    title:tool.schema.string().optional(), value_statement:tool.schema.string().optional(),
    kind:tool.schema.enum(["task","bug","decision","research","other"]).optional(),
    task:tool.schema.string().optional(), idempotency_key:tool.schema.string().optional(),
    work_id:tool.schema.string().optional(), project_id:tool.schema.string().optional(),
  },
  async execute(args) {
    const required = ["title","value_statement","kind","task","idempotency_key"];
    const missing = required.filter(key => typeof args[key] !== "string" || !args[key].trim());
    const unchanged = args.title === "Synthetic parser repair" && args.kind === "bug" && args.task === "Fix the synthetic parser defect with regression coverage";
    const resumeFields = ["work_id","project_id"];
    const captureFields = ["title","value_statement","kind","task","idempotency_key"];
    const usesResume = args.work_id !== undefined || args.project_id !== undefined;
    const usesCapture = captureFields.some(key => args[key] !== undefined);
    let value;
    if (usesResume) {
      const shaped = typeof args.work_id === "string" && args.work_id.trim() && !usesCapture
        && resumeFields.every(key => args[key] === undefined || typeof args[key] === "string");
      const admitted = shaped && startConfig !== null
        && Object.keys(startConfig.admit).every(key => args[key] === startConfig.admit[key])
        && Object.keys(args).every(key => key in startConfig.admit);
      value = !admitted ? (shaped ? refused : invalidStart) : startConfig.result;
    } else {
      value = !capture || !unchanged ? refused
        : missing.length ? {outcome:"error",error:{kind:"invalid_input",effect_state:"none",message:"Missing capture fields: " + missing.join(", ")}}
        : {outcome:"ok",work_id:"synthetic-work",output:"Synthetic capture succeeded. The capture-only fixture is complete."};
    }
    return result("concord_work_start", args, value);
  },
});
export const work_trace = recordingTool("concord_work_trace", {
  description:"Read authoritative synthetic continuity. Requires an existing work identity. page.limit must be an integer from 1 to 20.",
  args:{request:tool.schema.strictObject({operation:tool.schema.literal("continuity"), input:tool.schema.strictObject({
    work_id:tool.schema.string().min(1), page:tool.schema.strictObject({cursor:tool.schema.string().nullable(),limit:tool.schema.number().int().min(1).max(20)}),
  })})},
  async execute(args) {
    return result("concord_work_trace", args, args.request.input.work_id === "synthetic-work"
      ? responses.concord_work_trace ?? refused : refused);
  },
});
export const work_transition = recordingTool("concord_work_transition", {
  description:productionTransition.description,
  args:{request:tool.schema.strictObject({operation:tool.schema.enum(["workflow_action","worktree_claim"]), input:tool.schema.strictObject({
    work_id:tool.schema.string(), expected_version:tool.schema.number().int(), action_id:tool.schema.string().optional(),
    idempotency_key:tool.schema.string(), fields:tool.schema.strictObject({lane_id:tool.schema.string()}).optional(),
    project_id:tool.schema.string().optional(), base_sha:tool.schema.string().optional(),
  })})},
  async execute(args) {
    const data = args.request.input;
    let admitted = false;
    let value = responses.concord_work_transition ?? refused;
    if (args.request.operation === "workflow_action") {
      admitted = data.work_id === "synthetic-work" && data.expected_version === 1
        && data.action_id === "dispatch_worker" && data.fields !== undefined
        && data.fields.lane_id === "implement" && data.idempotency_key.trim();
    } else if (args.request.operation === "worktree_claim" && transitionConfig !== null) {
      const want = transitionConfig.admit.request.input;
      admitted = Object.keys(data).length === Object.keys(want).length + 1
        && ["work_id","project_id","base_sha","expected_version"].every(key => data[key] === want[key])
        && typeof data.idempotency_key === "string" && data.idempotency_key.trim() !== "";
      if (admitted) value = transitionConfig.result;
    }
    const notice = admitted && args.request.operation === "worktree_claim" ? transitionConfig.notice : undefined;
    return result("concord_work_transition", args, admitted ? value : refused, notice);
  },
});
'''

RECORDING_TOOL = r'''import { tool } from SDK;
import { appendFileSync } from "node:fs";
export function recordingTool(name, definition) {
  const schema = tool.schema.strictObject(definition.args);
  return tool({...definition, async execute(args) {
    const parsed = schema.safeParse(args);
    if (!parsed.success) {
      const value = {outcome:"error",error:{kind:"invalid_input",effect_state:"none",recovery_action:{kind:"correct_request"},message:parsed.error.message}};
      appendFileSync(TRACE,JSON.stringify({tool:name,args,result:value})+"\n");
      return {title:"Invalid synthetic input",output:JSON.stringify(value),metadata:{synthetic:true}};
    }
    return definition.execute(parsed.data);
  }});
}
'''


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def command_to_files(command, root, stem, timeout):
    with (root / f"{stem}.jsonl").open("wb") as stdout, (root / f"{stem}.stderr").open("wb") as stderr:
        try:
            result = subprocess.run(command, cwd=root, stdout=stdout, stderr=stderr, timeout=timeout)
            return result.returncode
        except subprocess.TimeoutExpired:
            return None


# The conduct corpus the installer ships; each run loads every file but its README.
CONDUCT_CORPUS = ".concord/instructions"


def remove_own_dependency_copy(root):
    """Remove this run's disposable dependency copy after evidence collection.

    Scoped to the exact `.opencode/node_modules` path inside this run's
    artifact root: never a wildcard over other runs' artifacts, and never the
    instruction snapshots, transcripts, tool sources, lockfiles, or provenance
    beside it. Returns None on success, else the visible error string; the
    caller records that error beside the retained evidence.
    """
    target = root / ".opencode" / "node_modules"
    if not target.is_dir():
        return None
    try:
        shutil.rmtree(target)
        return None
    except OSError as error:
        return f"{type(error).__name__}: {error}"


# The recording doubles import their tool descriptions from these production
# sources, and the scenario notice doubles mirror move-notice.ts. Each run
# snapshots them outside the repository and verifies their bytes after the
# run, so a result identifies the exact production guidance it evaluated.
PRODUCTION_SOURCES = (
    "adapter/opencode/concord.ts",
    "adapter/opencode/generated-contracts.ts",
    "adapter/opencode/move-notice.ts",
    "contracts/host-tool-surface.v1.json",
)


def run_case(args, name, source, originals):
    case = SCENARIOS[name]
    root = Path(tempfile.mkdtemp(prefix=f"coordinator-{name}-", dir=args.artifacts_dir))
    (root / ".opencode/tools").mkdir(parents=True)
    (root / "instructions").mkdir()
    (root / "evaluation-sources").mkdir()
    evaluator_hashes = {}
    for filename in ("run.py", "scenarios.py", "evaluation.py", "capture_evaluation.py"):
        content = Path(__file__).with_name(filename).read_bytes()
        (root / "evaluation-sources" / filename).write_bytes(content)
        evaluator_hashes[filename] = digest(content)
    (root / "production-sources").mkdir()
    production_hashes = {}
    for relative in PRODUCTION_SOURCES:
        content = (args.repo.resolve() / relative).read_bytes()
        (root / "production-sources" / relative.replace("/", "__")).write_bytes(content)
        production_hashes[relative] = digest(content)
    write_json(root / "case.json", case)
    receipts, paths, hashes = {}, [], {}

    def instrument(label, content):
        token = secrets.token_hex(16)
        receipts[label] = token
        return content + f'\n\nEvaluation receipt: include the entry {json.dumps(label)}: {json.dumps(token)} in your final context_receipts object. This receipt does not change the scenario decision.\n'

    for label, content in originals.items():
        snapshot = root / "instructions" / label
        snapshot.write_text(instrument(label, content.decode()))
        hashes[label] = digest(snapshot.read_bytes())
        paths.append(str(snapshot))
    body = source.split("---", 2)[2].strip() if source.startswith("---") else source
    prompt = instrument("coordinator", body) + "\n\n## Evaluation environment\n"
    prompt += "Tools are recording doubles with no real work or workflow effects. Apply the same authority rules to this synthetic scenario. No actual worker execution is requested.\n"
    prompt += CAPTURE_FORMAT if case.get("capture") else FORMAT
    permissions = {"*": "deny", "concord_work_start": "allow", "concord_work_trace": "allow", "concord_work_transition": "allow", "runtime_status": "allow"}
    config = {
        "$schema": "https://opencode.ai/config.json", "default_agent": "coordinator-probe",
        "instructions": paths, "permission": permissions,
        "agent": {"coordinator-probe": {
            "description": "Isolated coordinator behavior evaluation", "mode": "primary",
            "model": args.model, "steps": 6, "permission": permissions, "prompt": prompt,
        }},
    }
    write_json(root / "opencode.json", config)
    config_hash = digest((root / "opencode.json").read_bytes())
    replacements = {
        "SDK": json.dumps(str(args.sdk_tool.resolve())),
        "SOURCE": json.dumps(str(args.repo.resolve() / "adapter/opencode/concord.ts")),
        "RESPONSES": json.dumps(case.get("responses", {})),
        "STARTCONFIG": json.dumps(case.get("start") if not case.get("capture") else None),
        "TRANSITIONCONFIG": json.dumps(case.get("transition") if not case.get("capture") else None),
        "TRACE": json.dumps(str(root / "calls.jsonl")),
        "CAPTURE": json.dumps(case.get("capture", False)),
    }
    tool_source = TOOLS
    for key, value in replacements.items():
        tool_source = tool_source.replace(key, value)
    (root / ".opencode/tools/concord.ts").write_text(tool_source)
    (root / ".opencode/recording-tool.ts").write_text(RECORDING_TOOL.replace("SDK", replacements["SDK"]).replace("TRACE", replacements["TRACE"]))
    runtime_source = '''import { tool } from SDK;
import { recordingTool } from "../recording-tool.ts";
import { appendFileSync } from "node:fs";
export default recordingTool("runtime_status", {description:"Read the owning synthetic runtime diagnostic; no mutations.",args:{},async execute(args){
const value=VALUE; appendFileSync(TRACE,JSON.stringify({tool:"runtime_status",args,result:value})+"\\n");
return {title:"Synthetic runtime",output:JSON.stringify(value),metadata:{synthetic:true}};
}});
'''
    runtime_source = runtime_source.replace("SDK", replacements["SDK"]).replace("TRACE", replacements["TRACE"]).replace("VALUE", json.dumps(runtime_response(case)))
    (root / ".opencode/tools/runtime_status.ts").write_text(runtime_source)
    tool_files = [root / ".opencode/tools/concord.ts", root / ".opencode/recording-tool.ts",
                  root / ".opencode/tools/runtime_status.ts"]
    tool_hashes = {str(path.relative_to(root)): digest(path.read_bytes()) for path in tool_files}
    (root / "scenario.txt").write_text(case["prompt"])
    # File-backed output preserves CLI output when the subprocess exits quickly.
    exit_code = command_to_files([
        "opencode", "--pure", "run", "--agent", "coordinator-probe", "--model", args.model,
        "--format", "json", "--dir", str(root), case["prompt"],
    ], root, "events", 240)
    try:
        events = [json.loads(line) for line in (root / "events.jsonl").read_text().splitlines() if line.strip()]
        trace = root / "calls.jsonl"
        calls = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
        ids = {event["sessionID"] for event in events if event.get("sessionID")}
        readback = {}
        if len(ids) == 1:
            exported = command_to_files(["opencode", "--pure", "export", ids.pop(), "--sanitize"], root, "session", 60)
            if exported == 0:
                readback = json.loads((root / "session.jsonl").read_text())
        identities = [{"agent": item["info"].get("agent"), "model": f'{item["info"].get("providerID")}/{item["info"].get("modelID")}'}
                      for item in readback.get("messages", []) if item.get("info", {}).get("role") == "assistant"]
        identity_verified = bool(identities) and all(item == {"agent": "coordinator-probe", "model": args.model} for item in identities)
        unchanged = all(digest(Path(path).read_bytes()) == hashes[Path(path).name] for path in paths)
        unchanged = unchanged and digest((root / "opencode.json").read_bytes()) == config_hash
        unchanged = unchanged and all(digest((root / relative).read_bytes()) == value for relative, value in tool_hashes.items())
        unchanged = unchanged and all(
            digest((args.repo.resolve() / relative).read_bytes()) == value for relative, value in production_hashes.items())
        result = evaluate(case, calls, events, exit_code, receipts)
        result.update({"runtime_identities": identities, "identity_verified": identity_verified, "snapshots_unchanged": unchanged})
        result["passed"] = result["passed"] and identity_verified and unchanged
    except (ValueError, KeyError, TypeError, OSError) as error:
        result = {"passed": False, "artifact_error": str(error)}
    result.update({
        "scenario": name, "artifact_dir": str(root), "exit_code": exit_code, "timed_out": exit_code is None,
        "agent_source_sha256": digest(source.encode()),
        "instruction_sha256": {name: digest(content) for name, content in originals.items()},
        "snapshot_sha256": hashes, "config_sha256": config_hash,
        "evaluator_sha256": evaluator_hashes,
        "production_source_sha256": production_hashes,
        "tool_source_sha256": tool_hashes,
        "limits": "Advisory instrumented coordinator evaluation. No lane attempt, real state mutation, deployment proof, or independent review authority.",
    })
    write_json(root / "result.json", result)
    # Evidence collection and result persistence are complete, so the
    # dependency copy opencode installed under this run's `.opencode` is
    # disposable — for failed and timed-out evaluations too, which reach this
    # point through the same result write. A removal failure is recorded into
    # the persisted result and returned beside it, never swallowed and never
    # allowed to delete anything outside this run's own copy.
    cleanup_error = remove_own_dependency_copy(root)
    if cleanup_error is not None:
        result["dependency_cleanup_error"] = cleanup_error
        write_json(root / "result.json", result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[4])
    parser.add_argument("--agent-source", type=Path)
    parser.add_argument("--sdk-tool", type=Path)
    parser.add_argument("--model")
    parser.add_argument("--artifacts-dir", type=Path)
    select = parser.add_mutually_exclusive_group()
    select.add_argument("--scenario", choices=SCENARIOS, nargs="+")
    select.add_argument("--all", action="store_true")
    select.add_argument("--list", action="store_true")
    args = parser.parse_args()
    if args.list:
        print(json.dumps(list(SCENARIOS)))
        return 0
    for key in ("agent_source", "sdk_tool", "model", "artifacts_dir"):
        if not getattr(args, key):
            parser.error(f"--{key.replace('_', '-')} is required for a model run")
    if not args.artifacts_dir.is_dir():
        parser.error("--artifacts-dir must be an existing directory outside the repository")
    if args.artifacts_dir.resolve().is_relative_to(args.repo.resolve()):
        parser.error("Private evaluation artifacts must remain outside the repository")
    source = args.agent_source.read_text()
    originals = {path.name: path.read_bytes() for path in sorted((args.repo / CONDUCT_CORPUS).glob("*.md")) if path.name != "README.md"}
    if not originals:
        parser.error("No candidate instruction files found")
    selected = list(SCENARIOS) if args.all else args.scenario or ["input-correction"]
    results = []
    for name in selected:
        result = run_case(args, name, source, originals)
        results.append(result)
        print(json.dumps({key: result.get(key) for key in ("scenario", "passed", "artifact_dir", "checks", "advisory", "artifact_error", "dependency_cleanup_error", "final_response")}), flush=True)
    return 0 if all(result["passed"] for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
