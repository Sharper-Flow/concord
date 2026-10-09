import { expect, test } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { configureCoreBinary, dispatchWorker, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { verifyPacketOracleReadiness } from "./oracle-readiness"

const oid = "a".repeat(40)
const lane = agentLanes.find((entry) => entry.id === "implement")!
const signal = new AbortController().signal

function packet(subject: string | undefined = `commit:${oid}`): AgentLanePacket {
  return {
    schema_version: "1.1", attempt_id: "attempt-subject", lane_id: lane.id,
    lane_version: lane.version, lane_digest: lane.digest, work_id: "work-subject", step_id: "implement",
    inputs: {
      task: "Repair the bounded owner.",
      binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" },
      work_context: { source_event_frontier: 0, required_reading: [], findings: [], domain_groups: [], ...(subject === undefined ? {} : { candidate_subject: subject }) },
      worker_job: {
        job_id: "job:subject", revision: 1, digest: `sha256:${"b".repeat(64)}`, objective: "Repair the owner.",
        stopping_condition: "The control passes.", project_scope: "project-1", path_scope: ["internal/store"], predicate_ids: ["predicate:subject"],
        checks: ["true"], prerequisites: [], unresolved_refs: [], reserved_integration: "",
        acceptance_oracle: {
          owners: [{ owner_id: "owner:subject", domain_id: "workflow-engine", mechanism: { project_id: "project-1", path: "internal/store/worktrees.go", entry_point: "VerifyWorktree" }, obligation: "The subject matches the clean commit.", predicate_ids: ["predicate:subject"], law_bindings: [] }],
          cases: [{ case_id: "case:subject", owner_id: "owner:subject", entry_point: "VerifyWorktree", input_class: "clean commit", expected_state: "one receipt-owned subject", control_ids: ["control:subject"] }],
          controls: [{ control_id: "control:subject", owner_id: "owner:subject", predicate_ids: ["predicate:subject"], case_ids: ["case:subject"], recipe_source: { kind: "repository_file", project_id: "project-1", path: "internal/store/worker_oracle_subject_test.go", commit_oid: oid }, argv: ["true"], cwd: ".", expected_result: "pass", required_evidence_role: "reported", readiness_evidence_refs: ["run:fixture"] }],
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

test("core subject must match observed clean HEAD", async () => {
  expect(await verifyPacketOracleReadiness(packet(), { runner: runner(), directory: "/synthetic/tree", signal })).toBeNull()
  for (const deps of [runner("c".repeat(40)), runner(oid, "?? file\0"), runner(oid, " M tracked\0"), runner(oid, "", true), runner("not-an-oid")]) {
    const refusal = await verifyPacketOracleReadiness(packet(), { runner: deps, directory: "/synthetic/tree", signal })
    expect(refusal).not.toBeNull()
    expect(refusal!.message).toContain("worktree_verify")
  }
})

test("no core-qualified subject requires existing verify recovery", async () => {
  const missing = packet()
  delete missing.inputs.work_context!.candidate_subject
  const refusal = await verifyPacketOracleReadiness(missing, { runner: runner(), directory: "/synthetic/tree", signal })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("worktree_verify")
})

test("subject drift and dirty tree refuse before authorization", async () => {
  configureCoreBinary("concord-test")
  for (const probes of [runner("c".repeat(40)), runner(oid, "?? file\0"), runner(oid, " M tracked\0")]) {
    let authorizations = 0
    const result = await dispatchWorker(packet(), { runner: probes, workerDirectory: process.cwd(), sessionID: "session-subject", authorize: async () => { authorizations++; return { outcome: "ok" } } })
    expect(authorizations).toBe(0)
    expect(result.outcome).toBe("error")
    expect(result.error?.message).toContain("worktree_verify")
  }
  const missing = packet()
  delete missing.inputs.work_context!.candidate_subject
  let authorizations = 0
  const result = await dispatchWorker(missing, { runner: runner(), workerDirectory: process.cwd(), sessionID: "session-subject", authorize: async () => { authorizations++; return { outcome: "ok" } } })
  expect(authorizations).toBe(0)
  expect(result.error?.message).toContain("worktree_verify")
})
