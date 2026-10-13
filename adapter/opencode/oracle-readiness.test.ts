import { expect, test } from "bun:test"
import { createHash } from "node:crypto"
import { agentLanes } from "./generated-agent-lanes"
import { configureCoreBinary, dispatchWorker, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { verifyPacketOracleReadiness } from "./oracle-readiness"

const oid = "a".repeat(40)
const lane = agentLanes.find((entry) => entry.id === "implement")!
const signal = new AbortController().signal
const controlArgv = ["go", "test", "-count=1", "-run", "^(TestSubject)$", "."]

function packet(subject: string | undefined = oid): AgentLanePacket {
  return {
    schema_version: "1.1", attempt_id: "attempt-subject", lane_id: lane.id,
    lane_version: lane.version, lane_digest: lane.digest, work_id: "work-subject", step_id: "implement",
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

function runner(head = oid, status = "", fail = false): DispatchRunner {
  return { async run(argv) {
    const args = argv.slice(3)
    if (fail) return { exitCode: 128, stdout: "", stderr: "probe failed" }
    return { exitCode: 0, stdout: args[0] === "rev-parse" ? `${head}\n` : args[0] === "status" ? status : args[0] === "ls-tree" ? `100644 blob ${oid}\tharness\n` : "", stderr: "" }
  } }
}

test("readiness leaves subject freshness to the core and runs no git", async () => {
  for (const deps of [runner(), runner("c".repeat(40)), runner(oid, "?? file\0"), runner(oid, " M tracked\0"), runner(oid, "", true), runner("not-an-oid")]) {
    expect(await verifyPacketOracleReadiness(packet(), { runner: deps, directory: "/synthetic/tree", signal })).toBeNull()
  }
})

test("no core-qualified subject requires existing verify recovery", async () => {
  const missing = packet()
  delete missing.inputs.work_context!.subject_commit
  const refusal = await verifyPacketOracleReadiness(missing, { runner: runner(), directory: "/synthetic/tree", signal })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("worktree_verify")
})

test("control Project owns preparation independently of the job primary Project", async () => {
  const candidate = packet()
  candidate.inputs.worker_job!.project_scope = "primary-project"
  expect(await verifyPacketOracleReadiness(candidate, { runner: runner(), directory: "/synthetic/tree", signal })).toBeNull()
})

test("preparation from the primary Project cannot replace the control Project", async () => {
  const candidate = packet()
  candidate.inputs.worker_job!.project_scope = "primary-project"
  candidate.inputs.work_context!.oracle_preparations![0].project_id = "primary-project"
  let probes = 0
  const refusal = await verifyPacketOracleReadiness(candidate, { directory: "/synthetic/tree", signal,
    runner: { async run() { probes++; return { exitCode: 0, stdout: "", stderr: "" } } } })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("preparation")
  expect(probes).toBe(0)
})

test("subject drift and dirty tree authorize; the core owns their refusal", async () => {
  configureCoreBinary("concord-test")
  for (const probes of [runner("c".repeat(40)), runner(oid, "?? file\0"), runner(oid, " M tracked\0")]) {
    let authorizations = 0
    const result = await dispatchWorker(packet(), { runner: probes, workerDirectory: process.cwd(), sessionID: "session-subject", authorize: async () => { authorizations++; return { outcome: "ok" } } })
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
    let probes = 0
    const refusal = await verifyPacketOracleReadiness(candidate, { directory: "/synthetic/tree", signal,
      runner: { async run() { probes++; return { exitCode: 0, stdout: "", stderr: "" } } } })
    expect(refusal).not.toBeNull()
    expect(refusal!.message).toContain("preparation")
    expect(probes).toBe(0)
  }
})

test("qualified preparation admission runs no git and never auto-prepares", async () => {
  const seen: string[][] = []
  expect(await verifyPacketOracleReadiness(packet(), { directory: "/synthetic/tree", signal,
    runner: { async run(argv, input, cancellation) { seen.push(argv); return { exitCode: 0, stdout: "", stderr: "" } } } })).toBeNull()
  expect(seen).toEqual([])
})

test("the adapter readiness runner receives no HEAD or status calls; the core owns subject freshness", async () => {
  const seen: string[][] = []
  expect(await verifyPacketOracleReadiness(packet(), { directory: "/synthetic/tree", signal,
    runner: { async run(argv, input, cancellation) { seen.push(argv); return { exitCode: 0, stdout: "", stderr: "" } } } })).toBeNull()
  for (const argv of seen) {
    expect(["rev-parse", "status"]).not.toContain(argv[3])
  }
})
