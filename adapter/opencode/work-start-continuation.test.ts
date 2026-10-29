import { afterEach, beforeEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter } from "./concord"
import { configureCoreBinary } from "./dispatch"
import { configureHostLease } from "./host-lease"
import { manifestDigest } from "./generated-contracts"
import { armedClaimedWorktree, resetClaimedWorktrees, unlandedClaimedWorktree } from "./claimed-worktree"
import { hostControlPlane, MOVE_SESSION_ROUTE } from "./move-session"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"
import { queueWorkStartContinuation, resetWorkStartContinuations, workStartContinuationSettled } from "./work-start-continuation"

const sessionID = "session-start-continuation"
const origin = "/synthetic/default"
const claimed = "/synthetic/claimed"
const args = { title: "Work", value_statement: "Start work", kind: "task", task: "Do work", idempotency_key: "start-continuation" }
const context = (directory = origin, messageID = "message-original") => ({
  sessionID, messageID, agent: "concord-1", directory, worktree: directory, abort: new AbortController().signal,
}) as any

let plugin: any
let metadata: Record<string, unknown>
let directory: string
let status: string
let posts: Array<{ url: string; body?: any }>
let bootstrapKeys: string[]
let landingCalls: string[]
let messages: any[]
let promptStatus: number
let logs: string[]

beforeEach(async () => {
  metadata = {}
  directory = origin
  status = "busy"
  posts = []
  bootstrapKeys = []
  landingCalls = []
  promptStatus = 204
  logs = []
  messages = [
    { info: { id: "user-original", role: "user", time: { created: 1 } }, parts: [{ type: "text", text: "Start work" }] },
    { info: { id: "message-original", role: "assistant", parentID: "user-original", time: { created: 2, completed: 3 } }, parts: [{ type: "tool" }] },
  ]
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  resetWorkStartContinuations()
  configureCoreBinary("concord")
  configureHostLease({ reset: true })
  const client = {
    async get({ url }: any) {
      const data = url === "/session/status" ? { [sessionID]: { type: status } }
        : url === "/session/{id}/message" ? messages
        : { id: sessionID, directory, metadata, agent: "concord-1" }
      return { data, response: new Response(null, { status: 200 }) }
    },
    async post(request: { url: string; body?: any }) {
      posts.push(request)
      if (request.url === MOVE_SESSION_ROUTE) directory = request.body.destination.directory
      if (request.url === "/session/{id}/prompt_async") {
        if (promptStatus !== 204) return { response: new Response(null, { status: promptStatus }) }
        await plugin["chat.message"]({ sessionID, messageID: "user-wake" }, { parts: request.body.parts })
        messages.push(
          { info: { id: "user-wake", role: "user", time: { created: 4 } }, parts: request.body.parts },
          { info: { id: "assistant-wake", role: "assistant", parentID: "user-wake", time: { created: 5, completed: 6 } }, parts: [{ type: "text", text: "Continuing" }] },
        )
      }
      return { response: new Response(null, { status: 204 }) }
    },
    async patch({ body }: any) {
      if (body.metadata !== undefined) metadata = body.metadata
      return { response: new Response(null, { status: 200 }) }
    },
  }
  plugin = await ConcordAdapterPlugin({ client: { _client: client, app: { async log({ body }: { body: { message: string } }) { logs.push(body.message) } } } as never })
  // An unstamped checkout has no release lease to claim. Clear its test-local
  // fault instead of reaching the live store or supplying a forged release.
  configureHostLease({ reset: true })
  configureConcordAdapter({ runner: {
    async run(argv: string[], input: string) {
      const request = JSON.parse(input || "{}")
      let answer: unknown
      switch (argv[1]) {
        case "project-resolve":
          answer = { project_id: "project-1", product_ids: ["product-1"], scope_version: "1", main_worktree: request.directory === origin }
          break
        case "work-bootstrap":
          bootstrapKeys.push(request.idempotency_key)
          answer = { schema_version: "1.0", operation_id: "operation-1", replayed: bootstrapKeys.length > 1, product_id: "product-1", project_id: "project-1", work_id: "work-1", work_version: 1,
            worktree: { set_id: "set-1", path: claimed, branch: "work/work-1", base_sha: "a".repeat(40), state: "active" } }
          break
        case "session-prepare":
          answer = { schema_version: "1.0", agent: request.agent, directory: claimed, product_id: "product-1", work_id: "work-1", title: "Work", prompt: "Do work" }
          break
        case "invoke":
          expect(["worktree_claim", "session_vacate"]).toContain(request.operation)
          answer = { schema_version: "1.0", request_id: `${sessionID}-message-${request.operation === "worktree_claim" ? "claim" : "vacate"}`, origin: "core", tool: "concord_work_transition", operation: request.operation,
            outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, manifest_digest: manifestDigest,
            source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false,
            result: { changed_refs: [], next_valid_intents: [], ...(request.operation === "worktree_claim" ? { path: claimed }
              : { work_id: "work-1", project_id: "project-1", source_directory: claimed, destination_directory: origin }) }, changed_refs: [], next_valid_intents: [] }
          break
        case "claim-landing":
        case "vacate-landing":
          landingCalls.push(argv[1])
          answer = { work_id: request.work_id, already_recorded: false }
          break
        default:
          throw new Error(`unexpected core command: ${argv.join(" ")}`)
      }
      return { exitCode: 0, stdout: JSON.stringify(answer), stderr: "" }
    },
  } })
})

afterEach(async () => {
  await plugin?.dispose?.()
  await workStartContinuationSettled(sessionID)
  resetWorkStartContinuations()
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  hostControlPlane().bind(undefined)
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
})

async function start(ctx = context()) {
  const result = await plugin.tool.concord_work_start.execute(args, ctx)
  await plugin["tool.execute.after"]({ tool: "concord_work_start", sessionID, callID: "start-call", args }, result)
  return JSON.parse(result.output)
}

test("a metadata-only start keeps the placement gate closed", async () => {
  const ctx = context()
  const refused = await start(ctx)
  expect(directory).toBe(claimed)
  expect(ctx.directory).toBe(origin)
  expect(refused.outcome).toBe("error")
  expect(refused.error.kind).toBe("session_directory_mismatch")
  expect(refused.error.effect_state).toBe("partial")
  expect(refused.work_id).toBe("work-1")
  expect(unlandedClaimedWorktree(sessionID)).toBe(claimed)
  expect(armedClaimedWorktree(sessionID)).toBeNull()
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
})

test("a landed replay uses the original request and work identity", async () => {
  expect((await start()).outcome).toBe("error")
  const landed = await start(context(claimed, "message-landed"))
  expect(landed.outcome).toBe("ok")
  expect(landed.work_id).toBe("work-1")
  expect(bootstrapKeys).toEqual([args.idempotency_key, args.idempotency_key])
  expect(unlandedClaimedWorktree(sessionID)).toBeNull()
  expect(armedClaimedWorktree(sessionID)).toBe(claimed)
})

test("a worktree_claim landing retires the pending continuation", async () => {
  expect((await start()).error.kind).toBe("session_directory_mismatch")
  const next = { ...args, idempotency_key: "next-start" }
  expect(queueWorkStartContinuation(next, context(), claimed, "work-1")).toBe(false)
  const result = await plugin.tool.concord_work_transition.execute({ request: { operation: "worktree_claim", input: {
    work_id: "work-1", project_id: "project-1", base_sha: "a".repeat(40), expected_version: 1, idempotency_key: "claim-continuation",
  } } }, context(claimed, "message-claim"))
  const output = typeof result === "string" ? result : result.output
  const outcome: string = JSON.parse(output.split("\n")[0]).outcome
  expect(outcome, output).toBe("ok")
  expect(landingCalls).toEqual(["claim-landing"])
  expect(unlandedClaimedWorktree(sessionID)).toBeNull()
  expect(armedClaimedWorktree(sessionID)).toBe(claimed)
  expect(queueWorkStartContinuation(next, context(), claimed, "work-1")).toBe(true)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
})

test("the real vacate route retires the pending continuation without a wake", async () => {
  expect((await start()).error.kind).toBe("session_directory_mismatch")
  const next = { ...args, idempotency_key: "next-start" }
  expect(queueWorkStartContinuation(next, context(), claimed, "work-1")).toBe(false)
  const result = await plugin.tool.concord_work_transition.execute({ request: {
    operation: "session_vacate", input: { idempotency_key: "vacate-continuation" },
  } }, context(claimed, "message-vacate"))
  const output = typeof result === "string" ? result : result.output
  const outcome: string = JSON.parse(output.split("\n")[0]).outcome
  expect(outcome, output).toBe("ok")
  expect(landingCalls).toEqual(["vacate-landing"])
  expect(directory).toBe(origin)
  expect(unlandedClaimedWorktree(sessionID)).toBeNull()
  expect(armedClaimedWorktree(sessionID)).toBeNull()
  status = "idle"
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(queueWorkStartContinuation(next, context(), claimed, "work-1")).toBe(true)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
  expect(logs).toEqual([])
})

test("an unlanded start wakes its session after idle without an operator message", async () => {
  const refused = await start()
  expect(refused.error.kind).toBe("session_directory_mismatch")
  expect(armedClaimedWorktree(sessionID)).toBeNull()
  status = "idle"
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  // Give an event-owned asynchronous delivery its turn; no chat.message or
  // synthetic landed ToolContext is supplied to manufacture a successful move.
  await new Promise<void>(resolve => setImmediate(resolve))
  const wakes = posts.filter(post => post.url === "/session/{id}/prompt_async")
  expect(wakes).toHaveLength(1)
  expect(wakes[0].body.parts[0].synthetic).toBe(true)
  expect(wakes[0].body.parts[0].text).toContain(JSON.stringify(args))
  expect(wakes[0].body.parts[0].text).toContain("work-1")
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await new Promise<void>(resolve => setImmediate(resolve))
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(bootstrapKeys).toEqual([args.idempotency_key])
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})

test("a trailing separator in the host session directory preserves the continuation", async () => {
  await start()
  directory = `${claimed}/`
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(logs).toEqual([])
})

test.each([origin, null])("an unverified host directory %s suppresses the continuation", async reported => {
  await start()
  directory = reported as any
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
  expect(logs.some(message => message.includes("originating run cannot be verified"))).toBe(true)
})

test("a stale automatic replay spends no second continuation", async () => {
  await start()
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  const refused = await start(context(origin, "assistant-wake"))
  expect(refused.error.kind).toBe("session_directory_mismatch")
  expect(refused.error.message).toContain("already spent")
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(bootstrapKeys).toEqual([args.idempotency_key, args.idempotency_key])
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})

test("concurrent exact replay and duplicate idle events coalesce one wake", async () => {
  const results = await Promise.all([start(), start()])
  expect(results.every(result => result.error.kind === "session_directory_mismatch")).toBe(true)
  await Promise.all([1, 2].map(() => plugin.event({ event: { type: "session.idle", properties: { sessionID } } })))
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(bootstrapKeys).toEqual([args.idempotency_key, args.idempotency_key])
})

test("normal native stream finalization does not cancel a continuation", async () => {
  const controller = new AbortController()
  expect((await start({ ...context(), abort: controller.signal })).error.kind).toBe("session_directory_mismatch")
  controller.abort()
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})

test.each(["originating", "later"])("a persisted %s assistant cancellation suppresses a wake without an error event", async source => {
  expect((await start()).error.kind).toBe("session_directory_mismatch")
  const error = { name: "MessageAbortedError", data: { message: "Aborted" } }
  if (source === "originating") messages[1].info.error = error
  else messages.push({ info: { id: "assistant-cancelled", role: "assistant", parentID: "user-original", time: { created: 4 }, error } })
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})

test.each(["operator", "error", "cancelled", "deleted", "wrong-session", "restarted"])("suppresses the wake when %s supersedes the pending move", async mode => {
  expect((await start()).error.kind).toBe("session_directory_mismatch")
  if (mode === "operator") await plugin["chat.message"]({ sessionID, messageID: "user-new" }, { parts: [{ type: "text", text: "Stop" }] })
  if (mode === "error" || mode === "cancelled") await plugin.event({ event: { type: "session.error", properties: { sessionID, error: { name: mode === "cancelled" ? "MessageAbortedError" : "UnknownError" } } } })
  if (mode === "deleted") await plugin.event({ event: { type: "session.deleted", properties: { info: { id: sessionID } } } })
  if (mode === "restarted") resetClaimedWorktrees()
  status = "idle"
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: mode === "wrong-session" ? "other-session" : sessionID } } })
  await new Promise<void>(resolve => setImmediate(resolve))
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})

test.each([404, 503])("a %i prompt failure is visible, stays unarmed and does not resend", async statusCode => {
  await start()
  promptStatus = statusCode
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(logs.some(message => message.includes(`the host prompt_async route answered ${statusCode}`))).toBe(true)
  expect(armedClaimedWorktree(sessionID)).toBeNull()
  expect(unlandedClaimedWorktree(sessionID)).toBe(claimed)
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
})

test.each([["/synthetic/other", "work-other"], [claimed, "work-other"]])("another target or work identity %s/%s cannot replace the pending continuation", async (directory, workID) => {
  await start()
  expect(queueWorkStartContinuation(args, context(), directory, workID)).toBe(false)
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await workStartContinuationSettled(sessionID)
  const wakes = posts.filter(post => post.url === "/session/{id}/prompt_async")
  expect(wakes).toHaveLength(1)
  expect(wakes[0].body.parts[0].text).toContain(claimed)
  expect(wakes[0].body.parts[0].text).not.toContain("/synthetic/other")
  expect(wakes[0].body.parts[0].text).not.toContain("work-other")
})

test("session deletion retires its continuation record", async () => {
  await start()
  const next = { ...args, idempotency_key: "next-start" }
  expect(queueWorkStartContinuation(next, context(), claimed, "work-1")).toBe(false)
  await plugin.event({ event: { type: "session.deleted", properties: { info: { id: sessionID } } } })
  expect(queueWorkStartContinuation(next, context(), claimed, "work-1")).toBe(true)
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
})
