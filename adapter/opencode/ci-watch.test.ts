import { afterEach, expect, spyOn, test } from "bun:test"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { bindCiWatchClient, ciWatchSettled, concord_ci_watch, configureCiWatch, drainQueuedCiReports, drainQueuedCiReportsForMessage, type VerbSpawner } from "./ci-watch"
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
// "none" (default) never replies, "parented" adds a real assistant reply (a
// non-empty text part) whose parentID is the injected message id,
// "parented_tool" confirms by tool activity instead of text, "placeholder"
// adds the bare record the host persists before the model streams anything,
// "errored" adds a record the host finalized with an error, and "unrelated"
// adds a real reply parented to a concurrent message instead.
function hostFixture(
  options: {
    status?: string
    absentStatus?: boolean
    session?: Record<string, unknown>
    messages?: FixtureMessage[]
    promptStatus?: number
    reply?: "parented" | "parented_tool" | "placeholder" | "errored" | "unrelated" | "none"
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
              parts: [{ type: "text", text: "an unrelated concurrent reply" }],
            })
          } else if (options.reply === "parented") {
            injected.push({
              info: { id: `msg_reply_${promptCount}`, role: "assistant", parentID: id },
              parts: [{ type: "text", text: "CI is green; continuing." }],
            })
          } else if (options.reply === "parented_tool") {
            injected.push({
              info: { id: `msg_reply_${promptCount}`, role: "assistant", parentID: id },
              parts: [{ type: "tool", tool: "bash", state: { status: "completed" } }],
            })
          } else if (options.reply === "placeholder") {
            injected.push({ info: { id: `msg_reply_${promptCount}`, role: "assistant", parentID: id }, parts: [] })
          } else if (options.reply === "errored") {
            injected.push({
              info: { id: `msg_reply_${promptCount}`, role: "assistant", parentID: id, error: { name: "AbortError", data: { message: "interrupted" } } },
              parts: [],
            })
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

test("a caller whose context was already cancelled is refused and spawns nothing", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  const { calls, spawner } = verbFixture([])
  configureCiWatch({ spawner })
  const controller = new AbortController()
  controller.abort()
  const refused = result(await concord_ci_watch.execute(watchArgs, { sessionID: SESSION, abort: controller.signal }))
  expect(refused.status).toBe("refused")
  expect(String(refused.reason)).toContain("coordinator surface")
  expect(String(refused.reason)).toContain("cancelled")
  expect(calls).toHaveLength(0)
})

test("a cancellation landing during the managed-scope lookup refuses admission and spawns nothing", async () => {
  const fixture = hostFixture()
  const controller = new AbortController()
  const client = fixture.client
  const scopeGet = client.get
  client.get = async (request: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown> }) => {
    if (request.url === "/session/{id}") await new Promise((resolve) => setTimeout(resolve, 50))
    return scopeGet(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  const { calls, spawner } = verbFixture([])
  configureCiWatch({ spawner })
  const executing = concord_ci_watch.execute(watchArgs, { sessionID: SESSION, abort: controller.signal })
  controller.abort()
  const refused = result(await executing)
  expect(refused.status).toBe("refused")
  expect(String(refused.reason)).toContain("cancelled")
  expect(calls).toHaveLength(0)
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

test("a failed /log write surfaces on stderr, so a delivery failure keeps a diagnostic", async () => {
  const fixture = hostFixture({ promptStatus: 400 })
  const client = fixture.client
  const post = client.post
  client.post = async (request: { url: string; body?: unknown }) => {
    if (request.url === "/log") return { response: new Response(null, { status: 500 }) }
    return post(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const stderr: string[] = []
  const originalError = console.error
  console.error = (...values: unknown[]) => stderr.push(values.map(String).join(" "))
  try {
    const started = await startWatch(fixture)
    await ciWatchSettled(String(started.watch_id))
  } finally {
    console.error = originalError
  }
  // The delivery failed, the /log write for that failure also failed, and the
  // stderr line is the one diagnostic that survives both.
  expect(stderr.some((line) => line.includes("/log route answered 500"))).toBe(true)
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

test("the host publishes concord_ci_watch args as per-field argument schemas", () => {
  // OpenCode treats each key of a tool's args record as one argument field
  // and publishes it as a property of the tool's object schema. A root JSON
  // Schema passed as args publishes its own keywords as argument fields
  // instead, so repo and selector never reach the model.
  const args = concord_ci_watch.args as Record<string, unknown>
  expect(Object.keys(args).sort()).toEqual(["mode", "repo", "selector", "time_seconds_max"])
  expect(args.repo).toMatchObject({ type: "string", minLength: 3 })
  const selector = args.selector as Record<string, unknown>
  expect(selector).toMatchObject({ type: "object", additionalProperties: false, required: ["kind", "value"] })
  expect((selector.properties as Record<string, unknown>).kind).toMatchObject({ enum: ["pr", "sha", "run"] })
  expect(args.mode).toMatchObject({ enum: ["checks", "merge"] })
  expect(args.time_seconds_max).toMatchObject({ type: "integer", minimum: 0, maximum: 1800 })
})

test("a JSON-Schema envelope of the arguments is refused, never unwrapped", async () => {
  const fixture = hostFixture({ messages: [] })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  configureCoreBinary("/synthetic/concord")
  const { calls, spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const enveloped = {
    type: "object",
    additionalProperties: false,
    required: ["repo", "selector"],
    properties: { repo: "owner/name", selector: { kind: "pr", value: "12" } },
  }
  const refused = result(await concord_ci_watch.execute(enveloped, context))
  expect(refused.status).toBe("refused")
  expect(calls).toHaveLength(0)
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

test("assistant tool activity parented to the injected report confirms delivery", async () => {
  const fixture = hostFixture({ reply: "parented_tool" })
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
  drainQueuedCiReports(SESSION, "msg_drain_parented_tool", output)
  expect(output.parts).toEqual([])
})

test("a pre-model placeholder parented to the injected report does not confirm the wake", async () => {
  // The host persists the assistant record before the model streams anything.
  // A record with no output proves no turn ran, so the report must stay
  // queued for the next chat.message instead of being logged as delivered.
  const fixture = hostFixture({ reply: "placeholder" })
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
  drainQueuedCiReports(SESSION, "msg_drain_placeholder", output)
  expect(output.parts).toHaveLength(1)
})

test("an errored assistant record parented to the injected report does not confirm the wake", async () => {
  // The host finalizes an interrupted placeholder with an error. Such a
  // record is not a reply: the report must stay queued, never logged as
  // delivered.
  const fixture = hostFixture({ reply: "errored" })
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
  drainQueuedCiReports(SESSION, "msg_drain_errored", output)
  expect(output.parts).toHaveLength(1)
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

test("a watch beyond the session's delivery capacity is refused and every accepted report is retained", async () => {
  const fixture = hostFixture({ status: "busy" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idleTimeoutMs: 30, idlePollMs: 5 })
  const spawner: VerbSpawner = async () => ({
    exitCode: 0,
    stdout: JSON.stringify({ status: "success", iterations: 1 }),
    stderr: "",
  })
  configureCiWatch({ spawner })
  const accepted: Array<Record<string, unknown>> = []
  for (const value of ["1", "2", "3", "4"]) {
    accepted.push(await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value } }))
  }
  const fifth = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "5" } })
  expect(fifth.status).toBe("refused")
  expect(String(fifth.reason)).toContain("delivery queue caps at 4")
  await Promise.all(accepted.map((watch) => ciWatchSettled(String(watch.watch_id))))
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_capacity_drain", output)
  expect(output.parts).toHaveLength(4)
  for (const value of ["1", "2", "3", "4"]) {
    const carried = output.parts.some((part) => String((part as { text: string }).text).includes(`pr:${value}.`))
    expect(carried).toBe(true)
  }
  // The drain released the capacity, so the refused selector can start now.
  const fifthAgain = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "5" } })
  expect(fifthAgain.status).toBe("started")
  await ciWatchSettled(String(fifthAgain.watch_id))
})

test("a delivered wake releases the session's admission capacity for the next watch", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxQueuedPerSession: 1, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  const first = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "1" } })
  const blocked = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "2" } })
  expect(blocked.status).toBe("refused")
  expect(String(blocked.reason)).toContain("delivery queue caps at 1")
  await ciWatchSettled(String(first.watch_id))
  const second = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "2" } })
  expect(second.status).toBe("started")
  await ciWatchSettled(String(second.watch_id))
})

test("a queued report that cannot drain yet still holds the session's admission capacity", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxQueuedPerSession: 1, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  const first = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "1" } })
  await ciWatchSettled(String(first.watch_id))
  // The report queued unconfirmed; a message with no resolvable id drains nothing.
  const blocked = { parts: [] as unknown[] }
  drainQueuedCiReportsForMessage(SESSION, undefined, blocked)
  expect(blocked.parts).toHaveLength(0)
  const second = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "2" } })
  expect(second.status).toBe("refused")
  expect(String(second.reason)).toContain("release capacity")
  const later = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_later_capacity", later)
  expect(later.parts).toHaveLength(1)
  const third = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "2" } })
  expect(third.status).toBe("started")
  await ciWatchSettled(String(third.watch_id))
})

test("a new watch posts one noReply notice naming repo, selector, mode, and budget, and a reused watch posts none", async () => {
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const held = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  const spawner: VerbSpawner = async () => held.promise
  configureCiWatch({ spawner })
  const started = await startWatch(fixture, {
    repo: "owner/name",
    selector: { kind: "pr", value: "12" },
    mode: "merge",
    time_seconds_max: 600,
  })
  expect(started.status).toBe("started")
  await until(() => fixture.posts.some((post) => post.url === "/session/{id}/message"), "the start notice")
  const notices = fixture.posts.filter((post) => post.url === "/session/{id}/message")
  expect(notices).toHaveLength(1)
  const body = notices[0].body as {
    noReply?: boolean
    parts: Array<{ type: string; text: string; ignored?: boolean; synthetic?: boolean; metadata?: Record<string, unknown> }>
  }
  expect(body.noReply).toBe(true)
  expect(body.parts).toHaveLength(1)
  expect(body.parts[0].type).toBe("text")
  expect(body.parts[0].ignored).toBe(true)
  expect(body.parts[0].synthetic).toBeUndefined()
  expect(body.parts[0].metadata).toEqual({ "concord.ci_watch_notice": started.watch_id })
  expect(body.parts[0].text).toContain("owner/name")
  expect(body.parts[0].text).toContain("pr:12")
  expect(body.parts[0].text).toContain("merge mode")
  expect(body.parts[0].text).toContain("600")
  // The notice spends no model turn: it never touches the prompt route.
  expect(fixture.posts.some((post) => post.url === "/session/{id}/prompt_async")).toBe(false)
  // A reused watch posts no second notice.
  const reused = await startWatch(fixture, {
    repo: "owner/name",
    selector: { kind: "pr", value: "12" },
    mode: "merge",
    time_seconds_max: 600,
  })
  expect(reused.status).toBe("already_watching")
  expect(reused.watch_id).toBe(started.watch_id)
  await new Promise((resolve) => setTimeout(resolve, 10))
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/message")).toHaveLength(1)
  held.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "cancelled" }), stderr: "" })
  await ciWatchSettled(String(started.watch_id))
})

test("the start notice always precedes the terminal report", async () => {
  const fixture = hostFixture({ reply: "parented" })
  const client = fixture.client
  const post = client.post
  client.post = async (request: { url: string; body?: unknown }) => {
    // Delay the notice so a settle that did not await it would deliver first.
    if (request.url === "/session/{id}/message") await new Promise((resolve) => setTimeout(resolve, 30))
    return post(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  const noticeAt = fixture.posts.findIndex((entry) => entry.url === "/session/{id}/message")
  const reportAt = fixture.posts.findIndex((entry) => entry.url === "/session/{id}/prompt_async")
  expect(noticeAt).toBeGreaterThanOrEqual(0)
  expect(reportAt).toBeGreaterThan(noticeAt)
})

test("a failed notice post is logged at warn and never blocks or fails the report", async () => {
  const fixture = hostFixture({ reply: "parented" })
  const client = fixture.client
  const post = client.post
  client.post = async (request: { url: string; body?: unknown }) => {
    if (request.url === "/session/{id}/message") return { response: new Response(null, { status: 500 }) }
    return post(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  await ciWatchSettled(String(started.watch_id))
  const warns = fixture.posts.filter((entry) => entry.url === "/log" && (entry.body as { level: string }).level === "warn")
  expect(warns.some((entry) => String((entry.body as { message: string }).message).includes("start notice"))).toBe(true)
  const prompts = fixture.posts.filter((entry) => entry.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(1)
  expect((prompts[0].body as { parts: Array<{ text: string }> }).parts[0].text).toContain('"status": "success"')
})

function heartbeatFixture() {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  const held = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  configureCiWatch({
    stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40,
    heartbeatIntervalMs: 20, spawner: async () => held.promise,
  })
  const notices = () => fixture.posts.filter((post) => post.url === "/session/{id}/message")
  const finish = (status = "success") => held.resolve({ exitCode: 0, stdout: JSON.stringify({ status }), stderr: "" })
  return { ...fixture, notices, finish }
}

test("the default heartbeat interval is sixty seconds and its timer does not keep the host alive", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  const held = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  configureCiWatch({ stateDir: STATE_DIR, spawner: async () => held.promise })
  const timer = spyOn(globalThis, "setInterval")
  let started: Record<string, unknown> | undefined
  try {
    started = await startWatch(fixture)
    expect(timer).toHaveBeenCalledTimes(1)
    expect(timer.mock.calls[0][1]).toBe(60_000)
    expect(timer.mock.results[0].value.hasRef()).toBe(false)
  } finally {
    timer.mockRestore()
    held.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "success" }), stderr: "" })
    if (started !== undefined) await ciWatchSettled(String(started.watch_id))
  }
})

test("a held watch posts recurring ignored noReply heartbeats and stops them before delivery", async () => {
  const fixture = heartbeatFixture()
  const started = await startWatch(fixture)
  try {
    await until(() => fixture.notices().length >= 3, "two heartbeats", 300)
    for (const notice of fixture.notices().slice(1)) {
      const body = notice.body as { noReply: boolean; parts: Array<Record<string, unknown>> }
      expect(body.noReply).toBe(true)
      expect(body.parts).toHaveLength(1)
      expect(body.parts[0].ignored).toBe(true)
      expect(body.parts[0].synthetic).toBeUndefined()
      expect(body.parts[0].metadata).toEqual({ "concord.ci_watch_notice": started.watch_id })
      expect(String(body.parts[0].text)).toContain("Still watching CI for owner/name pr:12")
      expect(String(body.parts[0].text)).toContain("elapsed")
    }
    expect(fixture.posts.some((post) => post.url === "/session/{id}/prompt_async")).toBe(false)
  } finally {
    fixture.finish()
    await ciWatchSettled(String(started.watch_id))
  }
  const count = fixture.notices().length
  await new Promise((resolve) => setTimeout(resolve, 60))
  expect(fixture.notices()).toHaveLength(count)
  const reportAt = fixture.posts.findIndex((post) => post.url === "/session/{id}/prompt_async")
  expect(fixture.posts.slice(reportAt + 1).some((post) => post.url === "/session/{id}/message")).toBe(false)
})

test("heartbeats skip busy sessions instead of queuing a late notice", async () => {
  const fixture = heartbeatFixture()
  const started = await startWatch(fixture)
  await until(() => fixture.notices().length === 1, "the start notice")
  const get = fixture.client.get
  let busy = true
  fixture.client.get = async (request) => {
    if (request.url === "/session/status" && busy) {
      return { data: { [SESSION]: { type: "busy" } }, response: new Response(null, { status: 200 }) }
    }
    return get(request)
  }
  try {
    await new Promise((resolve) => setTimeout(resolve, 70))
    expect(fixture.notices()).toHaveLength(1)
    busy = false
    await until(() => fixture.notices().length >= 2, "the next idle heartbeat", 300)
  } finally {
    busy = false
    fixture.finish()
    await ciWatchSettled(String(started.watch_id))
  }
})

test("settle drains one in-flight heartbeat without overlapping posts or trailing notices", async () => {
  const fixture = heartbeatFixture()
  const post = fixture.client.post
  const blocked = deferred<void>()
  let heartbeats = 0
  fixture.client.post = async (request) => {
    const text = (request.body as { parts?: Array<{ text?: string }> })?.parts?.[0]?.text ?? ""
    if (request.url === "/session/{id}/message" && text.includes("Still watching")) {
      heartbeats++
      await blocked.promise
    }
    return post(request)
  }
  const started = await startWatch(fixture)
  try {
    await until(() => heartbeats === 1, "the held heartbeat", 300)
    await new Promise((resolve) => setTimeout(resolve, 60))
    expect(heartbeats).toBe(1)
    fixture.finish()
    await new Promise((resolve) => setTimeout(resolve, 30))
    expect(fixture.posts.some((post) => post.url === "/session/{id}/prompt_async")).toBe(false)
  } finally {
    blocked.resolve()
    fixture.finish()
    await ciWatchSettled(String(started.watch_id))
  }
  expect(heartbeats).toBe(1)
  const noticeAt = fixture.posts.findLastIndex((post) => post.url === "/session/{id}/message")
  const reportAt = fixture.posts.findIndex((post) => post.url === "/session/{id}/prompt_async")
  expect(reportAt).toBeGreaterThan(noticeAt)
})

test("heartbeat failures are logged and do not prevent a later heartbeat or terminal report", async () => {
  const fixture = heartbeatFixture()
  const post = fixture.client.post
  let failed = false
  fixture.client.post = async (request) => {
    const text = (request.body as { parts?: Array<{ text?: string }> })?.parts?.[0]?.text ?? ""
    if (!failed && request.url === "/session/{id}/message" && text.includes("Still watching")) {
      failed = true
      return { response: new Response(null, { status: 500 }) }
    }
    return post(request)
  }
  const started = await startWatch(fixture)
  try {
    await until(() => fixture.notices().length >= 2, "a heartbeat after the failed post", 300)
    expect(failed).toBe(true)
    expect(fixture.posts.some((post) => post.url === "/log" &&
      (post.body as { level: string; message: string }).level === "warn" &&
      (post.body as { message: string }).message.includes("heartbeat"))).toBe(true)
  } finally {
    fixture.finish()
    await ciWatchSettled(String(started.watch_id))
  }
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
})

for (const status of ["cancelled", "timeout", "error"]) {
  test(`heartbeats stop before a ${status} terminal report`, async () => {
    const fixture = heartbeatFixture()
    const started = await startWatch(fixture)
    try {
      await until(() => fixture.notices().length >= 2, "a heartbeat", 300)
    } finally {
      fixture.finish(status)
      await ciWatchSettled(String(started.watch_id))
    }
    const count = fixture.notices().length
    await new Promise((resolve) => setTimeout(resolve, 60))
    expect(fixture.notices()).toHaveLength(count)
    const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
    expect(prompts).toHaveLength(1)
    expect((prompts[0].body as { parts: Array<{ text: string }> }).parts[0].text).toContain(`"status": "${status}"`)
  })
}

test("a heartbeat waiting on a host read is discarded when the watch settles", async () => {
  const fixture = heartbeatFixture()
  const started = await startWatch(fixture)
  await until(() => fixture.notices().length === 1, "the start notice")
  const get = fixture.client.get
  const blocked = deferred<void>()
  let reading = false
  fixture.client.get = async (request) => {
    if (!reading && request.url === "/session/{id}") {
      reading = true
      await blocked.promise
    }
    return get(request)
  }
  try {
    await until(() => reading, "the heartbeat identity read", 300)
    fixture.finish()
    await new Promise((resolve) => setTimeout(resolve, 30))
  } finally {
    blocked.resolve()
    fixture.finish()
    await ciWatchSettled(String(started.watch_id))
  }
  expect(fixture.notices()).toHaveLength(1)
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
})
