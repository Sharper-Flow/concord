import { expect, test } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import type { AgentLanePacket } from "./dispatch"
import type { WorkerCapabilityContext } from "./lane_capabilities"
import { workerReadTool, type WorkerReadingRequest } from "./worker_source"

function fixture() {
  const lane = agentLanes.find((entry) => entry.id === "verify")!
  const packet: AgentLanePacket = {
    schema_version: "1.0", attempt_id: "read-attempt", work_id: "read-work", step_id: "verify",
    lane_id: lane.id, lane_digest: lane.digest,
    inputs: { task: "Read the declared source", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "verification_commands" } },
  }
  const worker: WorkerCapabilityContext = {
    dispatch: { packet, packetDigest: "sha256:" + "a".repeat(64), workerDirectory: process.cwd(), workerDirectoryIdentity: "sha256:" + "b".repeat(64) },
    parentSessionID: "parent", sessionID: "child", lane,
  }
  const requests: Array<{ request: WorkerReadingRequest; directory: string }> = []
  const permissions: unknown[] = []
  const context = { sessionID: "child", abort: AbortSignal.timeout(5_000), ask: async (request: unknown) => { permissions.push(request) } }
  const state = { authorized: true, page: {
    ordinal: 0, source_identity: "declared-source", content_hash: "sha256:" + "c".repeat(64),
    offset: 0, next_offset: 6, total_bytes: 6, eof: true, data: Buffer.from("source").toString("base64"),
  } as Record<string, unknown> }
  const tool = workerReadTool({ authorize: async () => state.authorized ? worker : null }, async (request, _signal, directory) => {
    requests.push({ request, directory })
    return state.page
  })
  return { tool, context, worker, state, requests, permissions }
}

test("the reader supplies the held attempt and packet rather than worker-authored identity", async () => {
  const f = fixture()
  const page = JSON.parse(await f.tool.execute({ ordinal: 0, offset: 0, length: 6 }, f.context))
  expect(f.requests).toHaveLength(1)
  expect(f.requests[0]).toEqual({ request: {
    ordinal: 0, offset: 0, length: 6, work_id: "read-work", attempt_id: "read-attempt",
    worktree_identity: f.worker.dispatch.workerDirectoryIdentity, packet: f.worker.dispatch.packet,
  }, directory: process.cwd() })
  expect(page.text).toBe("source")
  expect(page.data).toBe(f.state.page.data)
  expect(f.permissions).toEqual([{ permission: "concord_worker_read", patterns: ["*"], always: ["*"], metadata: { ordinal: 0, offset: 0, length: 6 } }])
})

test("a narrower host permission refuses the reader before the core call", async () => {
  const f = fixture()
  f.context.ask = async () => { throw new Error("operator denied this tool") }
  await expect(f.tool.execute({ ordinal: 0, offset: 0, length: 6 }, f.context)).rejects.toThrow("operator denied")
  expect(f.requests).toHaveLength(0)
})

test("caller-authored paths, execution, identities, and invalid ranges never reach the reader", async () => {
  for (const extra of [{ path: "other.md" }, { ref: "HEAD" }, { command: ["anything"] }, { packet: {} }, { work_id: "foreign" }, { ordinal: -1 }, { offset: -1 }, { length: 4097 }, { length: 0 }]) {
    const f = fixture()
    await expect(f.tool.execute({ ordinal: 0, offset: 0, length: 6, ...extra }, f.context)).rejects.toThrow("requires only")
    expect(f.requests).toHaveLength(0)
  }
})

test("a root caller or a retired worker gets no source bytes", async () => {
  const f = fixture()
  f.state.authorized = false
  await expect(f.tool.execute({ ordinal: 0, offset: 0, length: 6 }, f.context)).rejects.toThrow("active authorized worker")
  expect(f.requests).toHaveLength(0)
})

test("cancellation during host permission leaves the reader uncalled", async () => {
  const f = fixture()
  const abort = new AbortController()
  f.context.abort = abort.signal
  f.context.ask = async () => { abort.abort(new Error("worker canceled")) }
  await expect(f.tool.execute({ ordinal: 0, offset: 0, length: 6 }, f.context)).rejects.toThrow("worker canceled")
  expect(f.requests).toHaveLength(0)
})

test("a split UTF-8 page retains exact bytes without invented replacement text", async () => {
  const f = fixture()
  f.state.page = { ...f.state.page, next_offset: 1, total_bytes: 1, data: Buffer.from([0xc3]).toString("base64") }
  const page = JSON.parse(await f.tool.execute({ ordinal: 0, offset: 0, length: 1 }, f.context))
  expect(page.data).toBe(f.state.page.data)
  expect(Object.hasOwn(page, "text")).toBe(false)
})

test("malformed pages and byte bounds are producer errors, not source content", async () => {
  for (const change of [{ ordinal: 1 }, { offset: 1 }, { next_offset: 8 }, { total_bytes: 2 }, { eof: false }, { data: "%%%" }, { data: "", next_offset: 0, total_bytes: 6, eof: false }, { extra: "context" }]) {
    const f = fixture()
    f.state.page = { ...f.state.page, ...change }
    await expect(f.tool.execute({ ordinal: 0, offset: 0, length: 6 }, f.context)).rejects.toThrow("pinned reader returned")
  }
})
