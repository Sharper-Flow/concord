import { afterEach, expect, test } from "bun:test"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { bindCiWatchClient, ciWatchSettled, concord_ci_watch, configureCiWatch, drainQueuedCiReports, type VerbSpawner } from "./ci-watch"
import { configureCoreBinary } from "./dispatch"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { hostControlPlane } from "./move-session"

const SESSION = "watch-session"
const STATE_DIR = fs.mkdtempSync(path.join(os.tmpdir(), "ci-watch-test-"))

type RouteRecord = { url: string; path?: Record<string, unknown>; query?: Record<string, unknown>; body?: unknown }

type FixtureMessage = { info: Record<string, unknown>; parts?: Array<Record<string, unknown>> }

// hostFixture is the fake route client every test binds, shaped on the real
// opencode 1.18.x host: GET /session/{id} returns the session record whose
// model is { id, providerID, variant? }, GET /session/{id}/message returns
// { info, parts } records, and POST /session/{id}/prompt_async answers 204
// with no body — the forked host prompt then persists a user message carrying
// the report text. `reply` controls what follows that injected message:
// "none" (default) never replies, "parented" adds the assistant reply whose
// parentID is the injected message id, "unrelated" adds an assistant reply
// parented to a concurrent message instead.
function hostFixture(
  options: {
    status?: string
    absentStatus?: boolean
    session?: Record<string, unknown>
    messages?: FixtureMessage[]
    promptStatus?: number
    reply?: "parented" | "unrelated" | "none"
    inject?: boolean
  } = {},
) {
  const gets: RouteRecord[] = []
  const posts: RouteRecord[] = []
  let promptCount = 0
  const injected: FixtureMessage[] = []
  const client = {
    get: async (request: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown> }) => {
      gets.push({ url: request.url, path: request.path, query: request.query })
      if (request.url === "/session/status") {
        return {
          data: options.absentStatus ? {} : { [SESSION]: { type: options.status ?? "idle" } },
          response: new Response(null, { status: 200 }),
        }
      }
      if (request.url === "/session/{id}") {
        return {
          data: options.session ?? { id: SESSION, agent: "concord-1", model: { id: "glm-test", providerID: "zai" } },
          response: new Response(null, { status: 200 }),
        }
      }
      if (request.url === "/session/{id}/message") {
        return { data: [...(options.messages ?? []), ...injected], response: new Response(null, { status: 200 }) }
      }
      return { response: new Response(null, { status: 404 }) }
    },
    post: async (request: { url: string; body?: unknown }) => {
      posts.push({ url: request.url, body: request.body })
      if (request.url === "/session/{id}/prompt_async") {
        promptCount++
        const status = options.promptStatus ?? 204
        if (status !== 204) return { response: new Response(null, { status }) }
        if (options.inject !== false) {
          const id = `msg_report_${promptCount}`
          const text = (request.body as { parts?: Array<{ text?: string }> })?.parts?.[0]?.text ?? ""
          injected.push({ info: { id, role: "user" }, parts: [{ type: "text", text }] })
          if (options.reply === "unrelated") {
            injected.push({
              info: { id: `msg_unrelated_${promptCount}`, role: "assistant", parentID: "msg_concurrent_turn" },
              parts: [],
            })
          } else if (options.reply === "parented") {
            injected.push({ info: { id: `msg_reply_${promptCount}`, role: "assistant", parentID: id }, parts: [] })
          }
        }
        return { response: new Response(null, { status: 204 }) }
      }
      return { response: new Response(null, { status: 200 }) }
    },
  }
  return { gets, posts, client }
}

// verbFixture serves one recorded slice per spawn call and hangs once the
// scripted slices run out, so an unintended extra slice cannot silently pass.
function verbFixture(slices: Array<{ stdout: string; stderr?: string; exitCode?: number; delayMs?: number }>) {
  const calls: string[] = []
  const spawner: VerbSpawner = async (_argv, stdin) => {
    calls.push(stdin)
    const next = slices.shift()
    if (next === undefined) return new Promise(() => {})
    if (next.delayMs) await new Promise((resolve) => setTimeout(resolve, next.delayMs))
    return { exitCode: next.exitCode ?? 0, stdout: next.stdout, stderr: next.stderr ?? "" }
  }
  return { calls, spawner }
}

function failingSpawner(failures: number) {
  const calls: string[] = []
  let remaining = failures
  const spawner: VerbSpawner = async (_argv, stdin) => {
    calls.push(stdin)
    if (remaining-- > 0) throw new Error("synthetic spawn failure")
    return { exitCode: 0, stdout: "", stderr: "" }
  }
  return { calls, spawner }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolve_) => {
    resolve = resolve_
  })
  return { promise, resolve }
}

async function until(condition: () => boolean, label: string, ms = 2_000): Promise<void> {
  const deadline = Date.now() + ms
  while (!condition()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`)
    await new Promise((resolve) => setTimeout(resolve, 2))
  }
}

function result(result: { title: string; output: string }): Record<string, unknown> {
  return JSON.parse(result.output) as Record<string, unknown>
}

const context = { sessionID: SESSION, abort: new AbortController().signal }

const watchArgs = { repo: "owner/name", selector: { kind: "pr", value: "12" } }

const pendingSlice = (stateFile: string) => ({
  stdout: JSON.stringify({ status: "pending", state_file: stateFile, iterations: 1 }),
})

const successSlice = {
  stdout: JSON.stringify({ status: "success", checks: { total: 3, passing: 2, failing: 0, skipped: 1, pending: 0 }, iterations: 2 }),
}

async function startWatch(fixture: ReturnType<typeof hostFixture>, args: Record<string, unknown> = watchArgs) {
  const started = await concord_ci_watch.execute(args, context)
  return result(started)
}

afterEach(() => {
  configureCiWatch({ reset: true })
  configureCoreBinary(null)
  bindCiWatchClient(undefined)
  hostControlPlane().bind(undefined)
  configureHostLease({ reset: true })
})

test("a caller the host cannot prove is a coordinator session is refused before anything spawns", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  const parent = { id: "watch-parent", directory: process.cwd(), metadata: { "concord.task_scope": "managed" } }
  fixture.client.get = async (request: { url: string; path?: Record<string, unknown> }) => {
    const id = String(request.path?.id)
    return {
      data: id === SESSION ? { ...parent, id: SESSION, parentID: "watch-parent" } : parent,
      response: new Response(null, { status: 200 }),
    }
  }
  const refused = result(await concord_ci_watch.execute(watchArgs, context))
  expect(refused.status).toBe("refused")
  expect(String(refused.reason)).toContain("managed parent")
  expect(String(refused.reason)).toContain("coordinator surface")
})

test("a host that handed the plugin no client is refused because the report could never be delivered", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(undefined)
  configureCoreBinary("/synthetic/concord")
  const refused = result(await concord_ci_watch.execute(watchArgs, context))
  expect(refused.status).toBe("refused")
  expect(String(refused.reason)).toContain("cannot deliver the terminal report")
})

test("an adapter with no bound core binary is refused at start", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  const refused = result(await concord_ci_watch.execute(watchArgs, context))
  expect(refused.status).toBe("refused")
  expect(String(refused.reason)).toContain("ci-wait verb")
})

test("malformed arguments are refused before a verb child spawns", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  for (const args of [
    { repo: "not-a-repo", selector: { kind: "pr", value: "12" } },
    { repo: "owner/name", selector: { kind: "pr", value: "abc" } },
    { repo: "owner/name", selector: { kind: "branch", value: "main" } },
    { repo: "owner/name", selector: { kind: "pr", value: "12" }, time_seconds_max: 9000 },
    { repo: "owner/name", selector: { kind: "pr", value: "12" }, mode: "poll" },
  ]) {
    const refused = result(await concord_ci_watch.execute(args, context))
    expect(refused.status).toBe("refused")
  }
})

test("the watch returns at once, carries one state file across slices, and prompts only after the terminal slice", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const first = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  const second = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  const slices = [first.promise, second.promise]
  const calls: string[] = []
  const spawner: VerbSpawner = async (_argv, stdin) => {
    calls.push(stdin)
    return slices.shift()!
  }

  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  expect(started.status).toBe("started")
  expect(typeof started.watch_id).toBe("string")
  expect(String(started.state_file)).toContain("ci-wait-watch-")
  // Zero model turns while the wait runs: no prompt has been posted.
  expect(fixture.posts).toEqual([])
  expect(calls).toHaveLength(1)
  expect(JSON.parse(calls[0])).toEqual({ repo: "owner/name", selector: { kind: "pr", value: "12" }, state_file: started.state_file })

  first.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "pending", state_file: started.state_file }), stderr: "" })
  await until(() => calls.length === 2, "the second slice")
  expect(JSON.parse(calls[1])).toEqual({ repo: "owner/name", selector: { kind: "pr", value: "12" }, state_file: started.state_file })
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toEqual([])

  second.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "success", iterations: 2 }), stderr: "" })
  await ciWatchSettled(String(started.watch_id))
  const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(1)
  const body = prompts[0].body as { parts: Array<{ type: string; synthetic?: boolean; text: string }>; agent?: string; model?: { providerID: string; modelID: string } }
  expect(body.parts).toHaveLength(1)
  expect(body.parts[0].type).toBe("text")
  expect(body.parts[0].synthetic).toBe(true)
  expect(body.parts[0].text).toContain('"status": "success"')
  expect(body.agent).toBe("concord-1")
  expect(body.model).toEqual({ providerID: "zai", modelID: "glm-test" })
})

test("an unconfirmed wake queues the report and the next chat.message injects it", async () => {
  const fixture = hostFixture({ messages: [] })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  const { calls, spawner } = verbFixture([{ stdout: JSON.stringify({ status: "failure", reason: "1 of 3 checks failed" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  // The prompt was accepted (204) but no assistant reply followed.
  expect(fixture.posts.some((post) => post.url === "/session/{id}/prompt_async")).toBe(true)
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "warn")
  expect(logged).toHaveLength(1)
  expect(String((logged[0].body as { message: string }).message)).toContain("queued for the next message")

  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_drain_1", output)
  expect(output.parts).toHaveLength(1)
  const drained = output.parts[0] as { id: string; sessionID: string; messageID: string; type: string; synthetic: boolean; text: string }
  expect(drained.synthetic).toBe(true)
  expect(drained.id.startsWith("prt_ciwatch-")).toBe(true)
  expect(drained.sessionID).toBe(SESSION)
  expect(drained.messageID).toBe("msg_drain_1")
  expect(drained.text).toContain('"status": "failure"')
  // The queue is drained exactly once.
  const again = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_drain_2", again)
  expect(again.parts).toEqual([])
  expect(calls).toHaveLength(1)
})

test("a session that stays busy past the idle window queues the report without prompting", async () => {
  const fixture = hostFixture({ status: "busy" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idleTimeoutMs: 30, idlePollMs: 5 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  expect(fixture.posts.some((post) => post.url === "/session/{id}/prompt_async")).toBe(false)
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_drain_3", output)
  expect(output.parts).toHaveLength(1)
  expect((output.parts[0] as { text: string }).text).toContain('"status": "success"')
})

test("a prompt the host refuses is logged and queued, never swallowed", async () => {
  const fixture = hostFixture({ promptStatus: 400 })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  const errors = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "error")
  expect(errors.length).toBeGreaterThanOrEqual(1)
  expect(String((errors[0].body as { message: string }).message)).toContain("prompt_async route answered 400")
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_drain_4", output)
  expect(output.parts).toHaveLength(1)
})

test("consecutive verb slice failures deliver an explicit error report and are logged", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxSliceFailures: 2, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  const { calls, spawner } = failingSpawner(2)
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(1)
  const text = (prompts[0].body as { parts: Array<{ text: string }> }).parts[0].text
  expect(text).toContain('"status": "error"')
  expect(text).toContain("failed 2 consecutive slices")
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "error")
  expect(logged.length).toBeGreaterThanOrEqual(2)
  expect(calls).toHaveLength(2)
})

test("a verb error report re-invokes once with the same state file, then delivers the second error", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  const { calls, spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "error", reason: "gh: auth failure", state_file: "" }) },
    { stdout: JSON.stringify({ status: "error", reason: "gh: transport refused", state_file: "" }) },
  ])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  expect(calls).toHaveLength(2)
  expect(JSON.parse(calls[0]).state_file).toBe(JSON.parse(calls[1]).state_file)
  const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(1)
  expect((prompts[0].body as { parts: Array<{ text: string }> }).parts[0].text).toContain("gh: transport refused")
})

test("a duplicate watch for the same session and selector returns the active identity", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  const held = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  const calls: string[] = []
  const spawner: VerbSpawner = async (_argv, stdin) => {
    calls.push(stdin)
    return held.promise
  }
  configureCiWatch({ spawner })
  const first = await startWatch(fixture)
  const second = await startWatch(fixture)
  expect(second.status).toBe("already_watching")
  expect(second.watch_id).toBe(first.watch_id)
  expect(calls).toHaveLength(1)
  held.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "cancelled" }), stderr: "" })
  await ciWatchSettled(String(first.watch_id))
})

test("a different selector starts its own watch and each session wakes with its own report", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
    { stdout: JSON.stringify({ status: "timeout", iterations: 9 }) },
  ])
  configureCiWatch({ spawner })
  const pr = await startWatch(fixture)
  const run = await startWatch(fixture, { repo: "owner/name", selector: { kind: "run", value: "77" } })
  expect(pr.watch_id).not.toBe(run.watch_id)
  await ciWatchSettled(String(pr.watch_id))
  await ciWatchSettled(String(run.watch_id))
  const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(2)
  expect((prompts[0].body as { parts: Array<{ text: string }> }).parts[0].text).toContain('"status": "success"')
  expect((prompts[1].body as { parts: Array<{ text: string }> }).parts[0].text).toContain('"status": "timeout"')
})

test("the plugin registers the watcher tool and drains a queued report on the next message", async () => {
  const fixture = hostFixture({ messages: [] })
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  configureCoreBinary("/synthetic/concord")
  const { calls, spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  expect(plugin.tool.concord_ci_watch).toBeDefined()
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  expect(started.status).toBe("started")
  await ciWatchSettled(String(started.watch_id))
  const output = { parts: [] as unknown[] }
  await plugin["chat.message"]({ sessionID: SESSION, messageID: "msg_plugin_drain" }, output)
  expect(output.parts).toHaveLength(1)
  const part = output.parts[0] as { id: string; sessionID: string; messageID: string; synthetic: boolean }
  expect(part.synthetic).toBe(true)
  expect(part.id.startsWith("prt_ciwatch-")).toBe(true)
  expect(part.sessionID).toBe(SESSION)
  expect(part.messageID).toBe("msg_plugin_drain")
  expect(calls).toHaveLength(1)
})

test("a JSON-Schema envelope of the arguments still starts the watch", async () => {
  const fixture = hostFixture({ messages: [] })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  configureCoreBinary("/synthetic/concord")
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const enveloped = {
    type: "object",
    additionalProperties: false,
    required: ["repo", "selector"],
    properties: { repo: "owner/name", selector: { kind: "pr", value: "12" } },
  }
  const started = result(await concord_ci_watch.execute(enveloped, context))
  expect(started.status).toBe("started")
  await ciWatchSettled(String(started.watch_id))
})

test("a serialized selector string and a flattened kind/value pair both start the watch", async () => {
  const fixture = hostFixture({ messages: [] })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  configureCoreBinary("/synthetic/concord")
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success" }) },
    { stdout: JSON.stringify({ status: "success" }) },
  ])
  configureCiWatch({ spawner })
  const sha = "d6dcfb372fbac68788410ce2a03e20ed88c2fa84"
  const serialized = result(
    await concord_ci_watch.execute({ repo: "owner/name", selector: JSON.stringify({ kind: "sha", value: sha }) }, context),
  )
  expect(serialized.status).toBe("started")
  await ciWatchSettled(String(serialized.watch_id))
  const flattened = result(await concord_ci_watch.execute({ repo: "owner/name", kind: "sha", value: sha }, context))
  expect(flattened.status).toBe("started")
  await ciWatchSettled(String(flattened.watch_id))
})

test("an entry absent from the session status map is idle, so delivery proceeds", async () => {
  const fixture = hostFixture({ absentStatus: true, messages: [] })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2, idleTimeoutMs: 60 })
  configureCoreBinary("/synthetic/concord")
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = result(await concord_ci_watch.execute(watchArgs, context))
  expect(started.status).toBe("started")
  await ciWatchSettled(String(started.watch_id))
  const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(1)
})

test("the assistant reply parented to the injected report confirms delivery", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  const delivered = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "info")
  expect(delivered).toHaveLength(1)
  expect(String((delivered[0].body as { message: string }).message)).toContain("delivered")
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_drain_parented", output)
  expect(output.parts).toEqual([])
})

test("a concurrent assistant reply not parented to the injected report does not confirm the wake", async () => {
  const fixture = hostFixture({ reply: "unrelated" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 30 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  const delivered = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "info")
  expect(delivered).toHaveLength(0)
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "warn")
  expect(logged).toHaveLength(1)
  expect(String((logged[0].body as { message: string }).message)).toContain("queued for the next message")
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_drain_unrelated", output)
  expect(output.parts).toHaveLength(1)
})

test("the drain resolves the message id from the chat.message output when the host omits input.messageID", async () => {
  const fixture = hostFixture()
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const { calls, spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  expect(plugin.tool.concord_ci_watch).toBeDefined()
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  expect(started.status).toBe("started")
  await ciWatchSettled(String(started.watch_id))
  const output = { message: { id: "msg_host_generated" }, parts: [] as unknown[] }
  await plugin["chat.message"]({ sessionID: SESSION }, output)
  expect(output.parts).toHaveLength(1)
  const part = output.parts[0] as { messageID: string; synthetic: boolean }
  expect(part.messageID).toBe("msg_host_generated")
  expect(part.synthetic).toBe(true)
  expect(calls).toHaveLength(1)
})

test("a queued report survives a chat.message with no resolvable message id and the failure is logged", async () => {
  const fixture = hostFixture()
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  await ciWatchSettled(String(started.watch_id))
  const output = { parts: [] as unknown[] }
  await plugin["chat.message"]({ sessionID: SESSION }, output)
  expect(output.parts).toHaveLength(0)
  const errors = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "error")
  expect(errors.length).toBeGreaterThanOrEqual(1)
  expect(String((errors[0].body as { message: string }).message)).toContain("stay queued")
  const later = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_later", later)
  expect(later.parts).toHaveLength(1)
})
