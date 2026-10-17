import { afterEach, beforeEach, expect, test } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter } from "./concord"
import { configureCoreBinary } from "./dispatch"
import { configureHostLease } from "./host-lease"
import { configureCiWatch } from "./ci-watch"
import { armedClaimedWorktree, resetClaimedWorktrees, unlandedClaimedWorktree } from "./claimed-worktree"
import { hostControlPlane, MOVE_SESSION_ROUTE } from "./move-session"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"

const sessionID = "session-start-continuation"
const origin = "/synthetic/default"
const claimed = "/synthetic/claimed"
const args = { title: "Work", value_statement: "Start work", kind: "task", task: "Do work", idempotency_key: "start-continuation" }
const context = (directory = origin, messageID = "message-original") => ({
  sessionID, messageID, agent: "concord-1", directory, worktree: directory, abort: new AbortController().signal,
}) as any

let plugin: any
let stateDirectory: string
let metadata: Record<string, unknown>
let directory: string
let status: string
let posts: Array<{ url: string; body?: any }>
let bootstrapKeys: string[]
let messages: any[]

beforeEach(async () => {
  stateDirectory = mkdtempSync(join(tmpdir(), "concord-start-continuation-"))
  metadata = {}
  directory = origin
  status = "busy"
  posts = []
  bootstrapKeys = []
  messages = [
    { info: { id: "user-original", role: "user", time: { created: 1 } }, parts: [{ type: "text", text: "Start work" }] },
    { info: { id: "message-original", role: "assistant", parentID: "user-original", time: { created: 2, completed: 3 } }, parts: [{ type: "tool" }] },
  ]
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  configureCoreBinary("concord")
  configureCiWatch({ reset: true, stateDir: stateDirectory })
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
  plugin = await ConcordAdapterPlugin({ client: { _client: client } as never })
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
        default:
          throw new Error(`unexpected core command: ${argv.join(" ")}`)
      }
      return { exitCode: 0, stdout: JSON.stringify(answer), stderr: "" }
    },
  } })
})

afterEach(async () => {
  await plugin?.dispose?.()
  configureCiWatch({ reset: true })
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  hostControlPlane().bind(undefined)
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  rmSync(stateDirectory, { recursive: true, force: true })
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
  expect(refused.error.effect_state).toBe("none")
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
  await plugin.event({ event: { type: "session.idle", properties: { sessionID } } })
  await new Promise<void>(resolve => setImmediate(resolve))
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(bootstrapKeys).toEqual([args.idempotency_key])
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})

test.each(["operator", "error", "cancelled", "wrong-session", "vacated"])("suppresses the wake when %s supersedes the pending move", async mode => {
  expect((await start()).error.kind).toBe("session_directory_mismatch")
  if (mode === "operator") await plugin["chat.message"]({ sessionID, messageID: "user-new" }, { parts: [{ type: "text", text: "Stop" }] })
  if (mode === "error" || mode === "cancelled") await plugin.event({ event: { type: "session.error", properties: { sessionID, error: { name: mode === "cancelled" ? "MessageAbortedError" : "UnknownError" } } } })
  if (mode === "vacated") resetClaimedWorktrees()
  status = "idle"
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: mode === "wrong-session" ? "other-session" : sessionID } } })
  await new Promise<void>(resolve => setImmediate(resolve))
  expect(posts.filter(post => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
  expect(armedClaimedWorktree(sessionID)).toBeNull()
})
