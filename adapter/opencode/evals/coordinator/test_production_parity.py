"""Executable production-boundary parity for the recording doubles.

The comparison the contract names: one common synthetic corpus — the
malformed and corrected shapes for both concord_work_start resume and
concord_work_transition.worktree_claim — runs through the actual production
boundaries AND through the recording double run.py generates for a model run,
and the captured semantic envelopes are compared field by field: outcome,
error.kind, error.effect_state, error.recovery_action (the whole object),
error.retry_safe, and the exact diagnostic message. Kind/effect/recovery
constants and source imports alone prove nothing, so every comparison below
reads envelopes captured by executing a real boundary in this test run:

- concord_work_transition.worktree_claim executes through the real core CLI
  binary built from this repository (`go build ./cmd/concord`, then
  `concord invoke` against an isolated empty store). The core owns this
  input boundary; the adapter injects host_pid and forwards.
- concord_work_start executes through the real adapter module
  (adapter/opencode/concord.ts) under bun, with a controlled child runner
  and host control plane exactly like the owning adapter suite. The adapter
  owns this input boundary (validateWorkStartArgs, before any host write or
  core call).
- the recording double executes under bun from the same generated tool
  source run.py writes for a model run, against the real plugin tool module,
  so the served envelopes and the calls.jsonl trace are the double's real
  behavior.

Transport-only members of the executed core envelope (request_id,
manifest_digest, origin, resolved_scope, authority, freshness, cursors,
watermarks, omissions, warnings, evidence_refs, replayed, schema_version)
are excluded from the double comparison by named structural reason: they are
per-invocation transport identity and framing the synthetic double does not
fabricate. A separate check proves the executed production envelope carries
them consistently. No operator-owned store, worktree, or installation is
touched: every directory below is an isolated stdlib temporary directory.

Missing prerequisites fail explicitly. A skipped prerequisite is a green
run that proved nothing, so this file raises an explicit failure instead of
reporting a skip everywhere.
"""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import run as run_module
from evaluation import structural_equal
from scenarios import BASE_SHA, SCENARIOS, WORK

REPO = Path(__file__).resolve().parents[4]
ADAPTER = REPO / "adapter" / "opencode"
PIN = json.loads((REPO / ".concord" / "docs" / "adapter-host-pin.v1.json").read_text())
PINNED_PLUGIN = next(package for package in PIN["packages"] if package["name"] == "@opencode-ai/plugin")
PINNED_PLUGIN_NAME = PINNED_PLUGIN["name"]
PINNED_PLUGIN_VERSION = PINNED_PLUGIN["version"]

# The semantic triple classification each production boundary serves for the
# corpus, cross-checked against the envelopes executed in this run. The
# recovery_action is the object form both executed boundaries serve.
PRODUCTION_CLAIM_INPUT_REFUSAL = {
    "kind": "invalid_input",
    "effect_state": "none",
    "recovery_action": {"kind": "restart_query"},
    "retry_safe": False,
}
PRODUCTION_START_INPUT_REFUSAL = {
    "kind": "invalid_input",
    "effect_state": "none",
    "recovery_action": {"kind": "correct_request"},
    "retry_safe": False,
}
PRODUCTION_RESUME_REFUSAL = {
    "kind": "resume_failure",
    "effect_state": "none",
    "recovery_action": {"kind": "contact_operator"},
    "retry_safe": False,
}
MANIFEST_DIGEST = (REPO / "contracts" / "agent-tool-surface.digest").read_text().strip()
DIRTY_ORIGIN_STDERR = ("concord work-resume: store: work_bootstrap: invalid_operation: "
                       "cannot chain from dirty worktree of synthetic-origin-work")
SHORT_BASE_SHA = "aabbcc"
# The blank, oversize, and pattern-refusing payload variants: each one passes
# the double's tool schema (every value is a string) and reaches the core's
# payload-schema value validation, which classifies it by the public
# constraints the payload contract states ($defs/id and the inline base_sha
# rule) — never as a missing field.
EMPTY_IDEMPOTENCY_KEY = ""
WHITESPACE_IDEMPOTENCY_KEY = "   "
OVERSIZE_IDEMPOTENCY_KEY = "a" * 129
PATTERN_BASE_SHA = "a" * 45
OVERSIZE_BASE_SHA = "a" * 65


def require(command, purpose):
    """An explicit prerequisite: the named command must exist, or the test
    fails with the reason. Never a skip — a skip turns a missing boundary
    into silent green."""
    path = shutil.which(command)
    if path is None:
        raise AssertionError(f"{purpose} requires {command} on PATH; the boundary cannot be executed")
    return path


def pinned_package_root(node_modules):
    """The package directory the tracked pin's name selects under a
    node_modules directory."""
    return node_modules.joinpath(*PINNED_PLUGIN_NAME.split("/"))


def plugin_metadata_state(package_root):
    """Classify the package.json that owns a resolved plugin module:
    (state, metadata) with state one of ok, missing, unreadable, or
    malformed. Only ok carries the parsed metadata object."""
    try:
        raw = (package_root / "package.json").read_text(encoding="utf-8")
    except FileNotFoundError:
        return "missing", None
    except OSError:
        return "unreadable", None
    except UnicodeDecodeError:
        return "malformed", None
    try:
        metadata = json.loads(raw)
    except ValueError:
        return "malformed", None
    if not isinstance(metadata, dict):
        return "malformed", None
    return "ok", metadata


def metadata_pin_mismatch(metadata):
    """Why the parsed package metadata fails to prove the tracked pin, or
    None when it proves the exact tracked package name and version."""
    if metadata.get("name") != PINNED_PLUGIN_NAME:
        return f"package name {metadata.get('name')!r} is not the tracked {PINNED_PLUGIN_NAME!r}"
    if metadata.get("version") != PINNED_PLUGIN_VERSION:
        return f"package version {metadata.get('version')!r} is not the tracked {PINNED_PLUGIN_VERSION!r}"
    return None


def pinned_plugin_module():
    """The isolated pinned-install route: install the exact tracked plugin
    version into a stdlib temporary directory and return its module. The
    selected module is itself accepted only when its package metadata
    proves the tracked name and version; install, module, and identity
    failures are explicit."""
    bun = require("bun", "installing the pinned plugin dependency")
    root = Path(tempfile.mkdtemp(prefix="concord-parity-plugin-"))
    (root / "package.json").write_text(json.dumps(
        {"name": "concord-parity-plugin", "private": True,
         "dependencies": {PINNED_PLUGIN_NAME: PINNED_PLUGIN_VERSION}}) + "\n")
    install = subprocess.run([str(bun), "install", "--cwd", str(root)], capture_output=True,
                             text=True, timeout=300)
    if install.returncode != 0:
        raise AssertionError(
            "cannot resolve @opencode-ai/plugin through the repository pin "
            f"({PINNED_PLUGIN_VERSION}): bun install failed with exit {install.returncode}: "
            f"{install.stderr.strip()[:400]}")
    package_root = pinned_package_root(root / "node_modules")
    module = package_root / "dist" / "tool.js"
    if not module.is_file():
        raise AssertionError(
            f"cannot resolve {PINNED_PLUGIN_NAME} through the repository pin "
            f"({PINNED_PLUGIN_VERSION}): the install succeeded but placed no dist/tool.js module")
    state, metadata = plugin_metadata_state(package_root)
    if state != "ok":
        raise AssertionError(
            f"the pinned install of {PINNED_PLUGIN_NAME} {PINNED_PLUGIN_VERSION} resolved with "
            f"{state} package metadata: the selected module cannot prove the tracked pin")
    mismatch = metadata_pin_mismatch(metadata)
    if mismatch is not None:
        raise AssertionError(
            f"the pinned install of {PINNED_PLUGIN_NAME} {PINNED_PLUGIN_VERSION} resolved the "
            f"wrong package: {mismatch}")
    return module


def plugin_tool_module(repo=REPO):
    """The real @opencode-ai/plugin tool module, through the repository's
    accepted dependency mechanisms only, never a host-installed path.

    The repository-local cache (.opencode/node_modules) is selected only
    when its own package.json proves the exact package name and version
    the tracked .concord/docs/adapter-host-pin.v1.json pins. Missing,
    unreadable, malformed, wrong-name, or wrong-version metadata never
    establishes agreement: the resolver takes the isolated pinned-install
    route instead, and that route verifies its selected module the same
    way. Every failure is explicit.
    """
    package_root = pinned_package_root(repo / ".opencode" / "node_modules")
    module = package_root / "dist" / "tool.js"
    if module.is_file():
        state, metadata = plugin_metadata_state(package_root)
        if state == "ok" and metadata_pin_mismatch(metadata) is None:
            return module
    return pinned_plugin_module()


def envelope_error(envelope):
    error = envelope.get("error") if isinstance(envelope, dict) else None
    return error if isinstance(error, dict) else {}


def semantic_envelope(envelope):
    """The semantic comparison shape: outcome plus every error field the
    boundaries own — kind, effect_state, the whole recovery_action object,
    retry_safe, and the exact diagnostic. Transport-only members are checked
    separately against the executed production capture."""
    error = envelope_error(envelope)
    return {
        "outcome": envelope.get("outcome"),
        "error": {key: error.get(key) for key in ("kind", "effect_state", "recovery_action", "retry_safe", "message")},
    }


def invoke_request(input_value, request_id):
    return {
        "call_envelope": {
            "schema_version": "1.0", "request_id": request_id, "client_ref": "parity",
            "principal_ref": "", "session_ref": "parity-session", "agent_ref": "coordinator",
            "directory": str(REPO), "worktree": str(REPO),
            "ambient_project_id": "synthetic-project", "scope_version": "v1",
            "manifest_digest": MANIFEST_DIGEST,
        },
        "tool": "concord_work_transition", "operation": "worktree_claim",
        "input": input_value,
    }


def malformed_claim_args():
    """A worktree_claim missing its idempotency_key (the common malformed case)."""
    return {"request": {"operation": "worktree_claim", "input": {
        "work_id": WORK, "project_id": "synthetic-same-repo-project",
        "base_sha": BASE_SHA, "expected_version": 1,
    }}}


def corrected_claim_args():
    args = malformed_claim_args()
    args["request"]["input"]["idempotency_key"] = "parity-claim-key"
    return args


def short_base_claim_args():
    args = corrected_claim_args()
    args["request"]["input"]["base_sha"] = SHORT_BASE_SHA
    return args


def claim_args_with(**overrides):
    """Corrected claim args with the named input fields overridden."""
    args = corrected_claim_args()
    args["request"]["input"].update(overrides)
    return args


def claim_corpus_args():
    """The shared claim corpus as driven tool args, in execution order: every
    input except the corrected shape is a malformed variant the core's input
    boundary refuses before admission."""
    return {
        "claim-missing-idem": malformed_claim_args(),
        "claim-empty-idem": claim_args_with(idempotency_key=EMPTY_IDEMPOTENCY_KEY),
        "claim-whitespace-idem": claim_args_with(idempotency_key=WHITESPACE_IDEMPOTENCY_KEY),
        "claim-oversize-idem": claim_args_with(idempotency_key=OVERSIZE_IDEMPOTENCY_KEY),
        "claim-short-base": short_base_claim_args(),
        "claim-pattern-base": claim_args_with(base_sha=PATTERN_BASE_SHA),
        "claim-oversize-base": claim_args_with(base_sha=OVERSIZE_BASE_SHA),
        "claim-corrected": corrected_claim_args(),
    }


def execute_core_boundary_corpus(invoke):
    """The executed production core envelopes for the claim corpus, driven
    through one invoke(input_value, request_id) callable."""
    return {label: invoke(args["request"]["input"], "parity-" + label)[1]
            for label, args in claim_corpus_args().items()}


def build_core_boundary(root):
    """Build the real core CLI into an isolated directory and return
    (binary path, empty store directory). Fails explicitly, never skips."""
    require("go", "building the core CLI boundary")
    binary = root / "concord"
    build = subprocess.run(["go", "build", "-o", str(binary), "./cmd/concord"],
                           cwd=REPO, capture_output=True, text=True, timeout=600)
    if build.returncode != 0:
        raise AssertionError(f"go build failed:\n{build.stderr}")
    store = root / "store"
    store.mkdir()
    return binary, store


def invoke_core(binary, store, input_value, request_id):
    env = dict(os.environ, CONCORD_DB_PATH=str(store / "parity.db"))
    result = subprocess.run([str(binary), "invoke"],
                            input=json.dumps(invoke_request(input_value, request_id)),
                            capture_output=True, text=True, timeout=120, env=env,
                            cwd=str(binary.parent))
    return result, json.loads(result.stdout) if result.stdout.strip() else {}


def execute_core_corpus(root):
    """The executed production core envelopes for the claim corpus."""
    binary, store = build_core_boundary(root)
    def invoke(input_value, request_id):
        return invoke_core(binary, store, input_value, request_id)
    return execute_core_boundary_corpus(invoke)


def execute_adapter_corpus():
    """The executed production adapter envelopes for the start corpus."""
    root = Path(tempfile.mkdtemp(prefix="concord-parity-adapter-"))
    source = (ADAPTER_DRIVER
              .replace("{adapter}", str(ADAPTER))
              .replace("{dirty_stderr}", json.dumps(DIRTY_ORIGIN_STDERR))
              .replace("{start_mixed}", json.dumps({"work_id": WORK, "title": "Both shapes at once"}))
              .replace("{start_corrected}", json.dumps({"work_id": WORK})))
    run = run_bun(root, source)
    if run.returncode != 0:
        raise AssertionError(f"adapter driver failed:\n{run.stderr}")
    observation = {item["tool"]: item for item in (json.loads(line) for line in run.stdout.splitlines() if line.strip())}
    shutil.rmtree(root, ignore_errors=True)
    return observation


class ProductionCoreBoundaryTests(unittest.TestCase):
    """The real core CLI answers for the common synthetic claim inputs."""

    @classmethod
    def setUpClass(cls):
        require("go", "building the core CLI boundary")
        cls.root = Path(tempfile.mkdtemp(prefix="concord-parity-core-"))
        cls.binary, cls.store = build_core_boundary(cls.root)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.root, ignore_errors=True)

    def invoke(self, input_value, request_id):
        return invoke_core(self.binary, self.store, input_value, request_id)

    def captured(self):
        """The executed production envelopes for the whole claim corpus."""
        return execute_core_boundary_corpus(self.invoke)

    def test_malformed_claim_corpus_serves_the_core_input_refusal(self):
        for name, envelope in self.captured().items():
            if name == "claim-corrected":
                continue
            with self.subTest(case=name):
                self.assertEqual(envelope.get("outcome"), "error")
                captured = semantic_envelope(envelope)
                triple = {key: captured["error"][key] for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
                self.assertTrue(structural_equal(triple, PRODUCTION_CLAIM_INPUT_REFUSAL),
                                json.dumps(captured, indent=1))
                self.assertIsInstance(captured["error"]["message"], str)
                self.assertTrue(captured["error"]["message"])

    def test_corrected_claim_shape_passes_the_input_boundary(self):
        # The corrected shape is admitted by the input boundary: against an
        # empty isolated store the refusal that answers it is the authority
        # gate, not invalid_input — exactly the separation the correction
        # credit depends on.
        envelope = self.captured()["claim-corrected"]
        self.assertEqual(envelope.get("outcome"), "error")
        captured = semantic_envelope(envelope)
        triple = {key: captured["error"][key] for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
        self.assertFalse(structural_equal(triple, PRODUCTION_CLAIM_INPUT_REFUSAL))

    def test_executed_transport_only_members_are_consistent(self):
        # The named exclusion for the double comparison: the executed core
        # envelope's transport members are per-invocation framing. This check
        # proves they are present and consistent on the executed capture, so
        # excluding them from the double comparison hides no semantic drift.
        envelope = self.captured()["claim-missing-idem"]
        self.assertEqual(envelope.get("schema_version"), "1.0")
        self.assertEqual(envelope.get("request_id"), "parity-claim-missing-idem")
        self.assertEqual(envelope.get("manifest_digest"), MANIFEST_DIGEST)
        self.assertEqual(envelope.get("origin"), "core")
        self.assertEqual(envelope.get("tool"), "concord_work_transition")
        self.assertEqual(envelope.get("operation"), "worktree_claim")
        for member in ("resolved_scope", "freshness", "next_cursor", "omissions",
                       "warnings", "evidence_refs", "ordering_keys", "source_version_watermark"):
            self.assertIn(member, envelope)


DOUBLE_DRIVER = """
import { work_start, work_transition } from {tools_import};
const context = { sessionID: "parity-session", messageID: "m1", agent: "coordinator-probe",
  directory: "/synthetic/repo", worktree: "/synthetic/repo", abort: new AbortController().signal };
async function call(name, tool, args) {
  const result = await tool.execute(args, context);
  const output = typeof result === "string" ? result : result.output;
  const newline = output.indexOf("\\n");
  console.log(JSON.stringify({ tool: name, envelope: JSON.parse(newline === -1 ? output : output.slice(0, newline)),
    notice: newline === -1 ? null : output.slice(newline + 1) }));
}
await call("start-mixed", work_start, {start_mixed});
await call("start-corrected", work_start, {start_corrected});
{claim_calls}
"""

ADAPTER_DRIVER = """
import { configureCoreBinary } from "{adapter}/dispatch.ts";
import { hostControlPlane } from "{adapter}/move-session.ts";
import { configureConcordAdapter, work_start } from "{adapter}/concord.ts";
configureCoreBinary("concord");
hostControlPlane().bind({
  get: async () => ({ data: { id: "parity-session", directory: "/synthetic/repo", metadata: { "concord.task_scope": "managed" } }, response: new Response(null, { status: 200 }) }),
  post: async () => ({ response: new Response(null, { status: 204 }) }),
  patch: async () => ({ response: new Response(null, { status: 204 }) }),
});
const calls = [];
configureConcordAdapter({ runner: { async run(argv, input) {
  calls.push(argv[1]);
  if (argv[1] === "project-resolve") return { exitCode: 0, stdout: JSON.stringify({ project_id: "synthetic-project", product_ids: ["synthetic-product"], scope_version: "1", main_worktree: true }), stderr: "" };
  if (argv[1] === "work-resume") return { exitCode: 2, stdout: "", stderr: {dirty_stderr} };
  throw new Error("unexpected command " + argv.join(" "));
} } });
const context = { sessionID: "parity-session", messageID: "m1", agent: "coordinator-probe",
  directory: "/synthetic/repo", worktree: "/synthetic/repo", abort: new AbortController().signal };
async function call(name, args) {
  const before = calls.length;
  const result = await work_start.execute(args, context);
  const output = typeof result === "string" ? result : result.output;
  const newline = output.indexOf("\\n");
  console.log(JSON.stringify({ tool: name, envelope: JSON.parse(newline === -1 ? output : output.slice(0, newline)), children: calls.slice(before) }));
}
await call("start-mixed", {start_mixed});
await call("start-corrected", {start_corrected});
"""


def run_bun(root, driver_source):
    require("bun", "executing the adapter and recording-double boundaries")
    driver = root / "driver.ts"
    driver.write_text(driver_source)
    return subprocess.run(["bun", str(driver)], capture_output=True, text=True,
                          timeout=180, cwd=str(root))


class ProductionAdapterBoundaryTests(unittest.TestCase):
    """The real adapter module answers for the common synthetic start inputs."""

    def observed(self):
        return execute_adapter_corpus()

    def test_mixed_resume_fields_serve_the_adapter_input_refusal_before_effects(self):
        envelope = self.observed()["start-mixed"]["envelope"]
        self.assertEqual(envelope.get("outcome"), "error")
        captured = semantic_envelope(envelope)
        triple = {key: captured["error"][key] for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
        self.assertTrue(structural_equal(triple, PRODUCTION_START_INPUT_REFUSAL),
                        json.dumps(captured, indent=1))
        self.assertIsInstance(captured["error"]["message"], str)
        # The shape refusal precedes every effect: no effect-bearing child
        # command ran (project-resolve is a permitted context read).
        children = self.observed()["start-mixed"]["children"]
        self.assertNotIn("work-resume", children)

    def test_corrected_resume_passes_the_input_boundary_and_classifies_the_refusal_exit(self):
        observation = self.observed()
        corrected = observation["start-corrected"]["envelope"]
        # Input admitted: the adapter reached the work-resume child for the
        # corrected shape, and the typed refusal exit is classified as the
        # genuine non-retry refusal.
        self.assertIn("work-resume", observation["start-corrected"]["children"])
        self.assertEqual(corrected.get("outcome"), "error")
        captured = semantic_envelope(corrected)
        triple = {key: captured["error"][key] for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
        self.assertTrue(structural_equal(triple, PRODUCTION_RESUME_REFUSAL),
                        json.dumps(captured, indent=1))


class RecordingDoubleParityTests(unittest.TestCase):
    """The recording double, executed on the same corpus, mirrors production
    envelope for envelope, diagnostic for diagnostic, on every shared input."""

    @classmethod
    def setUpClass(cls):
        cls.sdk_tool = plugin_tool_module()

    def double_observation(self):
        root = Path(tempfile.mkdtemp(prefix="concord-parity-double-"))
        self.addCleanup(shutil.rmtree, root, ignore_errors=True)
        case = {
            "start": SCENARIOS["default-checkout-resume"]["start"],
            "transition": SCENARIOS["same-repository-second-project"]["transition"],
            "responses": {},
        }
        sources = run_module.double_sources(root, case,
                                            sdk_tool=self.sdk_tool,
                                            production_concord=ADAPTER / "concord.ts")
        for relative, content in sources.items():
            target = root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content)
        claim_calls = "\n".join(
            f'await call({json.dumps(label)}, work_transition, {json.dumps(args)});'
            for label, args in claim_corpus_args().items())
        driver = (DOUBLE_DRIVER
                  .replace("{tools_import}", json.dumps(str(root / ".opencode/tools/concord.ts")))
                  .replace("{start_mixed}", json.dumps({"work_id": WORK, "title": "Both shapes at once"}))
                  .replace("{start_corrected}", json.dumps({"work_id": WORK}))
                  .replace("{claim_calls}", claim_calls))
        run = run_bun(root, driver)
        if run.returncode != 0:
            raise AssertionError(f"double driver failed:\n{run.stderr}")
        observations = {item["tool"]: item for item in
                        (json.loads(line) for line in run.stdout.splitlines() if line.strip())}
        trace = root / "calls.jsonl"
        observations["trace"] = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
        return observations

    def production_capture(self):
        """The executed production evidence for the same corpus: the captured
        claim envelopes from the core, plus the adapter observation (envelopes
        and the child commands that prove which side of the input boundary
        each start input reached)."""
        root = Path(tempfile.mkdtemp(prefix="concord-parity-core-compare-"))
        self.addCleanup(shutil.rmtree, root, ignore_errors=True)
        captured = execute_core_corpus(root)
        adapter_observation = execute_adapter_corpus()
        return {
            "start-mixed": adapter_observation["start-mixed"]["envelope"],
            "start-corrected": adapter_observation["start-corrected"]["envelope"],
            "start-corrected-children": adapter_observation["start-corrected"]["children"],
            **captured,
        }

    def assert_double_matches_production(self, double_envelope, production_envelope, label):
        double_semantic = semantic_envelope(double_envelope)
        production_semantic = semantic_envelope(production_envelope)
        self.assertTrue(structural_equal(double_semantic, production_semantic),
                        f"{label}: recording double and executed production disagree\n"
                        f"double:\n{json.dumps(double_semantic, indent=1)}\n"
                        f"production:\n{json.dumps(production_semantic, indent=1)}")

    def test_double_serves_the_executed_production_envelope_on_the_shared_corpus(self):
        # The production comparison this repair exists for: for every
        # MALFORMED corpus input, the double's captured semantic envelope —
        # kind, effect, recovery object, retry classification, and the exact
        # diagnostic — equals the envelope the executed production boundary
        # served in this same test run. Constants and source imports prove
        # nothing; only these captured envelopes do.
        double = self.double_observation()
        production = self.production_capture()
        malformed = (label for label in claim_corpus_args() if label != "claim-corrected")
        for label in ("start-mixed", *malformed):
            with self.subTest(corpus=label):
                self.assert_double_matches_production(double[label]["envelope"], production[label], label)

    def test_double_admits_the_corrected_corpus_where_production_admits_it(self):
        # The corrected inputs prove the input boundary, not envelope
        # equality: production's admitted results are real core/adapter
        # payloads while the double's admitted results are the scenario's
        # synthetic fixtures. What must agree is admission itself — the
        # corrected shape passes the input boundary on both sides, never
        # serving the input refusal.
        double = self.double_observation()
        production = self.production_capture()
        with self.subTest(corpus="start-corrected"):
            # Production: the corrected resume reached the work-resume child
            # (past validateWorkStartArgs). Double: admitted, outcome ok.
            self.assertIn("work-resume", production["start-corrected-children"])
            self.assertEqual(double["start-corrected"]["envelope"].get("outcome"), "ok")
        with self.subTest(corpus="claim-corrected"):
            # Production: the corrected claim left the input boundary (the
            # empty-store refusal is the authority gate, not invalid_input).
            # Double: admitted with the move notice.
            production_triple = {key: semantic_envelope(production["claim-corrected"])["error"][key]
                                 for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
            self.assertFalse(structural_equal(production_triple, PRODUCTION_CLAIM_INPUT_REFUSAL))
            self.assertEqual(double["claim-corrected"]["envelope"].get("outcome"), "ok")

    def test_double_corrected_claim_admits_with_notice_and_trace(self):
        observation = self.double_observation()
        corrected = observation["claim-corrected"]["envelope"]
        self.assertEqual(corrected.get("outcome"), "ok")
        self.assertEqual(corrected.get("result", {}).get("path"), "/synthetic/worktrees/synthetic-work-other-project")
        self.assertIn("A turn-move boundary is active", observation["claim-corrected"]["notice"] or "")

    def test_double_trace_records_every_served_envelope_exactly(self):
        # Trace fidelity: calls.jsonl is append-order, so its entries map to
        # the driven corpus one to one. Each entry records the tool, the exact
        # arguments, and exactly the envelope the double served on that
        # call's output line — compared structurally, not by string luck.
        observation = self.double_observation()
        trace = observation["trace"]
        corpus = (
            ("concord_work_start", "start-mixed", {"work_id": WORK, "title": "Both shapes at once"}),
            ("concord_work_start", "start-corrected", {"work_id": WORK}),
            *(("concord_work_transition", label, args)
              for label, args in claim_corpus_args().items()),
        )
        self.assertEqual(len(trace), len(corpus))
        for entry, (tool, label, args) in zip(trace, corpus):
            with self.subTest(corpus=label):
                self.assertEqual(entry.get("tool"), tool)
                self.assertTrue(structural_equal(entry.get("args"), args),
                                f"trace args for {label} do not match the driven input")
                self.assertTrue(structural_equal(entry.get("result"), observation[label]["envelope"]),
                                f"trace entry for {label} does not match the served envelope")

    def test_double_genuine_refusal_fixture_matches_the_production_adapter_classification(self):
        served = SCENARIOS["genuine-refusal-no-fallback"]["start"]["result"]["error"]
        envelope = {"outcome": "error", "error": served}
        captured = semantic_envelope(envelope)
        triple = {key: captured["error"][key] for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
        self.assertTrue(structural_equal(triple, PRODUCTION_RESUME_REFUSAL))


class PluginToolModuleResolverTests(unittest.TestCase):
    """The owning plugin resolver: which module it actually selects, and
    that the selected module's package metadata proves the tracked pin.

    Cache and installer negatives run against synthetic scratch fixtures
    with a mocked installer; the cold-install and this-checkout tests run
    the real isolated pinned install.
    """

    def scratch_repo(self, metadata_name=None, metadata_version=None,
                     metadata_raw=None, with_module=True, with_metadata=True):
        """A scratch repository whose .opencode cache holds one plugin
        package with the given metadata shape."""
        repo = Path(tempfile.mkdtemp(prefix="concord-parity-resolver-"))
        self.addCleanup(shutil.rmtree, repo, ignore_errors=True)
        package_root = pinned_package_root(repo / ".opencode" / "node_modules")
        if with_module:
            (package_root / "dist").mkdir(parents=True, exist_ok=True)
            (package_root / "dist" / "tool.js").write_text("// scratch plugin module\n")
        if with_metadata:
            raw = metadata_raw if metadata_raw is not None else json.dumps({
                "name": metadata_name or PINNED_PLUGIN_NAME,
                "version": metadata_version or PINNED_PLUGIN_VERSION})
            package_root.joinpath("package.json").write_text(raw)
        return repo

    def fake_installer(self, *, returncode=0, stderr="", name=None,
                       version=None, with_module=True, with_metadata=True):
        """A mocked bun-install outcome: lays the scratch install layout
        under the --cwd root the resolver chose, then reports the given
        install result."""
        def install(argv, **kwargs):
            cwd = Path(argv[argv.index("--cwd") + 1])
            package_root = pinned_package_root(cwd / "node_modules")
            if with_module or with_metadata:
                package_root.mkdir(parents=True, exist_ok=True)
            if with_module:
                (package_root / "dist").mkdir(parents=True, exist_ok=True)
                (package_root / "dist" / "tool.js").write_text("// scratch installed module\n")
            if with_metadata:
                package_root.joinpath("package.json").write_text(
                    json.dumps({"name": name or PINNED_PLUGIN_NAME,
                                "version": version or PINNED_PLUGIN_VERSION}))
            return subprocess.CompletedProcess(argv, returncode, "", stderr)
        return install

    def local_module(self, repo):
        return pinned_package_root(repo / ".opencode" / "node_modules") / "dist" / "tool.js"

    def assert_module_proves_the_pin(self, module):
        """The selected module's own package metadata must prove the exact
        tracked package name and version — read independently of the
        resolver helpers, from the module the resolver returned."""
        expected = next(p for p in PIN["packages"] if p["name"] == "@opencode-ai/plugin")
        metadata = json.loads(module.parents[1].joinpath("package.json").read_text())
        self.assertEqual(metadata.get("name"), expected["name"])
        self.assertEqual(metadata.get("version"), expected["version"])
        return metadata

    def test_matching_local_cache_is_selected_without_any_install(self):
        import unittest.mock as mock
        repo = self.scratch_repo()
        with mock.patch.object(subprocess, "run",
                               side_effect=AssertionError("a proving cache must not trigger an install")):
            module = plugin_tool_module(repo=repo)
        self.assertEqual(module, self.local_module(repo))
        self.assert_module_proves_the_pin(module)

    def test_wrong_version_local_cache_falls_back_to_the_pinned_install(self):
        import unittest.mock as mock
        repo = self.scratch_repo(metadata_version="1.18.34")
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()):
            module = plugin_tool_module(repo=repo)
        self.assertNotEqual(module, self.local_module(repo))
        self.assert_module_proves_the_pin(module)

    def test_wrong_name_local_cache_falls_back_to_the_pinned_install(self):
        import unittest.mock as mock
        repo = self.scratch_repo(metadata_name="@other/plugin")
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()):
            module = plugin_tool_module(repo=repo)
        self.assertNotEqual(module, self.local_module(repo))
        self.assert_module_proves_the_pin(module)

    def test_missing_local_metadata_falls_back_to_the_pinned_install(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()):
            module = plugin_tool_module(repo=repo)
        self.assertNotEqual(module, self.local_module(repo))
        self.assert_module_proves_the_pin(module)

    def test_malformed_local_metadata_falls_back_to_the_pinned_install(self):
        import unittest.mock as mock
        for raw in ("{ not json", '["not", "an", "object"]', '"a string"'):
            with self.subTest(metadata=raw):
                repo = self.scratch_repo(metadata_raw=raw)
                with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()):
                    module = plugin_tool_module(repo=repo)
                self.assertNotEqual(module, self.local_module(repo))
                self.assert_module_proves_the_pin(module)

    def test_unreadable_local_metadata_falls_back_to_the_pinned_install(self):
        import unittest.mock as mock
        repo = self.scratch_repo()
        target = pinned_package_root(repo / ".opencode" / "node_modules") / "package.json"
        original_read_text = Path.read_text

        def unreadable(self, *args, **kwargs):
            if self == target:
                raise PermissionError("scratch metadata is unreadable")
            return original_read_text(self, *args, **kwargs)
        with mock.patch.object(Path, "read_text", unreadable), \
                mock.patch.object(subprocess, "run", side_effect=self.fake_installer()):
            module = plugin_tool_module(repo=repo)
        self.assertNotEqual(module, self.local_module(repo))
        self.assert_module_proves_the_pin(module)

    def test_invalid_encoding_local_metadata_uses_verified_fallback(self):
        import unittest.mock as mock
        repo = self.scratch_repo()
        package = pinned_package_root(repo / ".opencode" / "node_modules")
        (package / "package.json").write_bytes(b"\xff")
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()) as install:
            module = plugin_tool_module(repo=repo)
        self.assertEqual(install.call_count, 1)
        self.assertNotEqual(module, self.local_module(repo))
        self.assert_module_proves_the_pin(module)

    def test_fallback_malformed_metadata_fails_explicitly(self):
        import unittest.mock as mock
        for raw in (b"\xff", b"{ not json", b"[]", b'"not an object"'):
            with self.subTest(metadata=raw):
                repo = self.scratch_repo(with_module=False, with_metadata=False)
                installer = self.fake_installer()

                def install(argv, **kwargs):
                    result = installer(argv, **kwargs)
                    root = Path(argv[argv.index("--cwd") + 1])
                    package = pinned_package_root(root / "node_modules")
                    (package / "package.json").write_bytes(raw)
                    return result

                with mock.patch.object(subprocess, "run", side_effect=install):
                    with self.assertRaisesRegex(AssertionError, "malformed package metadata"):
                        plugin_tool_module(repo=repo)

    def test_fallback_unreadable_metadata_fails_explicitly(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        original_read = Path.read_text

        def unreadable(path, *args, **kwargs):
            if path.name == "package.json" and path.parent.name == "plugin":
                raise PermissionError("synthetic installed metadata is unreadable")
            return original_read(path, *args, **kwargs)

        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()), \
                mock.patch.object(Path, "read_text", unreadable):
            with self.assertRaisesRegex(AssertionError, "unreadable package metadata"):
                plugin_tool_module(repo=repo)

    def test_absent_local_module_takes_the_install_route(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer()) as install:
            module = plugin_tool_module(repo=repo)
        self.assertTrue(install.called)
        self.assert_module_proves_the_pin(module)

    def test_fallback_install_failure_fails_explicitly(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer(
                returncode=1, stderr="offline: registry unreachable")):
            with self.assertRaisesRegex(AssertionError, "plugin"):
                plugin_tool_module(repo=repo)

    def test_fallback_without_a_module_fails_explicitly(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer(with_module=False)):
            with self.assertRaisesRegex(AssertionError, "no dist/tool.js"):
                plugin_tool_module(repo=repo)

    def test_fallback_with_the_wrong_version_fails_explicitly(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer(version="1.18.34")):
            with self.assertRaisesRegex(AssertionError, "1.18.23"):
                plugin_tool_module(repo=repo)

    def test_fallback_with_the_wrong_name_fails_explicitly(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer(name="@other/plugin")):
            with self.assertRaisesRegex(AssertionError, "wrong package"):
                plugin_tool_module(repo=repo)

    def test_fallback_with_missing_metadata_fails_explicitly(self):
        import unittest.mock as mock
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        with mock.patch.object(subprocess, "run", side_effect=self.fake_installer(with_metadata=False)):
            with self.assertRaisesRegex(AssertionError, "metadata"):
                plugin_tool_module(repo=repo)

    def test_cold_scratch_install_proves_the_tracked_pin(self):
        repo = self.scratch_repo(with_module=False, with_metadata=False)
        module = plugin_tool_module(repo=repo)
        self.assertTrue(module.is_file())
        self.assertIn("concord-parity-plugin-", str(module))
        self.assert_module_proves_the_pin(module)

    def test_selected_module_on_this_checkout_proves_the_tracked_pin(self):
        expected = next(p for p in PIN["packages"] if p["name"] == "@opencode-ai/plugin")
        module = plugin_tool_module()
        metadata = json.loads(module.parents[1].joinpath("package.json").read_text())
        self.assertEqual(metadata.get("name"), expected["name"])
        self.assertEqual(metadata.get("version"), expected["version"])
        host_package = pinned_package_root(REPO / ".opencode" / "node_modules")
        host_state, host_metadata = plugin_metadata_state(host_package)
        if host_state != "ok" or (host_metadata.get("name"), host_metadata.get("version")) != (expected["name"], expected["version"]):
            self.assertNotEqual(
                module, host_package / "dist" / "tool.js",
                "a host cache that does not prove the tracked pin must not be selected")

    def test_checkout_selection_assertion_handles_malformed_host_metadata(self):
        import unittest.mock as mock
        fallback_repo = self.scratch_repo()
        verified_module = self.local_module(fallback_repo)
        for raw in (b"\xff", b"[]", b"{ not json"):
            with self.subTest(metadata=raw):
                repo = self.scratch_repo()
                package = pinned_package_root(repo / ".opencode" / "node_modules")
                (package / "package.json").write_bytes(raw)
                with mock.patch(__name__ + ".REPO", repo), \
                        mock.patch(__name__ + ".plugin_tool_module", return_value=verified_module):
                    self.test_selected_module_on_this_checkout_proves_the_tracked_pin()


class PrerequisiteFailureTests(unittest.TestCase):
    """Epoch9 P2: a missing prerequisite fails the run explicitly. A skip
    would report green without executing the boundary."""

    def test_missing_bun_fails_the_double_execution_explicitly(self):
        import unittest.mock as mock
        with mock.patch.object(shutil, "which", lambda name: None):
            with self.assertRaises(AssertionError):
                run_bun(Path(tempfile.mkdtemp(prefix="concord-parity-prereq-")), "void")

    def test_missing_plugin_dependency_fails_explicitly(self):
        import unittest.mock as mock
        original_is_file = Path.is_file

        def no_local_plugin(self):
            return False if self.parts[-3:] == ("@opencode-ai", "plugin", "dist") or self.name == "tool.js" and "plugin" in self.parts else original_is_file(self)
        with mock.patch.object(shutil, "which", lambda name: "/usr/bin/bun" if name == "bun" else None):
            with mock.patch.object(Path, "is_file", no_local_plugin):
                with mock.patch.object(subprocess, "run") as run:
                    run.return_value = subprocess.CompletedProcess([], 1, "", "offline: registry unreachable")
                    with self.assertRaisesRegex(AssertionError, "plugin"):
                        plugin_tool_module()

    def test_no_skip_decorators_or_skiptest_calls_remain(self):
        source = Path(__file__).read_text()
        # The banned tokens are assembled from parts so this test's own
        # assertions are not the match.
        for token in ("Skip" + "Test", "@unittest" + ".skip", "skip" + "Test(",
                      "/tmp/" + "opencode", str(Path.home() / ".config")):
            self.assertNotIn(token, source)


if __name__ == "__main__":
    unittest.main()
