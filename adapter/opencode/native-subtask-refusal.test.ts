import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { dispatchWindows, DispatchWindowError } from "./dispatch-window"
import { hostControlPlane, SESSION_MESSAGES_ROUTE, type RouteClient } from "./move-session"
import { configureHostLease } from "./host-lease"
import { bindCiWatchClient, configureCiWatch } from "./ci-watch"
import { cleanupRefusedSubtask } from "./native-subtask-refusal"

const args = { subagent_type: "concord-research", description: "Bounded research", prompt: "signed packet" }
const sessionID = "ses-refusal"
const subtask = (id: string, messageID: string, prompt = args.prompt) => ({
  id, messageID, sessionID, type: "subtask", agent: args.subagent_type, description: args.description, prompt,
})
const user = (id: string, created: number, parts: any[]) => ({
  info: { id, sessionID, role: "user", time: { created } }, parts,
})
const native = (id: string, created: number, partID: string) => ({
  info: { id, sessionID, role: "assistant", agent: args.subagent_type, mode: args.subagent_type, time: { created } },
  parts: [{ id: partID, messageID: id, sessionID, type: "tool", tool: "task", callID: `call-${partID}`,
    state: { status: "running", input: { ...args }, time: { start: created } } }],
})

async function fixture(messages: any[], options: { pageSize?: number; failPatch?: number } = {}) {
  const plugin = await ConcordAdapterPlugin()
  const writes: any[] = []
  const reads: any[] = []
  const client: RouteClient = {
    async get(request) {
      expect(request.url).toBe(SESSION_MESSAGES_ROUTE)
      expect(request.path).toEqual({ id: sessionID })
      reads.push(request)
      const end = request.query?.before === undefined ? messages.length : Number(request.query.before)
      const start = options.pageSize === undefined ? 0 : Math.max(0, end - options.pageSize)
      return { data: structuredClone(messages.slice(start, end)), response: new Response(null, {
        headers: start > 0 ? { "X-Next-Cursor": String(start) } : {},
      }) }
    },
    async post() { throw new Error("cleanup cannot launch work or inject prompts") },
    async patch(request) {
      writes.push(request)
      if (writes.length === options.failPatch) return { response: new Response(null, { status: 503 }) }
      expect(request.url).toBe("/session/{id}/message/{messageID}/part/{partID}")
      expect(request.path?.id).toBe(sessionID)
      const message = messages.find(m => m.info.id === request.path?.messageID)
      const index = message.parts.findIndex((p: any) => p.id === request.path?.partID)
      expect(index).toBeGreaterThanOrEqual(0)
      message.parts[index] = structuredClone(request.body)
      return { data: structuredClone(request.body), response: new Response(null) }
    },
  }
  hostControlPlane().bind(client)
  return { plugin, writes, reads, client }
}

afterEach(() => {
  hostControlPlane().bind(undefined)
  dispatchWindows().close(sessionID)
  dispatchWindows().takeInFlight(sessionID)
  configureHostLease({ reset: true })
  configureCiWatch({ reset: true })
  bindCiWatchClient(undefined)
})

test("a refused native subtask becomes a visible failure and cannot be collected on the next prompt", async () => {
  const source = user("msg-source", 1, [subtask("prt-source", "msg-source")])
  const assistant = native("msg-assistant", 2, "prt-current")
  const { plugin, writes } = await fixture([source, assistant])
  await expect(plugin["tool.execute.before"]({ tool: "task", sessionID, callID: "prt-current" }, { args: { ...args } }))
    .rejects.toThrow(DispatchWindowError)
  expect(assistant.parts[0].state.status).toBe("error")
  expect((assistant.parts[0].state as any).error).toContain("no open dispatch window")
  expect((assistant.parts[0].state.time as any).end).toBeGreaterThanOrEqual(2)
  expect(source.parts[0].type).toBe("text")
  expect(source.parts[0].text).toContain("no open dispatch window")
  expect(source.parts[0].metadata.concord_refused_subtask.prompt).toBe(args.prompt)
  // OpenCode collects subtask parts after the last finished assistant. Neither
  // message needs an invented finish field once this collection is empty.
  expect([source, assistant].flatMap(m => m.parts).filter(p => p.type === "subtask")).toEqual([])
  expect(writes).toHaveLength(2)
  expect(dispatchWindows().has(sessionID)).toBe(false)
})

test("a refusal repairs earlier stuck replays across message pages, without rewriting unrelated history", async () => {
  const finished = { ...native("msg-finished", 2, "prt-finished"), info: { ...native("msg-finished", 2, "prt-finished").info, finish: "tool-calls" } }
  const historical = user("msg-historical", 1, [subtask("prt-historical", "msg-historical")])
  const source = user("msg-source", 3, [subtask("prt-source", "msg-source")])
  const old = native("msg-old", 4, "prt-old")
  const unrelated = user("msg-other", 5, [subtask("prt-other", "msg-other", "different packet")])
  const ordinary = native("msg-ordinary", 6, "prt-ordinary")
  ordinary.info.agent = ordinary.info.mode = "build"
  const current = native("msg-current", 7, "prt-current")
  const messages = [historical, finished, source, old, unrelated, ordinary, current]
  const { plugin, writes, reads } = await fixture(messages, { pageSize: 2 })
  await expect(plugin["tool.execute.before"]({ tool: "task", sessionID, callID: "prt-current" }, { args: { ...args } }))
    .rejects.toThrow("no open dispatch window")
  expect(old.parts[0].state.status).toBe("error")
  expect(current.parts[0].state.status).toBe("error")
  expect(source.parts[0].type).toBe("text")
  expect(historical.parts[0].type).toBe("subtask")
  expect(finished.parts[0].state.status).toBe("running")
  expect(unrelated.parts[0].type).toBe("subtask")
  expect(ordinary.parts[0].state.status).toBe("running")
  expect(writes).toHaveLength(3)
  expect(reads.length).toBeGreaterThan(1)
})

test("an ordinary Task refusal does not edit native history", async () => {
  const source = user("msg-source", 1, [subtask("prt-source", "msg-source")])
  const assistant = native("msg-assistant", 2, "prt-current")
  const { plugin, writes } = await fixture([source, assistant])
  await expect(plugin["tool.execute.before"]({ tool: "task", sessionID, callID: "model-call" }, { args: { ...args } }))
    .rejects.toThrow(DispatchWindowError)
  expect(writes).toEqual([])
  expect(source.parts[0].type).toBe("subtask")
  expect(assistant.parts[0].state.status).toBe("running")
})

test("a failed cleanup write preserves the authorization refusal and leaves the source available for repair", async () => {
  const source = user("msg-source", 1, [subtask("prt-source", "msg-source")])
  const assistant = native("msg-assistant", 2, "prt-current")
  const { plugin, writes } = await fixture([source, assistant], { failPatch: 1 })
  await expect(plugin["tool.execute.before"]({ tool: "task", sessionID, callID: "prt-current" }, { args: { ...args } }))
    .rejects.toThrow("no open dispatch window")
  expect(writes).toHaveLength(1)
  expect(source.parts[0].type).toBe("subtask")
})

test("cleanup leaves the original in-flight worker alone when its duplicate is refused", async () => {
  const source = user("msg-source", 1, [subtask("prt-source", "msg-source")])
  const active = native("msg-active", 2, "prt-active")
  const current = native("msg-current", 3, "prt-current")
  const { client, writes } = await fixture([source, active, current])
  await cleanupRefusedSubtask(client, { sessionID, callID: "prt-current" }, args, "duplicate refused", "prt-active")
  expect(active.parts[0].state.status).toBe("running")
  expect(current.parts[0].state.status).toBe("error")
  expect(source.parts[0].type).toBe("text")
  expect(writes.map(write => write.path.partID)).toEqual(["prt-current", "prt-source"])
})

test("a partial repair can be retried without rewriting already-failed tasks", async () => {
  const source = user("msg-source", 1, [subtask("prt-source", "msg-source")])
  const current = native("msg-current", 2, "prt-current")
  const messages = [source, current]
  const { plugin, writes } = await fixture(messages, { failPatch: 2 })
  await expect(plugin["tool.execute.before"]({ tool: "task", sessionID, callID: "prt-current" }, { args: { ...args } }))
    .rejects.toThrow(DispatchWindowError)
  expect(current.parts[0].state.status).toBe("error")
  expect(source.parts[0].type).toBe("subtask")
  const failed = structuredClone(current.parts[0])
  const replay = native("msg-replay", 3, "prt-replay")
  messages.push(replay)
  await expect(plugin["tool.execute.before"]({ tool: "task", sessionID, callID: "prt-replay" }, { args: { ...args } }))
    .rejects.toThrow(DispatchWindowError)
  expect(current.parts[0]).toEqual(failed)
  expect(replay.parts[0].state.status).toBe("error")
  expect(source.parts[0].type).toBe("text")
  expect(writes.map(write => write.path.partID)).toEqual(["prt-current", "prt-source", "prt-replay", "prt-source"])
})

test("foreign history and repeated pagination cursors cause no mutations", async () => {
  const current = native("msg-current", 2, "prt-current")
  const { client, writes } = await fixture([current])
  client.get = async () => ({ data: [{ ...current, info: { ...current.info, sessionID: "foreign" } }], response: new Response(null) })
  await expect(cleanupRefusedSubtask(client, { sessionID, callID: "prt-current" }, args, "refused")).rejects.toThrow("malformed session messages")
  client.get = async () => ({ data: [current], response: new Response(null, { headers: { "X-Next-Cursor": "repeated" } }) })
  await expect(cleanupRefusedSubtask(client, { sessionID, callID: "prt-current" }, args, "refused")).rejects.toThrow("repeated message cursor")
  expect(writes).toEqual([])
})
