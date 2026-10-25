import { expect, test } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { directoryIdentity } from "./dispatch-window"
import type { AgentLanePacket } from "./dispatch"
import type { WorkerCapabilityContext } from "./lane_capabilities"
import { canonicalWorkerControl, workerCheckTool } from "./worker_control"
import vector from "./worker-control-vector.json"

function fixture() {
  const lane = agentLanes.find((entry) => entry.id === "verify")!
  const packet: AgentLanePacket = {
    schema_version: "2.0", attempt_id: "check-attempt", work_id: "check-work", step_id: "verify",
    lane_id: lane.id, lane_digest: lane.digest,
    inputs: { task: "Run the recorded control", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "commands" },
      worker_job: { job_id: "job:check", revision: 1, digest: "sha256:" + "d".repeat(64), objective: "Check", stopping_condition: "Result recorded", project_scope: "project-check", path_scope: [], predicate_ids: [], checks: ["go test ./..."], prerequisites: [], unresolved_refs: [], reserved_integration: "",
        acceptance_oracle: { owners: [], cases: [], controls: [{ control_id: "control:check", owner_id: "owner:check", predicate_ids: [], case_ids: [], recipe_source: { kind: "repository_file", project_id: "project-check", path: "control.json", commit_oid: "a".repeat(40) }, argv: ["recorded-runner"], cwd: ".", expected_result: "pass", required_evidence_role: "independently_executed", readiness_evidence_refs: ["native/prepared"] }] } },
    },
  }
  const worker: WorkerCapabilityContext = { lane, sessionID: "child", parentSessionID: "parent", dispatch: { packet, packetDigest: "sha256:" + "a".repeat(64), workerDirectory: process.cwd(), workerDirectoryIdentity: directoryIdentity(process.cwd())! } }
  const requests: unknown[][] = []
  const permissions: unknown[] = []
  const state = { authorized: true }
  const context = { sessionID: "child", messageID: "message", agent: "concord-verify", directory: process.cwd(), abort: new AbortController().signal, ask: async (value: unknown) => { permissions.push(value) } }
  const tool = workerCheckTool({ authorize: async () => state.authorized ? worker : null }, async (...args) => { requests.push(args); return { oracle: { qualification: "fail" }, operation_ref: "native/receipt" } })
  return { worker, requests, permissions, state, context, tool }
}

test("worker execution framing matches the Go vector including UTF-8 and delimiter bytes", () => {
  expect(new TextDecoder().decode(canonicalWorkerControl(vector.assertion))).toBe(vector.canonical)
})

test("a recorded check uses only held bindings and preserves the producer's non-green result", async () => {
  const f = fixture()
  const result = JSON.parse(await f.tool.execute({ control_id: "control:check" }, f.context))
  expect(result.oracle.qualification).toBe("fail")
  expect(f.requests).toEqual([[{ work_id: "check-work", project_id: "project-check", attempt_id: "check-attempt", worktree_identity: f.worker.dispatch.workerDirectoryIdentity, packet: f.worker.dispatch.packet, control_id: "control:check" }, f.context, f.worker.dispatch.packetDigest, process.cwd()]])
  expect(f.permissions).toEqual([{ permission: "concord_worker_check", patterns: ["control:check"], always: ["*"], metadata: { control_id: "control:check" } }])
})

test("the selected control's Project owns execution independently of the job's primary Project", async () => {
  const f = fixture()
  f.worker.dispatch.packet.inputs.worker_job!.project_scope = "project-primary"
  await f.tool.execute({ control_id: "control:check" }, f.context)
  expect(f.requests[0][0]).toMatchObject({ project_id: "project-check" })
})

test("a control without its recorded repository Project grants no execution", async () => {
  const f = fixture()
  delete f.worker.dispatch.packet.inputs.worker_job!.acceptance_oracle!.controls[0].recipe_source.project_id
  await expect(f.tool.execute({ control_id: "control:check" }, f.context)).rejects.toThrow("repository Project backing")
  expect(f.requests).toHaveLength(0)
  expect(f.permissions).toHaveLength(0)
})

test("arbitrary execution, replacement identities, and invalid selectors never reach the host", async () => {
  for (const extra of [{ argv: ["anything"] }, { cwd: "/other" }, { packet: {} }, { work_id: "foreign" }, { runner: "other" }, { control_id: "" }, { control_id: "has space" }, { control_id: "é".repeat(65) }]) {
    const f = fixture()
    await expect(f.tool.execute({ control_id: "control:check", ...extra }, f.context)).rejects.toThrow("requires only")
    expect(f.requests).toHaveLength(0)
  }
})

test("ordinary checks strings and missing oracle backing never become commands", async () => {
  for (const id of ["go test ./...", "control:unrecorded"]) {
    const f = fixture()
    await expect(f.tool.execute({ control_id: id }, f.context)).rejects.toThrow()
    expect(f.requests).toHaveLength(0)
  }
  const f = fixture()
  delete f.worker.dispatch.packet.inputs.worker_job!.acceptance_oracle
  await expect(f.tool.execute({ control_id: "control:check" }, f.context)).rejects.toThrow("no recorded executable backing")
})

test("root, retired, research, and foreign-directory callers cannot run controls", async () => {
  for (const change of [(f: ReturnType<typeof fixture>) => { f.state.authorized = false }, (f: ReturnType<typeof fixture>) => { f.worker.lane = agentLanes.find((lane) => lane.id === "research")! }, (f: ReturnType<typeof fixture>) => { f.context.directory = "/missing-worker-directory" }]) {
    const f = fixture()
    change(f)
    await expect(f.tool.execute({ control_id: "control:check" }, f.context)).rejects.toThrow()
    expect(f.requests).toHaveLength(0)
  }
})

test("host permission and cancellation remain upper bounds on recorded execution", async () => {
  const denied = fixture()
  denied.context.ask = async () => { throw new Error("operator denied") }
  await expect(denied.tool.execute({ control_id: "control:check" }, denied.context)).rejects.toThrow("operator denied")
  expect(denied.requests).toHaveLength(0)
  const canceled = fixture()
  const abort = new AbortController()
  canceled.context.abort = abort.signal
  canceled.context.ask = async () => { abort.abort(new Error("worker canceled")) }
  await expect(canceled.tool.execute({ control_id: "control:check" }, canceled.context)).rejects.toThrow("worker canceled")
  expect(canceled.requests).toHaveLength(0)
})
