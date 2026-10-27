import { expect, test } from "bun:test"
import { createHash } from "node:crypto"
import { agentLanes } from "./generated-agent-lanes"
import { configureCoreBinary, dispatchWorker, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { verifyPacketOracleReadiness } from "./oracle-readiness"

const oid = "a".repeat(40)
const lane = agentLanes.find((entry) => entry.id === "implement")!
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }
const controlArgv = ["go", "test", "-count=1", "-run", "^(TestSubject)$", "."]

function packet(subject: string | undefined = oid): AgentLanePacket {
  return {
    schema_version: "1.1", attempt_id: "attempt-subject", lane_id: lane.id,
    lane_digest: lane.digest, work_id: "work-subject", step_id: "implement",
    inputs: {
      task: "Repair the bounded owner.",
      binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" },
      work_context: { source_event_frontier: 0, required_reading: [], findings: [], domain_groups: [], ...(subject === undefined ? {} : { subject_commit: subject }), oracle_preparations: [{
        protocol: "native_oracle_v2", phase: "prepare", qualification: "ready", run_ref: "run:fixture",
        work_id: "work-subject", project_id: "project-1", contract_version: 1, subject_commit: oid,
        bundle_digest: `sha256:${"b".repeat(64)}`, logical_argv_digest: `sha256:${createHash("sha256").update(JSON.stringify(controlArgv)).digest("hex")}`,
        logical_cwd: ".", recipe_source: { kind: "repository_file", project_id: "project-1", path: "testdata/subject_recipe.json", commit_oid: oid },
        manifest_blob: oid, manifest_digest: `sha256:${"b".repeat(64)}`, files: [{ path: "fixture_test.go", blob_oid: oid, sha256: `sha256:${"b".repeat(64)}` }],
        toolchain_identity: "go1.26.7", build_environment_digest: `sha256:${"b".repeat(64)}`, selected_test_names: ["TestSubject"], case_to_test_map: { "case:subject": ["TestSubject"] },
        selected_distinct_count: 1, native_plan_sha256: `sha256:${"b".repeat(64)}`, streams_complete: true,
        stdout: { stream: "stdout", length: 0, sha256: `sha256:${"b".repeat(64)}`, complete: true, ref: "output:stdout" },
        stderr: { stream: "stderr", length: 0, sha256: `sha256:${"b".repeat(64)}`, complete: true, ref: "output:stderr" },
      }] },
      worker_job: {
        job_id: "job:subject", revision: 1, digest: `sha256:${"b".repeat(64)}`, objective: "Repair the owner.",
        stopping_condition: "The control passes.", project_scope: "project-1", path_scope: ["internal/store"], predicate_ids: ["predicate:subject"],
        checks: ["true"], prerequisites: [], unresolved_refs: [], reserved_integration: "",
        acceptance_oracle: {
          owners: [{ owner_id: "owner:subject", domain_id: "workflow-engine", mechanism: { project_id: "project-1", path: "internal/store/worktrees.go", entry_point: "VerifyWorktree" }, obligation: "The subject matches the clean commit.", predicate_ids: ["predicate:subject"], law_bindings: [] }],
          cases: [{ case_id: "case:subject", owner_id: "owner:subject", entry_point: "VerifyWorktree", input_class: "clean commit", expected_state: "one receipt-owned subject", control_ids: ["control:subject"] }],
          controls: [{ control_id: "control:subject", owner_id: "owner:subject", predicate_ids: ["predicate:subject"], case_ids: ["case:subject"], recipe_source: { kind: "repository_file", project_id: "project-1", path: "testdata/subject_recipe.json", commit_oid: oid }, argv: controlArgv, cwd: ".", expected_result: "pass", required_evidence_role: "reported", readiness_evidence_refs: ["run:fixture"] }],
        },
      },
    },
  } as AgentLanePacket
}

const pytestWitness = "tests/test_billing.py::test_invoice_total"
const pytestArgv = ["pytest", pytestWitness]
const checkWitnesses = ["format", "types"]
const checkArgv = ["check", ...checkWitnesses]

function targetPacket(pair: { materializer: string; runner: string } = { materializer: "uv", runner: "pytest" }): AgentLanePacket {
  const candidate = packet()
  const witnesses = pair.runner === "pytest" ? [pytestWitness] : checkWitnesses
  const argv = pair.runner === "pytest" ? pytestArgv : checkArgv
  const control = candidate.inputs.worker_job!.acceptance_oracle!.controls
  control[0].argv = argv
  const preparation = candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>
  delete preparation.selected_test_names
  delete preparation.case_to_test_map
  preparation.logical_argv_digest = `sha256:${createHash("sha256").update(JSON.stringify(argv)).digest("hex")}`
  preparation.recipe_kind = "target_runner_v1"
  preparation.target_materializer = pair.materializer
  preparation.target_runner = pair.runner
  preparation.selected_witness_ids = witnesses
  preparation.case_to_witness_map = { "case:subject": witnesses }
  preparation.selected_distinct_count = witnesses.length
  return candidate
}

function packagePacket(): AgentLanePacket {
  const candidate = packet()
  const control = candidate.inputs.worker_job!.acceptance_oracle!.controls[0]
  control.cwd = "internal/example"
  control.argv = ["go", "test", "-count=1", "."]
  const preparation = candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>
  delete preparation.selected_test_names
  delete preparation.case_to_test_map
  preparation.recipe_kind = "go_package_v1"
  preparation.files = null
  preparation.logical_cwd = control.cwd
  preparation.logical_argv_digest = `sha256:${createHash("sha256").update(JSON.stringify(control.argv)).digest("hex")}`
  preparation.selected_witness_ids = ["go_package:./internal/example"]
  preparation.case_to_witness_map = { "case:subject": ["go_package:./internal/example"] }
  return candidate
}

test("complete-package readiness admits one cwd-bound package witness without a test catalog", async () => {
  expect(await verifyPacketOracleReadiness(packagePacket())).toBeNull()
  const candidate = packagePacket()
  const preparation = candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>
  preparation.case_to_witness_map = {
    "case:subject": ["go_package:./internal/example"],
    "case:other-recipe-case": ["go_package:./internal/example"],
  }
  expect(await verifyPacketOracleReadiness(candidate)).toBeNull()
})

test("qualified package preparation reaches authorization without any readiness subprocess", async () => {
  configureCoreBinary("concord-test")
  let authorizations = 0
  await dispatchWorker(packagePacket(), {
    credentials: testCredentials, runner: runner(), workerDirectory: process.cwd(), sessionID: "session-package",
    authorize: async () => { authorizations++; return { outcome: "ok" } },
  })
  expect(authorizations).toBe(1)
})

test("package readiness keeps the native complete-package command grammar", async () => {
  const candidate = packagePacket()
  const control = candidate.inputs.worker_job!.acceptance_oracle!.controls[0]
  control.argv = ["go", "test", "-count=1", "-run", "TestSubject", "."]
  candidate.inputs.work_context!.oracle_preparations![0].logical_argv_digest = `sha256:${createHash("sha256").update(JSON.stringify(control.argv)).digest("hex")}`
  expect(await verifyPacketOracleReadiness(candidate)).not.toBeNull()
})

test("package readiness refuses mixed, mismatched, empty and fabricated package populations", async () => {
  for (const mutate of [
    (p: Record<string, unknown>) => { p.selected_test_names = ["TestSubject"] },
    (p: Record<string, unknown>) => { p.case_to_test_map = { "case:subject": ["TestSubject"] } },
    (p: Record<string, unknown>) => { p.target_runner = "check" },
    (p: Record<string, unknown>) => { p.target_materializer = "none" },
    (p: Record<string, unknown>) => { p.selected_witness_ids = [] },
    (p: Record<string, unknown>) => { p.selected_witness_ids = ["go_package:./other"] },
    (p: Record<string, unknown>) => { p.selected_witness_ids = ["go_package:./internal/example", "go_package:./internal/example"] },
    (p: Record<string, unknown>) => { p.selected_distinct_count = 2 },
    (p: Record<string, unknown>) => { p.case_to_witness_map = {} },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:other": ["go_package:./internal/example"] } },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:subject": ["go_package:./other"] } },
    (p: Record<string, unknown>) => { p.files = [{ path: "replacement_test.go" }] },
    (p: Record<string, unknown>) => { p.harness_input_count = 33 },
    (p: Record<string, unknown>) => { p.harness_input_digest = `sha256:${"a".repeat(64)}` },
  ]) {
    const candidate = packagePacket()
    mutate(candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>)
    expect(await verifyPacketOracleReadiness(candidate)).not.toBeNull()
  }
})

test("selected nested Go readiness caps the actual witnesses rather than just their roots", async () => {
  const candidate = packet()
  const preparation = candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>
  const control = candidate.inputs.worker_job!.acceptance_oracle!.controls[0]
  for (const count of [40, 41]) {
    const names = Array.from({ length: count }, (_, i) => `TestSubject/scenario-${i}`)
    const cases: Record<string, string[]> = {}
    names.forEach((name, i) => { (cases[`case:group-${Math.floor(i / 8)}`] ??= []).push(name) })
    control.case_ids = Object.keys(cases)
    preparation.selected_test_names = names
    preparation.case_to_test_map = cases
    preparation.selected_distinct_count = count
    expect(await verifyPacketOracleReadiness(candidate) === null).toBe(count === 40)
  }
})

function runner(head = oid, status = ""): DispatchRunner {
  return { async run(argv) {
    const args = argv.slice(3)
    return { exitCode: 0, stdout: args[0] === "rev-parse" ? `${head}\n` : args[0] === "status" ? status : args[0] === "ls-tree" ? `100644 blob ${oid}\tharness\n` : "", stderr: "" }
  } }
}

test("readiness leaves subject freshness to the core", async () => {
  expect(await verifyPacketOracleReadiness(packet())).toBeNull()
})

test("no core-qualified subject requires existing verify recovery", async () => {
  const missing = packet()
  delete missing.inputs.work_context!.subject_commit
  const refusal = await verifyPacketOracleReadiness(missing)
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("worktree_verify")
})

test("control Project owns preparation independently of the job primary Project", async () => {
  const candidate = packet()
  candidate.inputs.worker_job!.project_scope = "primary-project"
  expect(await verifyPacketOracleReadiness(candidate)).toBeNull()
})

test("preparation from the primary Project cannot replace the control Project", async () => {
  const candidate = packet()
  candidate.inputs.worker_job!.project_scope = "primary-project"
  candidate.inputs.work_context!.oracle_preparations![0].project_id = "primary-project"
  const refusal = await verifyPacketOracleReadiness(candidate)
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("preparation")
})

test("subject drift and dirty tree authorize; the core owns their refusal", async () => {
  configureCoreBinary("concord-test")
  for (const probes of [runner("c".repeat(40)), runner(oid, "?? file\0"), runner(oid, " M tracked\0")]) {
    let authorizations = 0
    await dispatchWorker(packet(), { credentials: testCredentials, runner: probes, workerDirectory: process.cwd(), sessionID: "session-subject", authorize: async () => { authorizations++; return { outcome: "ok" } } })
    // Readiness no longer refuses drift: authorization is reached, and the
    // stubbed dispatch may stop later for reasons this check does not own.
    expect(authorizations).toBe(1)
  }
  const missing = packet()
  delete missing.inputs.work_context!.subject_commit
  let authorizations = 0
  const result = await dispatchWorker(missing, { runner: runner(), workerDirectory: process.cwd(), sessionID: "session-subject", authorize: async () => { authorizations++; return { outcome: "ok" } } })
  expect(authorizations).toBe(0)
  expect(result.error?.message).toContain("worktree_verify")
})

test("qualified preparation refs and metadata are mandatory before any probe or authorization", async () => {
  for (const mutation of [
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations = [] },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].qualification = "unavailable" },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].run_ref = "unrelated:run" },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].subject_commit = "c".repeat(40) },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].contract_version = 2 },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].project_id = "foreign" },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].streams_complete = false },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].selected_distinct_count = 0 },
    (p: AgentLanePacket) => { p.inputs.work_context!.oracle_preparations![0].case_to_test_map = {} },
  ]) {
    const candidate = packet()
    mutation(candidate)
    const refusal = await verifyPacketOracleReadiness(candidate)
    expect(refusal).not.toBeNull()
    expect(refusal!.message).toContain("preparation")
  }
})

test("target preparations pass only the exact admitted materializer and runner pairs", async () => {
  for (const pair of [
    { materializer: "uv", runner: "pytest" },
    { materializer: "none", runner: "check" },
    { materializer: "uv", runner: "check" },
    { materializer: "bun", runner: "check" },
  ]) {
    expect(await verifyPacketOracleReadiness(targetPacket(pair))).toBeNull()
  }
  for (const pair of [
    { materializer: "none", runner: "pytest" },
    { materializer: "bun", runner: "pytest" },
    { materializer: "future", runner: "pytest" },
    { materializer: "future", runner: "check" },
    { materializer: "uv", runner: "future" },
    { materializer: "bun", runner: "future" },
    { materializer: "", runner: "check" },
  ]) {
    const refusal = await verifyPacketOracleReadiness(targetPacket(pair))
    expect(refusal).not.toBeNull()
    expect(refusal!.message).toContain("preparation")
  }
})

test("target witness metadata must be real, distinct, and case-complete before any probe", async () => {
  const longWitness = "tests/" + "x".repeat(250) + "/test_too_long.py::test_case"
  for (const mutation of [
    (p: Record<string, unknown>) => { delete p.recipe_kind },
    (p: Record<string, unknown>) => { p.recipe_kind = "target_runner_v2" },
    (p: Record<string, unknown>) => { p.selected_test_names = ["TestSubject"] },
    (p: Record<string, unknown>) => { p.selected_witness_ids = [] },
    (p: Record<string, unknown>) => { p.selected_witness_ids = [pytestWitness, pytestWitness] },
    (p: Record<string, unknown>) => { p.selected_witness_ids = [longWitness] },
    (p: Record<string, unknown>) => { p.selected_witness_ids = ["tests/na\x00l.py::test_case"] },
    (p: Record<string, unknown>) => { p.selected_distinct_count = 2 },
    (p: Record<string, unknown>) => { p.case_to_witness_map = {} },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:other": [pytestWitness] } },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:subject": [] } },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:subject": [pytestWitness, pytestWitness, pytestWitness, pytestWitness, pytestWitness, pytestWitness, pytestWitness, pytestWitness, pytestWitness] } },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:subject": ["checks/undeclared.py::test_case"] } },
    (p: Record<string, unknown>) => { p.case_to_witness_map = { "case:subject": [pytestWitness], "case:extra": [pytestWitness] } },
  ]) {
    const candidate = targetPacket()
    mutation(candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>)
    const refusal = await verifyPacketOracleReadiness(candidate)
    expect(refusal).not.toBeNull()
    expect(refusal!.message).toContain("preparation")
  }
})

test("target-selected witnesses must be exactly the union of the case map", async () => {
  const candidate = targetPacket({ materializer: "none", runner: "check" })
  const preparation = candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>
  preparation.selected_witness_ids = checkWitnesses.concat(["extra"])
  preparation.selected_distinct_count = 3
  const refusal = await verifyPacketOracleReadiness(candidate)
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("preparation")
})

test("target witness limits count UTF-8 bytes rather than characters", async () => {
  for (const [witness, accepted] of [["字".repeat(85) + "a", true], ["字".repeat(86), false]] as const) {
    const candidate = targetPacket({ materializer: "none", runner: "check" })
    const control = candidate.inputs.worker_job!.acceptance_oracle!.controls[0]
    control.argv = ["check", witness]
    const preparation = candidate.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>
    preparation.logical_argv_digest = `sha256:${createHash("sha256").update(JSON.stringify(control.argv)).digest("hex")}`
    preparation.selected_witness_ids = [witness]
    preparation.selected_distinct_count = 1
    preparation.case_to_witness_map = { "case:subject": [witness] }
    const refusal = await verifyPacketOracleReadiness(candidate)
    expect(refusal === null).toBe(accepted)
  }
})

test("a mixed or unknown target shape refuses at the closed packet schema before readiness", async () => {
  configureCoreBinary("concord-test")
  const mixed = targetPacket()
  ;(mixed.inputs.work_context!.oracle_preparations![0] as unknown as Record<string, unknown>).selected_test_names = ["TestSubject"]
  const authorizations = { count: 0 }
  const result = await dispatchWorker(mixed, { runner: runner(), workerDirectory: process.cwd(), sessionID: "session-subject", authorize: async () => { authorizations.count++; return { outcome: "ok" } } })
  expect(authorizations.count).toBe(0)
  expect(result.outcome).toBe("error")
})
