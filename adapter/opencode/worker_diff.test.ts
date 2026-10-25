import { expect, test } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { hostToolSchemas } from "./generated-contracts"
import type { AgentLanePacket } from "./dispatch"
import { toolAllowedByCapabilities, type WorkerCapabilityContext } from "./lane_capabilities"
import { workerDiffTool, publishWorkerDiffDefinition, type WorkerDiffRequest } from "./worker_diff"

function fixture() {
  const lane = agentLanes.find(lane => lane.id === "review")!
  const packet: AgentLanePacket = {
    schema_version: "1.0", work_id: "diff-work", attempt_id: "diff-attempt", step_id: "verify", lane_id: lane.id, lane_digest: lane.digest,
    inputs: { task: "Review the committed delta", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "contract_findings" }, work_context: { source_event_frontier: 1, subject_commit: "b".repeat(40), required_reading: [], findings: [], domain_groups: [] } },
  }
  const worker: WorkerCapabilityContext = { lane, sessionID: "child", parentSessionID: "parent", dispatch: { packet, packetDigest: "sha256:" + "c".repeat(64), workerDirectory: process.cwd(), workerDirectoryIdentity: "sha256:" + "d".repeat(64) } }
  const state = { authorized: true, page: { base_commit: "a".repeat(40), subject_commit: "b".repeat(40), content_hash: "sha256:" + "e".repeat(64), offset: 0, next_offset: 6, total_bytes: 6, eof: true, data: Buffer.from("+delta").toString("base64") } as Record<string, unknown> }
  const requests: Array<{ request: WorkerDiffRequest; directory: string }> = []
  const permissions: unknown[] = []
  const context = { sessionID: "child", abort: new AbortController().signal, ask: async (request: unknown) => { permissions.push(request) } }
  const tool = workerDiffTool({ authorize: async () => state.authorized ? worker : null }, async (request, _signal, directory) => { requests.push({ request, directory }); return state.page })
  return { worker, state, requests, permissions, context, tool }
}

test("inspect_diff alone grants its bounded tool, never a general command or reading", () => {
  expect(toolAllowedByCapabilities(["inspect_diff"], "concord_worker_diff")).toBe(true)
  expect(toolAllowedByCapabilities(["read_repository"], "concord_worker_diff")).toBe(false)
  expect(toolAllowedByCapabilities(["inspect_diff"], "bash")).toBe(false)
  expect(toolAllowedByCapabilities(["inspect_diff"], "concord_worker_read")).toBe(false)
})

test("the diff tool forwards only byte selectors plus held host identities", async () => {
  const f = fixture()
  const page = JSON.parse(await f.tool.execute({ offset: 0, length: 6 }, f.context))
  expect(page.text).toBe("+delta")
  expect(f.requests).toEqual([{ request: { offset: 0, length: 6, work_id: "diff-work", attempt_id: "diff-attempt", worktree_identity: f.worker.dispatch.workerDirectoryIdentity, packet: f.worker.dispatch.packet }, directory: process.cwd() }])
  expect(f.permissions).toEqual([{ permission: "concord_worker_diff", patterns: ["*"], always: ["*"], metadata: { offset: 0, length: 6 } }])
  const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await publishWorkerDiffDefinition({ toolID: "concord_worker_diff" }, output)
  expect(output.jsonSchema).toEqual(hostToolSchemas.concord_worker_diff)
})

test("diff callers cannot supply paths, refs, packets, identities, or oversized pages", async () => {
  for (const extra of [{ path: "private.txt" }, { ref: "HEAD" }, { base_commit: "a".repeat(40) }, { packet: {} }, { ordinal: 0 }, { work_id: "other" }, { offset: -1 }, { length: 4097 }]) {
    const f = fixture()
    await expect(f.tool.execute({ offset: 0, length: 6, ...extra }, f.context)).rejects.toThrow("requires only")
    expect(f.requests).toHaveLength(0)
  }
})

test("root, retired, wrong-capability, denied, and canceled calls never reach the diff reader", async () => {
  for (const fault of ["root", "capability", "permission", "cancel"]) {
    const f = fixture()
    if (fault === "root") f.state.authorized = false
    if (fault === "capability") f.worker.lane = agentLanes.find(lane => lane.id === "verify")!
    if (fault === "permission") f.context.ask = async () => { throw new Error("permission denied") }
    if (fault === "cancel") {
      const abort = new AbortController()
      f.context.abort = abort.signal
      f.context.ask = async () => { abort.abort() }
    }
    await expect(f.tool.execute({ offset: 0, length: 6 }, f.context)).rejects.toThrow()
    expect(f.requests).toHaveLength(0)
  }
})

test("diff replies must retain the held head, raw base, closed shape and byte bounds", async () => {
  for (const change of [{ subject_commit: "f".repeat(40) }, { base_commit: "HEAD~1" }, { next_offset: 8 }, { total_bytes: 1 }, { eof: false }, { data: "%%%" }, { extra: "context" }]) {
    const f = fixture()
    f.state.page = { ...f.state.page, ...change }
    await expect(f.tool.execute({ offset: 0, length: 6 }, f.context)).rejects.toThrow()
  }
})
