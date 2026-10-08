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
PINNED_PLUGIN_VERSION = next(
    package["version"] for package in PIN["packages"] if package["name"] == "@opencode-ai/plugin")

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


def require(command, purpose):
    """An explicit prerequisite: the named command must exist, or the test
    fails with the reason. Never a skip — a skip turns a missing boundary
    into silent green."""
    path = shutil.which(command)
    if path is None:
        raise AssertionError(f"{purpose} requires {command} on PATH; the boundary cannot be executed")
    return path


def plugin_tool_module():
    """The real @opencode-ai/plugin tool module, through the repository's
    accepted dependency mechanisms only, never a host-installed path.

    Resolution order: the repository-local plugin install
    (.opencode/node_modules, pinned by .opencode/package.json), then an
    isolated stdlib temporary install of the version pinned by the tracked
    .concord/docs/adapter-host-pin.v1.json. Both failures are explicit.
    """
    local = REPO / ".opencode" / "node_modules" / "@opencode-ai" / "plugin" / "dist" / "tool.js"
    if local.is_file():
        return local
    bun = require("bun", "installing the pinned plugin dependency")
    root = Path(tempfile.mkdtemp(prefix="concord-parity-plugin-"))
    (root / "package.json").write_text(json.dumps(
        {"name": "concord-parity-plugin", "private": True,
         "dependencies": {"@opencode-ai/plugin": PINNED_PLUGIN_VERSION}}) + "\n")
    install = subprocess.run([str(bun), "install", "--cwd", str(root)], capture_output=True,
                             text=True, timeout=300)
    module = root / "node_modules" / "@opencode-ai" / "plugin" / "dist" / "tool.js"
    if install.returncode != 0 or not module.is_file():
        raise AssertionError(
            "cannot resolve @opencode-ai/plugin through the repository pin "
            f"({PINNED_PLUGIN_VERSION}): bun install failed with exit {install.returncode}: "
            f"{install.stderr.strip()[:400]}")
    return module


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


def claim_input(**overrides):
    """The corpus claim input: every field except the overridden one is the
    corrected shape production admits at the input boundary."""
    base = {
        "work_id": WORK, "project_id": "synthetic-same-repo-project",
        "base_sha": BASE_SHA, "expected_version": 1, "idempotency_key": "parity-claim-key",
    }
    base.update(overrides)
    return base


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
    return {
        "missing-idempotency_key": invoke_core(
            binary, store, {k: v for k, v in claim_input().items() if k != "idempotency_key"},
            "parity-missing-idem")[1],
        "short-base_sha": invoke_core(binary, store, claim_input(base_sha=SHORT_BASE_SHA),
                                      "parity-short-base")[1],
        "corrected": invoke_core(binary, store, claim_input(), "parity-corrected")[1],
    }


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
        return {
            "missing-idempotency_key": self.invoke(
                {k: v for k, v in claim_input().items() if k != "idempotency_key"},
                "parity-missing-idem")[1],
            "short-base_sha": self.invoke(claim_input(base_sha=SHORT_BASE_SHA), "parity-short-base")[1],
            "corrected": self.invoke(claim_input(), "parity-corrected")[1],
        }
    def test_malformed_claim_corpus_serves_the_core_input_refusal(self):
        for name, envelope in self.captured().items():
            if name == "corrected":
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
        envelope = self.captured()["corrected"]
        self.assertEqual(envelope.get("outcome"), "error")
        captured = semantic_envelope(envelope)
        triple = {key: captured["error"][key] for key in ("kind", "effect_state", "recovery_action", "retry_safe")}
        self.assertFalse(structural_equal(triple, PRODUCTION_CLAIM_INPUT_REFUSAL))

    def test_executed_transport_only_members_are_consistent(self):
        # The named exclusion for the double comparison: the executed core
        # envelope's transport members are per-invocation framing. This check
        # proves they are present and consistent on the executed capture, so
        # excluding them from the double comparison hides no semantic drift.
        envelope = self.captured()["missing-idempotency_key"]
        self.assertEqual(envelope.get("schema_version"), "1.0")
        self.assertEqual(envelope.get("request_id"), "parity-missing-idem")
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
await call("claim-missing-idem", work_transition, {claim_missing_idem});
await call("claim-short-base", work_transition, {claim_short_base});
await call("claim-corrected", work_transition, {claim_corrected});
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
        driver = (DOUBLE_DRIVER
                  .replace("{tools_import}", json.dumps(str(root / ".opencode/tools/concord.ts")))
                  .replace("{start_mixed}", json.dumps({"work_id": WORK, "title": "Both shapes at once"}))
                  .replace("{start_corrected}", json.dumps({"work_id": WORK}))
                  .replace("{claim_missing_idem}", json.dumps(malformed_claim_args()))
                  .replace("{claim_short_base}", json.dumps(short_base_claim_args()))
                  .replace("{claim_corrected}", json.dumps(corrected_claim_args())))
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
            "claim-missing-idem": captured["missing-idempotency_key"],
            "claim-short-base": captured["short-base_sha"],
            "claim-corrected": captured["corrected"],
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
        for label in ("start-mixed", "claim-missing-idem", "claim-short-base"):
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
        self.assertEqual(len(trace), 5)
        corpus = (
            ("concord_work_start", "start-mixed", {"work_id": WORK, "title": "Both shapes at once"}),
            ("concord_work_start", "start-corrected", {"work_id": WORK}),
            ("concord_work_transition", "claim-missing-idem", malformed_claim_args()),
            ("concord_work_transition", "claim-short-base", short_base_claim_args()),
            ("concord_work_transition", "claim-corrected", corrected_claim_args()),
        )
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
