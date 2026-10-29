import { afterAll, afterEach, expect, spyOn, test } from "bun:test"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { bindCiWatchClient, ciWatchRedelivery, ciWatchSettled, ciWatchSnapshot, ciWatchTool, configureCiWatch, drainQueuedCiReports, drainQueuedCiReportsForMessage, validateCiWatchArgs, type CiWatchArgs, type VerbSpawner } from "./ci-watch"
import { configureCoreBinary } from "./core-runtime"
import ConcordAdapterPlugin from "./concord-plugin"
import { hostToolSchemas } from "./generated-contracts"
import { configureHostLease } from "./host-lease"
import { hostControlPlane } from "./move-session"

const SESSION = "watch-session"
const STATE_DIR = fs.mkdtempSync(path.join(os.tmpdir(), "ci-watch-test-"))
afterAll(() => {
  configureCiWatch({ reset: true })
  fs.rmSync(STATE_DIR, { recursive: true, force: true })
})

// The suite exercises the watcher through the factory the plugin registers
// from, so both surfaces see the same tool shape.
const concord_ci_watch = ciWatchTool()

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

const context = { sessionID: SESSION, agent: "Concord", abort: new AbortController().signal }

const watchArgs = { repo: "owner/name", selector: { kind: "pr", value: "12" } }

async function startWatch(_fixture: ReturnType<typeof hostFixture>, args: Record<string, unknown> = watchArgs) {
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

// The CI watch contract lives in the host tool catalog, so the runtime
// validator and the published contract must share one set of bounds. The
// validator reads its limits and keys from that owning schema; this test
// pins the agreement so neither side can drift.
test("watcher contract preserves shared runtime bounds", () => {
  const contract = hostToolSchemas.concord_ci_watch as Record<string, any>
  expect(contract).toBeObject()
  expect(contract.type).toBe("object")
  expect(contract.additionalProperties).toBe(false)
  expect(contract.required).toEqual(["repo", "selector"])
  expect(Object.keys(contract.properties).sort()).toEqual(["head_sha", "mode", "repo", "selector", "time_seconds_max"])
  expect(contract.properties.repo).toMatchObject({
    type: "string",
    minLength: 3,
    maxLength: 256,
    pattern: "^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9][A-Za-z0-9.-]*$",
  })
  expect(contract.properties.selector).toMatchObject({ type: "object", additionalProperties: false, required: ["kind", "value"] })
  expect(contract.properties.selector.properties.kind).toMatchObject({ type: "string", enum: ["pr", "sha", "run"] })
  expect(contract.properties.selector.properties.value).toMatchObject({ type: "string", minLength: 1, maxLength: 64 })
  expect(contract.properties.mode).toMatchObject({ type: "string", enum: ["checks", "merge"] })
  expect(contract.properties.time_seconds_max).toMatchObject({ type: "integer", minimum: 0, maximum: 7200 })
  expect(contract.properties.time_seconds_max.default).toBeUndefined()
  // The execution validator enforces the published enums at runtime, not
  // only in the schema document.
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { kind: "branch", value: "main" } })).toBeString()
  expect(validateCiWatchArgs({ ...watchArgs, mode: "poll" })).toBeString()
  // The execution validator enforces the published integer time-budget
  // boundaries: exactly 0..1800 after the numeric normalization.
  expect(validateCiWatchArgs({ ...watchArgs, time_seconds_max: 0 })).toBeObject()
  expect(validateCiWatchArgs({ ...watchArgs, time_seconds_max: 1800 })).toBeObject()
  expect(validateCiWatchArgs({ ...watchArgs, time_seconds_max: "900" })).toBeObject()
  for (const budget of [-1, 1801, 1.5, "abc", "18.5"]) {
    expect(validateCiWatchArgs({ ...watchArgs, time_seconds_max: budget }), String(budget)).toBeString()
  }
  for (const budget of [0, 1801, 7200, "7200"]) {
    expect(validateCiWatchArgs({ ...watchArgs, mode: "merge", time_seconds_max: budget })).toBeObject()
  }
  for (const budget of [-1, 7201, 1.5]) {
    expect(validateCiWatchArgs({ ...watchArgs, mode: "merge", time_seconds_max: budget })).toBeString()
  }
  // Only repo and selector are required, on the schema and at execution.
  expect(validateCiWatchArgs({ repo: "owner/name" })).toBeString()
  expect(validateCiWatchArgs({ selector: { kind: "pr", value: "12" } })).toBeString()
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { kind: "pr" } })).toBeString()
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { value: "12" } })).toBeString()
})

test("the published repo bound of 256 characters is enforced at execution", () => {
  const repo256 = `${"a".repeat(127)}/${"b".repeat(128)}`
  const repo257 = `${"a".repeat(128)}/${"b".repeat(128)}`
  expect(repo256.length).toBe(256)
  expect(repo257.length).toBe(257)
  expect(validateCiWatchArgs({ repo: repo256, selector: { kind: "pr", value: "12" } })).toBeObject()
  expect(validateCiWatchArgs({ repo: repo257, selector: { kind: "pr", value: "12" } })).toBeString()
})

test("the published selector value bound of 64 characters is enforced at execution", () => {
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { kind: "run", value: "1".repeat(64) } })).toBeObject()
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { kind: "run", value: "1".repeat(65) } })).toBeString()
})

test("arguments carrying unexpected top-level keys are refused", () => {
  const refused = validateCiWatchArgs({ ...watchArgs, unexpected: true })
  expect(refused).toBeString()
  expect(String(refused)).toContain("unexpected")
})

test("selectors carrying unexpected keys are refused", () => {
  const refused = validateCiWatchArgs({ repo: "owner/name", selector: { kind: "pr", value: "12", extra: "x" } })
  expect(refused).toBeString()
  expect(String(refused)).toContain("selector")
})

test("supported argument normalizations hold under the published contract", async () => {
  // The Code Mode request wrapper unwraps before validation.
  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCiWatch({ stateDir: STATE_DIR, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2 })
  configureCoreBinary("/synthetic/concord")
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
  configureCiWatch({ spawner })
  const wrapped = result(await concord_ci_watch.execute({ request: { ...watchArgs } }, context))
  expect(wrapped.status).toBe("started")
  await ciWatchSettled(String(wrapped.watch_id))
  // The numeric budget normalization accepts a digit string.
  const normalized = validateCiWatchArgs({ ...watchArgs, time_seconds_max: "600" })
  expect(normalized).toBeObject()
  expect((normalized as CiWatchArgs).time_seconds_max).toBe(600)
  // The closed-key check runs after the normalizations, so a key that
  // survives them is still refused.
  const unknownAfterNormalize = validateCiWatchArgs({ ...watchArgs, time_seconds_max: "600", unexpected: true })
  expect(unknownAfterNormalize).toBeString()
  expect(String(unknownAfterNormalize)).toContain("unexpected")
  // The per-kind semantic checks are runtime-only: the contract bounds a
  // selector value's length, never its shape.
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { kind: "sha", value: "z".repeat(40) } })).toBeString()
  expect(validateCiWatchArgs({ repo: "owner/name", selector: { kind: "pr", value: "1x" } })).toBeString()
})

test("the watch returns at once, carries one state file across slices, and prompts only after the terminal slice", async () => {
  const fixture = hostFixture({ reply: "parented" })
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
    fs.writeFileSync(JSON.parse(stdin).state_file, "slice state")
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
  expect(fs.existsSync(String(started.state_file))).toBe(true)
  expect(JSON.parse(calls[1])).toEqual({ repo: "owner/name", selector: { kind: "pr", value: "12" }, state_file: started.state_file })
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toEqual([])

  second.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "success", iterations: 2 }), stderr: "" })
  await ciWatchSettled(String(started.watch_id))
  expect(fs.existsSync(String(started.state_file))).toBe(false)
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

// The pinned host prompt payload carries the variant as a top-level field
// beside its { providerID, modelID } model reference, and the host persists
// the prompt's variant as the session's next choice, treating "default" as
// none. A wake or notice that omits a selected variant resets the session.
const GLM = { providerID: "zai", modelID: "glm-test" }
for (const { label, model, postedModel, posted } of [
  { label: "a selected variant", model: { id: "glm-test", providerID: "zai", variant: "high" }, postedModel: GLM, posted: "high" },
  { label: "no variant", model: { id: "glm-test", providerID: "zai" }, postedModel: GLM, posted: undefined },
  { label: "the default variant", model: { id: "glm-test", providerID: "zai", variant: "default" }, postedModel: GLM, posted: undefined },
  { label: "a variant without a usable model", model: { providerID: "zai", variant: "high" }, postedModel: undefined, posted: undefined },
]) {
  test(`the wake prompt and the start notice carry the session's model variant for ${label}`, async () => {
    const fixture = hostFixture({ reply: "parented", session: { id: SESSION, agent: "concord-1", model } })
    hostControlPlane().bind(fixture.client)
    bindCiWatchClient(fixture.client)
    configureCoreBinary("/synthetic/concord")
    configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
    const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success" }) }])
    configureCiWatch({ spawner })
    const started = await startWatch(fixture)
    await ciWatchSettled(String(started.watch_id))
    const bodies = ["/session/{id}/prompt_async", "/session/{id}/message"].map((url) => {
      const posts = fixture.posts.filter((post) => post.url === url)
      expect(posts).toHaveLength(1)
      return posts[0].body as Record<string, unknown>
    })
    for (const body of bodies) {
      expect(body.agent).toBe("concord-1")
      expect(body.model).toEqual(postedModel)
      if (posted === undefined) expect(Object.hasOwn(body, "variant")).toBe(false)
      else expect(body.variant).toBe(posted)
    }
  })
}

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
  // The idle session gave the report its one try at once, and no reply followed that either.
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(2)
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "warn")
  expect(logged).toHaveLength(2)
  expect(String((logged[0].body as { message: string }).message)).toContain("the report is queued for the next message")
  expect(String((logged[1].body as { message: string }).message)).toContain("any not yet drained stay queued for the next message")

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

for (const reply of ["parented", "none"] as const) {
  test(`a CLI fail-fast failure with a pending peer is terminal and ${reply === "parented" ? "delivered to its caller" : "retained for its caller after unanswered wakes"}`, async () => {
    const fixture = hostFixture({
      reply,
      session: { id: SESSION, agent: "concord-1", model: { id: "glm-test", providerID: "zai", variant: "high" } },
    })
    hostControlPlane().bind(fixture.client)
    bindCiWatchClient(fixture.client)
    configureCoreBinary("/synthetic/concord")
    configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
    const sha = "6459f13778fcad4b42f771ac4988f55897f635f5"
    const jobURL = "https://github.com/owner/name/actions/runs/77/job/88"
    const report = {
      status: "failure",
      reason: "failure observed; population may be incomplete, pending peers do not delay this report",
      sha,
      head_sha: sha,
      run_url: "https://github.com/owner/name/actions/runs/77",
      checks: {
        total: 2, passing: 0, failing: 1, skipped: 0, pending: 1,
        entries: [
          { name: "adapter tests", url: jobURL, bucket: "fail" },
          { name: "store tests", url: "https://github.com/owner/name/actions/runs/77/job/89", bucket: "pending" },
        ],
      },
      failures: [{ name: "adapter tests", url: jobURL, classification: "unknown" }],
      iterations: 1,
      elapsed_seconds: 2,
      deadline_seconds: 240,
    }
    // A failure stays terminal even when its report includes a pending peer.
    const { calls, spawner } = verbFixture([{ stdout: JSON.stringify(report) }])
    configureCiWatch({ spawner })
    const started = await startWatch(fixture, { ...watchArgs, head_sha: sha })
    expect(started.status).toBe("started")
    await ciWatchSettled(String(started.watch_id))
    await ciWatchRedelivery(SESSION)

    expect(calls).toHaveLength(1)
    expect(JSON.parse(calls[0])).toEqual({ ...watchArgs, head_sha: sha, state_file: started.state_file })
    expect(fixture.posts.some((post) => post.url.endsWith("/abort"))).toBe(false)
    const expectedText = `[concord ci-watch ${String(started.watch_id)}] CI wait failure for owner/name pr:12.\n\n\`\`\`json\n${JSON.stringify(report, null, 2)}\n\`\`\``
    const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
    expect(prompts).toHaveLength(reply === "parented" ? 1 : 2)
    for (const prompt of prompts) {
      const body = prompt.body as { agent: string; model: unknown; variant: string; parts: Array<{ type: string; synthetic: boolean; text: string }> }
      expect(body.agent).toBe("concord-1")
      expect(body.model).toEqual({ providerID: "zai", modelID: "glm-test" })
      expect(body.variant).toBe("high")
      expect(body.parts).toHaveLength(1)
      expect(body.parts[0]).toMatchObject({ type: "text", synthetic: true })
      expect(body.parts[0].text).toContain(expectedText)
    }
    expect(fixture.gets.filter((get) => get.url === "/session/{id}").every((get) => get.path?.id === SESSION)).toBe(true)
    const receipt = receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!
    expect(receipt.session_id).toBe(SESSION)
    expect(receipt.delivery_state).toBe(reply === "parented" ? "delivered" : "queued")
    expect(receipt.terminal_observation).toMatchObject({
      status: "failure", sha, head_sha: sha, run_url: report.run_url,
      checks: { total: 2, passing: 0, failing: 1, skipped: 0, pending: 1 },
      evidence: "exact_head", gaps: [],
    })
    if (reply === "parented") expect(ciWatchSnapshot(SESSION).active).toEqual([])
    else expect(activeRecord(ciWatchSnapshot(SESSION), String(started.watch_id))!.phase).toBe("queued")

    const other = { parts: [] as unknown[] }
    drainQueuedCiReports("another-session", "msg_other_fail_fast", other)
    expect(other.parts).toEqual([])
    const missingID = { parts: [] as unknown[] }
    drainQueuedCiReportsForMessage(SESSION, undefined, missingID)
    expect(missingID.parts).toEqual([])
    const output = { parts: [] as unknown[] }
    drainQueuedCiReports(SESSION, "msg_fail_fast", output)
    expect(output.parts).toHaveLength(reply === "parented" ? 0 : 1)
    if (reply === "none") {
      expect(output.parts[0]).toMatchObject({
        sessionID: SESSION, messageID: "msg_fail_fast", type: "text", synthetic: true, text: expectedText,
      })
      expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("drained")
    }
    const again = { parts: [] as unknown[] }
    drainQueuedCiReports(SESSION, "msg_fail_fast_again", again)
    expect(again.parts).toEqual([])
    expect(ciWatchSnapshot(SESSION).active).toEqual([])
    expect(calls).toHaveLength(1)
  })
}

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
  const fixture = hostFixture({ reply: "parented" })
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

test("a verb error report is terminal and cannot reset the deadline through a retry", async () => {
  const fixture = hostFixture({ reply: "parented" })
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
  expect(calls).toHaveLength(1)
  const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts).toHaveLength(1)
  expect((prompts[0].body as { parts: Array<{ text: string }> }).parts[0].text).toContain("gh: auth failure")
})

for (const status of ["success", "failure", "cancelled", "superseded", "timeout", "error", "refused", "no-report", "killed", "pending-ceiling"]) {
  test(`the watcher removes state when it ends ${status}`, async () => {
    const fixture = hostFixture({ reply: "parented" })
    hostControlPlane().bind(fixture.client)
    bindCiWatchClient(fixture.client)
    configureCoreBinary("/synthetic/concord")
    let file = ""
    const now = Date.now()
    let clock = now
    const clockSpy = status === "pending-ceiling" ? spyOn(Date, "now").mockImplementation(() => clock) : null
    configureCiWatch({
      stateDir: STATE_DIR, maxSliceFailures: 1, confirmPollMs: 2, confirmWindowMs: 20, idlePollMs: 2,
      spawner: async (_argv, stdin) => {
        file = JSON.parse(stdin).state_file
        fs.writeFileSync(file, JSON.stringify({ schema_version: 1, deadline_at: new Date(now + 60_000), owner_pid: process.pid }))
        if (status === "killed") throw new Error("child killed")
        if (status === "no-report") return { exitCode: 1, stdout: "", stderr: "child died" }
        if (status === "pending-ceiling") {
          clock += 2_000_000
          return { exitCode: 0, stdout: JSON.stringify({ status: "pending", state_file: file }), stderr: "" }
        }
        return { exitCode: status === "error" ? 1 : 0, stdout: JSON.stringify({ status }), stderr: "" }
      },
    })
    try {
      const started = await startWatch(fixture)
      await ciWatchSettled(String(started.watch_id))
      expect(file).not.toBe("")
      expect(fs.existsSync(file)).toBe(false)
      const prompts = fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
      expect(prompts).toHaveLength(1)
    } finally {
      clockSpy?.mockRestore()
    }
  })
}

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

// A watch started beside a push names the pushed commit, so the verb judges
// that commit and not the head it replaces. The commit is part of the watched
// subject: a watch for it never folds into a running watch on the same pull
// request that names no commit or another one.
test("a pushed head_sha is validated, reaches every verb slice, and keys its own watch", async () => {
  const pushed = "6459f13778fcad4b42f771ac4988f55897f635f5"
  const admitted = validateCiWatchArgs({ ...watchArgs, mode: "merge", head_sha: pushed })
  expect(admitted).toBeObject()
  expect((admitted as CiWatchArgs).head_sha).toBe(pushed)
  for (const refused of [
    { repo: "owner/name", selector: { kind: "sha", value: pushed }, head_sha: pushed },
    { repo: "owner/name", selector: { kind: "run", value: "77" }, head_sha: pushed },
    { ...watchArgs, head_sha: "6459f137" },
    { ...watchArgs, head_sha: pushed.toUpperCase() },
  ]) {
    expect(validateCiWatchArgs(refused), JSON.stringify(refused)).toBeString()
  }

  const fixture = hostFixture()
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  const held = deferred<{ exitCode: number; stdout: string; stderr: string }>()
  const calls: string[] = []
  configureCiWatch({
    stateDir: STATE_DIR,
    confirmPollMs: 2,
    confirmWindowMs: 20,
    idlePollMs: 2,
    spawner: async (_argv, stdin) => {
      calls.push(stdin)
      return held.promise
    },
  })
  const replaced = await startWatch(fixture)
  const named = await startWatch(fixture, { ...watchArgs, head_sha: pushed })
  expect(named.status).toBe("started")
  expect(named.watch_id).not.toBe(replaced.watch_id)
  expect(named.head_sha).toBe(pushed)
  await until(() => calls.length === 2, "both watches spawn a slice")
  expect(calls.map((stdin) => JSON.parse(stdin).head_sha)).toEqual([undefined, pushed])
  held.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "cancelled" }), stderr: "" })
  await ciWatchSettled(String(replaced.watch_id))
  await ciWatchSettled(String(named.watch_id))
})

test("a different selector starts its own watch and each session wakes with its own report", async () => {
  const fixture = hostFixture({ reply: "parented" })
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
  expect(Object.keys(args).sort()).toEqual(["head_sha", "mode", "repo", "selector", "time_seconds_max"])
  expect(args.repo).toMatchObject({ type: "string", minLength: 3 })
  const selector = args.selector as Record<string, unknown>
  expect(selector).toMatchObject({ type: "object", additionalProperties: false, required: ["kind", "value"] })
  expect((selector.properties as Record<string, unknown>).kind).toMatchObject({ enum: ["pr", "sha", "run"] })
  expect(args.mode).toMatchObject({ enum: ["checks", "merge"] })
  expect(args.time_seconds_max).toMatchObject({ type: "integer", minimum: 0, maximum: 7200 })
  expect((args.time_seconds_max as Record<string, unknown>).default).toBeUndefined()
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
  const fixture = hostFixture({ absentStatus: true, messages: [], reply: "parented" })
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
  // The idle session gives the report its one try at once, and that try is not confirmed either.
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(2)
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "warn")
  expect(logged).toHaveLength(2)
  expect(String((logged[0].body as { message: string }).message)).toContain("the report is queued for the next message")
  expect(String((logged[1].body as { message: string }).message)).toContain("idle redelivery of 1 queued report(s)")
  expect(String((logged[1].body as { message: string }).message)).toContain("any not yet drained stay queued for the next message")
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
  // The idle session gives the report its one try at once, and that try is not confirmed either.
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(2)
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "warn")
  expect(logged).toHaveLength(2)
  expect(String((logged[0].body as { message: string }).message)).toContain("the report is queued for the next message")
  expect(String((logged[1].body as { message: string }).message)).toContain("idle redelivery of 1 queued report(s)")
  expect(String((logged[1].body as { message: string }).message)).toContain("any not yet drained stay queued for the next message")
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
  // The idle session gives the report its one try at once, and that try is not confirmed either.
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(2)
  const logged = fixture.posts.filter((post) => post.url === "/log" && (post.body as { level: string }).level === "warn")
  expect(logged).toHaveLength(2)
  expect(String((logged[0].body as { message: string }).message)).toContain("the report is queued for the next message")
  expect(String((logged[1].body as { message: string }).message)).toContain("idle redelivery of 1 queued report(s)")
  expect(String((logged[1].body as { message: string }).message)).toContain("any not yet drained stay queued for the next message")
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

test("a report queued while the session stayed busy reaches the model when the session goes idle, with no chat message, and admission frees", async () => {
  const host: { status: string; reply: "parented" } = { status: "busy", reply: "parented" }
  const fixture = hostFixture(host)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxQueuedPerSession: 1, idleTimeoutMs: 30, idlePollMs: 5, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  // The host reports every prompted user message through chat.message.
  const hooked: unknown[][] = []
  const post = fixture.client.post
  fixture.client.post = async (request: { url: string; body?: unknown }) => {
    const answer = await post(request)
    if (request.url === "/session/{id}/prompt_async") {
      const output = { parts: [...(request.body as { parts: unknown[] }).parts] }
      await plugin["chat.message"]({ sessionID: SESSION, messageID: `msg_prompted_${hooked.length}` }, output)
      hooked.push(output.parts)
    }
    return answer
  }
  const first = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  expect(first.status).toBe("started")
  await ciWatchSettled(String(first.watch_id))
  const prompts = () => fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  // The session stayed busy past idleTimeoutMs, so the report queued without a wake.
  expect(prompts()).toHaveLength(0)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(first.watch_id))!.delivery_state).toBe("queued")
  const blocked = result(await plugin.tool.concord_ci_watch.execute!({ repo: "owner/name", selector: { kind: "pr", value: "2" } }, context))
  expect(blocked.status).toBe("refused")

  host.status = "idle"
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: SESSION } } })
  await ciWatchRedelivery(SESSION)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(first.watch_id))!.delivery_state).toBe("delivered")
  expect(prompts()).toHaveLength(1)
  const body = prompts()[0].body as { agent: string; model: unknown; parts: Array<{ text: string; synthetic: boolean }> }
  expect(body.parts).toHaveLength(1)
  expect(body.parts[0].synthetic).toBe(true)
  expect(body.parts[0].text).toContain(`[concord ci-watch ${String(first.watch_id)}] CI wait success for owner/name pr:12.`)
  expect(body.agent).toBe("concord-1")
  expect(body.model).toEqual({ providerID: "zai", modelID: "glm-test" })
  // The redelivery's own message does not drain the report it already carries.
  expect(hooked).toHaveLength(1)
  expect(hooked[0]).toHaveLength(1)
  // The confirmed redelivery emptied the queue, so the next watch is admitted.
  const second = result(await plugin.tool.concord_ci_watch.execute!({ repo: "owner/name", selector: { kind: "pr", value: "2" } }, context))
  expect(second.status).toBe("started")
  await ciWatchSettled(String(second.watch_id))
})

test("a queued report gets one idle redelivery try, so an unanswered try leaves it queued for the next message and a later idle event sends nothing", async () => {
  const host: { status: string; reply: "none" } = { status: "busy", reply: "none" }
  const fixture = hostFixture(host)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idleTimeoutMs: 30, idlePollMs: 5, confirmPollMs: 2, confirmWindowMs: 20 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success", iterations: 1 }) }])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  await ciWatchSettled(String(started.watch_id))
  const prompts = () => fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts()).toHaveLength(0)

  host.status = "idle"
  const idle = { event: { type: "session.idle", properties: { sessionID: SESSION } } }
  await plugin.event(idle)
  // A second idle event while the try is in flight starts no second try.
  await plugin.event(idle)
  await ciWatchRedelivery(SESSION)
  expect(prompts()).toHaveLength(1)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("queued")
  const warned = fixture.posts.filter((post) => post.url === "/log" && String((post.body as { message: string }).message).includes("idle redelivery"))
  expect(warned).toHaveLength(1)
  expect((warned[0].body as { level: string }).level).toBe("warn")

  // The spent try is not repeated: the idle event that ends any later turn sends nothing.
  await plugin.event(idle)
  expect(ciWatchRedelivery(SESSION)).toBeNull()
  expect(prompts()).toHaveLength(1)
  const output = { parts: [] as unknown[] }
  await plugin["chat.message"]({ sessionID: SESSION, messageID: "msg_after_spent_try" }, output)
  expect(output.parts).toHaveLength(1)
  expect((output.parts[0] as { text: string }).text).toContain(`[concord ci-watch ${String(started.watch_id)}] CI wait success`)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("drained")
})

test("an operator message that arrives before the idle try posts carries the queued report, and the try then posts nothing", async () => {
  const host: { status: string; reply: "parented" } = { status: "busy", reply: "parented" }
  const fixture = hostFixture(host)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idleTimeoutMs: 30, idlePollMs: 5, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success", iterations: 1 }) }])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  await ciWatchSettled(String(started.watch_id))
  configureCiWatch({ idleTimeoutMs: 2_000 })

  // The idle event starts the try, but the operator's message makes the session busy before the try posts.
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: SESSION } } })
  const output = { parts: [] as unknown[] }
  await plugin["chat.message"]({ sessionID: SESSION, messageID: "msg_operator" }, output)
  expect(output.parts).toHaveLength(1)
  expect((output.parts[0] as { text: string }).text).toContain(`[concord ci-watch ${String(started.watch_id)}] CI wait success`)
  host.status = "idle"
  await ciWatchRedelivery(SESSION)
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("drained")
})

test("a report queued while an idle redelivery is in flight gets its own try on the next idle event", async () => {
  const host: { status: string; reply: "none" } = { status: "busy", reply: "none" }
  const fixture = hostFixture(host)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idleTimeoutMs: 30, idlePollMs: 5, confirmPollMs: 2, confirmWindowMs: 500 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
    { stdout: JSON.stringify({ status: "failure", iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const idle = { event: { type: "session.idle", properties: { sessionID: SESSION } } }
  const prompts = () => fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  const first = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  await ciWatchSettled(String(first.watch_id))

  host.status = "idle"
  await plugin.event(idle)
  await until(() => prompts().length === 1, "the first idle redelivery to send")
  host.status = "busy"
  const second = result(await plugin.tool.concord_ci_watch.execute!({ repo: "owner/name", selector: { kind: "pr", value: "2" } }, context))
  await ciWatchSettled(String(second.watch_id))
  expect(receiptFor(ciWatchSnapshot(SESSION), String(second.watch_id))!.delivery_state).toBe("queued")
  // The first try is still inside its confirm window.
  expect(ciWatchRedelivery(SESSION)).not.toBeNull()
  expect(prompts()).toHaveLength(1)

  host.status = "idle"
  await plugin.event(idle)
  await until(() => prompts().length === 2, "the second report's own idle redelivery to send")
  const text = (prompts()[1].body as { parts: Array<{ text: string }> }).parts[0].text
  expect(text).toContain(`[concord ci-watch ${String(second.watch_id)}]`)
  expect(text).not.toContain(`[concord ci-watch ${String(first.watch_id)}]`)
  await ciWatchRedelivery(SESSION)
  expect(ciWatchRedelivery(SESSION)).toBeNull()
})

test("a report queued after its session already went idle gets its one try at once, with no idle event, and admission frees", async () => {
  const host: { status: string; reply: "none" | "parented" } = { status: "idle", reply: "none" }
  const fixture = hostFixture(host)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxQueuedPerSession: 1, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  // The first wake is lost: the host persists it and no turn answers. The session is idle throughout.
  const post = fixture.client.post
  fixture.client.post = async (request: { url: string; body?: unknown }) => {
    const answer = await post(request)
    if (request.url === "/session/{id}/prompt_async") host.reply = "parented"
    return answer
  }
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const first = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  expect(first.status).toBe("started")
  await ciWatchSettled(String(first.watch_id))
  await ciWatchRedelivery(SESSION)
  const prompts = () => fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts()).toHaveLength(2)
  const retried = prompts()[1].body as { parts: Array<{ text: string }> }
  expect(retried.parts[0].text).toContain(`[concord ci-watch ${String(first.watch_id)}] CI wait success for owner/name pr:12.`)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(first.watch_id))!.delivery_state).toBe("delivered")
  const second = result(await plugin.tool.concord_ci_watch.execute!({ repo: "owner/name", selector: { kind: "pr", value: "2" } }, context))
  expect(second.status).toBe("started")
  await ciWatchSettled(String(second.watch_id))
})

test("the at-once try is the report's one idle try: unanswered, the report stays queued, a later idle event sends nothing, and the next message carries it", async () => {
  const fixture = hostFixture({ status: "idle", reply: "none" })
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success", iterations: 1 }) }])
  configureCiWatch({ spawner })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  await ciWatchSettled(String(started.watch_id))
  await ciWatchRedelivery(SESSION)
  const prompts = () => fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")
  expect(prompts()).toHaveLength(2)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("queued")
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: SESSION } } })
  expect(ciWatchRedelivery(SESSION)).toBeNull()
  expect(prompts()).toHaveLength(2)
  const output = { parts: [] as unknown[] }
  await plugin["chat.message"]({ sessionID: SESSION, messageID: "msg_after_at_once_try" }, output)
  expect(output.parts).toHaveLength(1)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("drained")
})

test("a status read that fails when the report is queued is logged, and the session's next idle event still gives the report its try", async () => {
  const fixture = hostFixture({ status: "idle", reply: "parented" })
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success", iterations: 1 }) }])
  configureCiWatch({ spawner })
  // The status route fails until the session's idle event, so settle cannot deliver and the queue-time read fails.
  let statusFails = true
  const get = fixture.client.get
  fixture.client.get = async (request: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown> }) => {
    if (request.url === "/session/status" && statusFails) return { response: new Response(null, { status: 503 }) }
    return get(request)
  }
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  const started = result(await plugin.tool.concord_ci_watch.execute!(watchArgs, context))
  await ciWatchSettled(String(started.watch_id))
  await ciWatchRedelivery(SESSION)
  const failed = fixture.posts.filter((post) => post.url === "/log" && String((post.body as { message: string }).message).includes("could not read whether session"))
  expect(failed).toHaveLength(1)
  expect((failed[0].body as { level: string }).level).toBe("warn")
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("queued")
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(0)

  statusFails = false
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: SESSION } } })
  await ciWatchRedelivery(SESSION)
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(1)
  expect(receiptFor(ciWatchSnapshot(SESSION), String(started.watch_id))!.delivery_state).toBe("delivered")
})

test("an idle event for a session with no queued report, or an event of another type, starts no redelivery", async () => {
  const fixture = hostFixture({ reply: "parented" })
  const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
  await plugin.event({ event: { type: "session.idle", properties: { sessionID: SESSION } } })
  await plugin.event({ event: { type: "session.status", properties: { sessionID: SESSION, status: { type: "idle" } } } })
  expect(ciWatchRedelivery(SESSION)).toBeNull()
  expect(fixture.posts.filter((post) => post.url === "/session/{id}/prompt_async")).toHaveLength(0)
})

test("one default merge watch survives thirty minutes and delivers the merge outcome", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
  let now = Date.now()
  const clock = spyOn(Date, "now").mockImplementation(() => now)
  const requests: Record<string, unknown>[] = []
  configureCiWatch({ spawner: async (_argv, stdin) => {
    requests.push(JSON.parse(stdin))
    if (requests.length === 1) {
      now += 35 * 60_000
      return { exitCode: 0, stdout: JSON.stringify({ status: "pending", merge_queue: { present: true, state: "AWAITING_CHECKS", position: 1 } }), stderr: "" }
    }
    return { exitCode: 0, stdout: JSON.stringify({ status: "merged" }), stderr: "" }
  } })
  try {
    const started = await startWatch(fixture, { ...watchArgs, mode: "merge" })
    expect(started.time_seconds_max).toBe(7200)
    await ciWatchSettled(String(started.watch_id))
    expect(requests).toHaveLength(2)
    expect(requests[0].state_file).toBe(requests[1].state_file)
    expect(requests[0].time_seconds_max).toBeUndefined()
    const notice = fixture.posts.find((post) => post.url === "/session/{id}/message")!
    expect(JSON.stringify(notice.body)).toContain("7200s")
    const delivery = fixture.posts.find((post) => post.url === "/session/{id}/prompt_async")!
    expect((delivery.body as { parts: Array<{ text: string }> }).parts[0].text).toContain('"status": "merged"')
  } finally {
    clock.mockRestore()
  }
})

test("the watcher ceiling preserves the last queue observation and explicit shorter budget", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20, sliceTimeoutMs: 1 })
  let now = Date.now()
  const clock = spyOn(Date, "now").mockImplementation(() => now)
  let calls = 0
  configureCiWatch({ spawner: async (_argv, stdin) => {
    calls++
    expect(JSON.parse(stdin).time_seconds_max).toBe(600)
    now += 700_000
    return { exitCode: 0, stdout: JSON.stringify({ status: "pending", merge_queue: { present: true, state: "AWAITING_CHECKS", position: 2 } }), stderr: "" }
  } })
  try {
    const started = await startWatch(fixture, { ...watchArgs, mode: "merge", time_seconds_max: 600 })
    expect(started.time_seconds_max).toBe(600)
    await ciWatchSettled(String(started.watch_id))
    expect(calls).toBe(1)
    const delivery = fixture.posts.find((post) => post.url === "/session/{id}/prompt_async")!
    const text = (delivery.body as { parts: Array<{ text: string }> }).parts[0].text
    expect(text).toContain('"status": "timeout"')
    expect(text).toContain('"state": "AWAITING_CHECKS"')
    expect(text).toContain('"position": 2')
  } finally {
    clock.mockRestore()
  }
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
    const handle = timer.mock.results[0].value as ReturnType<typeof setInterval>
    expect(handle.hasRef()).toBe(false)
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
  const noticeAt = fixture.posts.map((post) => post.url).lastIndexOf("/session/{id}/message")
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

// The ci-watch lifecycle snapshot: `ciWatchSnapshot(sessionID, sinceSequence?)`
// is the read API over the watcher lifecycle on ci-watch.ts.

type CiWatchSnapshot = ReturnType<typeof ciWatchSnapshot>
type SliceResult = { exitCode: number; stdout: string; stderr: string }
type SliceDeferred = { promise: Promise<SliceResult>; resolve: (value: SliceResult) => void }

// A 40-hex sha already pinned by the selector-value test above, plus a second
// distinct 40-hex sha for the superseded head.
const SHA_BASE = "d6dcfb372fbac68788410ce2a03e20ed88c2fa84"
const SHA_NEXT = "0123456789abcdef0123456789abcdef01234567"
// A realistic success carries complete counts and a sha; a PR success also
// carries head_sha equal to that sha.
const COMPLETE_CHECKS = { total: 1, passing: 1, failing: 0, skipped: 0, pending: 0 }

function activeRecord(snapshot: CiWatchSnapshot, watchId: string) {
  return (snapshot.active as Array<Record<string, any>>).find((record) => record.watch_id === watchId)
}

function receiptFor(snapshot: CiWatchSnapshot, watchId: string) {
  return (snapshot.receipts as Array<Record<string, any>>).find((record) => record.watch_id === watchId)
}

test("the snapshot shows simultaneous watches, keeps the epoch stable on dedup, filters by session, and detaches reads", async () => {
  const fixture = hostFixture({ reply: "parented" })
  const client = fixture.client
  const get = client.get
  // Serve each requested session its own identity record so a second
  // coordinator session passes the managed-scope lookup like the first.
  client.get = async (request: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown> }) => {
    if (request.url === "/session/{id}") {
      return {
        data: { id: String(request.path?.id), agent: "concord-1", model: { id: "glm-test", providerID: "zai" } },
        response: new Response(null, { status: 200 }),
      }
    }
    return get(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const held = new Map<string, SliceDeferred>()
  const calls: string[] = []
  const spawner: VerbSpawner = async (_argv, stdin) => {
    calls.push(stdin)
    const slice: SliceDeferred = deferred<SliceResult>()
    held.set(String(JSON.parse(stdin).selector.value), slice)
    return slice.promise
  }
  configureCiWatch({ spawner })

  const pr = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "12" }, mode: "checks" })
  const run = await startWatch(fixture, { repo: "owner/name", selector: { kind: "run", value: "77" }, mode: "merge" })
  expect(pr.status).toBe("started")
  expect(run.status).toBe("started")
  const prId = String(pr.watch_id)
  const runId = String(run.watch_id)

  const first = ciWatchSnapshot(SESSION)
  expect(first.version).toBe(1)
  expect(first.pid).toBe(process.pid)
  expect(typeof first.owner_started_at).toBe("number")
  expect(typeof first.read_at).toBe("number")
  expect(typeof first.sequence).toBe("number")
  expect(first.session_id).toBe(SESSION)
  expect(first.active as Array<unknown>).toHaveLength(2)
  const prRecord = activeRecord(first, prId)!
  const runRecord = activeRecord(first, runId)!
  expect(prRecord.session_id).toBe(SESSION)
  expect(prRecord.repo).toBe("owner/name")
  expect(prRecord.selector).toEqual({ kind: "pr", value: "12" })
  expect(prRecord.mode).toBe("checks")
  expect(prRecord.phase).toBe("observing")
  expect(prRecord.binding_id).toBeNull()
  expect(prRecord.consecutive_slice_failures).toBe(0)
  expect(prRecord.last_observation).toBeNull()
  expect(runRecord.session_id).toBe(SESSION)
  expect(runRecord.mode).toBe("merge")
  expect(runRecord.phase).toBe("observing")
  expect(runRecord.last_observation).toBeNull()

  // A duplicate start reuses the active identity: no new slice, no new epoch,
  // and the owner epoch stays stable.
  const duplicate = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "12" }, mode: "checks" })
  expect(duplicate.status).toBe("already_watching")
  expect(String(duplicate.watch_id)).toBe(prId)
  expect(calls).toHaveLength(2)
  const afterDedup = ciWatchSnapshot(SESSION)
  expect(afterDedup.runtime_epoch).toBe(first.runtime_epoch)
  expect(afterDedup.active as Array<unknown>).toHaveLength(2)
  expect(activeRecord(afterDedup, prId)!.watch_epoch).toBe(prRecord.watch_epoch)

  // Another session's watches do not leak across session-filtered snapshots.
  const otherContext = { sessionID: "watch-session-b", agent: "Concord", abort: new AbortController().signal }
  const other = result(await concord_ci_watch.execute({ repo: "owner/name", selector: { kind: "sha", value: SHA_BASE } }, otherContext))
  expect(other.status).toBe("started")
  const otherId = String(other.watch_id)
  const otherSnapshot = ciWatchSnapshot("watch-session-b")
  expect(otherSnapshot.session_id).toBe("watch-session-b")
  const otherActive = otherSnapshot.active as Array<Record<string, any>>
  expect(otherActive).toHaveLength(1)
  expect(otherActive[0].watch_id).toBe(otherId)
  expect(otherActive.every((record) => record.session_id === "watch-session-b")).toBe(true)
  expect((first.active as Array<Record<string, any>>).some((record) => record.watch_id === otherId)).toBe(false)

  // Reads detach: mutating a returned snapshot never reaches the owner state,
  // and later state changes never move a previously returned snapshot.
  const detachedActive: Array<Record<string, unknown>> = first.active
  detachedActive.push({ watch_id: "injected" })
  prRecord.phase = "corrupted"
  const fresh = ciWatchSnapshot(SESSION)
  expect(fresh.active as Array<unknown>).toHaveLength(2)
  expect(activeRecord(fresh, prId)!.phase).toBe("observing")
  // An unmutated snapshot captured before settlement carries the second
  // direction: the owner's later state changes must not reach it either.
  const beforeSettle = ciWatchSnapshot(SESSION)

  held.get("12")!.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, head_sha: SHA_BASE, iterations: 1 }), stderr: "" })
  await ciWatchSettled(prId)
  expect(receiptFor(ciWatchSnapshot(SESSION), prId)!.delivery_state).toBe("delivered")
  // The mutated detached record stays corrupted: settlement never rewrote it.
  expect(prRecord.phase).toBe("corrupted")
  expect(activeRecord(beforeSettle, prId)!.phase).toBe("observing")
  expect(activeRecord(beforeSettle, prId)!.last_observation).toBeNull()

  // The remaining held watches must settle so the reset leaks no loops.
  held.get("77")!.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, iterations: 1 }), stderr: "" })
  held.get(SHA_BASE)!.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, iterations: 1 }), stderr: "" })
  await ciWatchSettled(runId)
  await ciWatchSettled(otherId)
})

test("a pending observation with an exact sha is superseded by a terminal receipt that stays retained after delivery", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const first = deferred<SliceResult>()
  const second = deferred<SliceResult>()
  const slices = [first.promise, second.promise]
  let stateFile = ""
  const spawner: VerbSpawner = async (_argv, stdin) => {
    stateFile = String(JSON.parse(stdin).state_file)
    return slices.shift()!
  }
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  const watchId = String(started.watch_id)
  const snap = () => ciWatchSnapshot(SESSION)
  expect(activeRecord(snap(), watchId)!.last_observation).toBeNull()

  // Realistic pending report: a full 40-hex sha and the resume state file.
  first.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "pending", sha: SHA_BASE, state_file: stateFile }), stderr: "" })
  await until(() => activeRecord(snap(), watchId)!.last_observation !== null, "the pending observation")
  const observing = activeRecord(snap(), watchId)!
  expect(observing.phase).toBe("observing")
  const observation = observing.last_observation as Record<string, any>
  expect(observation.status).toBe("pending")
  expect(observation.sha).toBe(SHA_BASE)
  expect(observation.head_sha).toBeNull()
  expect(observation.run_url).toBeNull()
  expect(observation.checks).toBeNull()
  expect(observation.evidence).toBe("exact_head")
  expect(typeof observation.slice_started_at).toBe("number")
  expect(typeof observation.report_received_at).toBe("number")

  // Terminal superseded: the watched sha is the old one, head_sha differs.
  second.resolve({ exitCode: 0, stdout: JSON.stringify({ status: "superseded", sha: SHA_BASE, head_sha: SHA_NEXT, iterations: 2 }), stderr: "" })
  await ciWatchSettled(watchId)
  const settledSnap = snap()
  expect(Array.isArray(settledSnap.receipts)).toBe(true)
  const receipt = receiptFor(settledSnap, watchId)!
  expect(typeof receipt.terminal_sequence).toBe("number")
  expect(receipt.delivery_state).toBe("delivered")
  const terminal = receipt.terminal_observation as Record<string, any>
  expect(terminal.status).toBe("superseded")
  expect(terminal.sha).toBe(SHA_BASE)
  expect(terminal.head_sha).toBe(SHA_NEXT)
  expect(terminal.evidence).toBe("exact_head")
  // The delivered receipt is retained, not dropped.
  expect(receiptFor(snap(), watchId)).toBeDefined()
  const clock = spyOn(Date, "now")
  try {
    clock.mockReturnValue(Number(terminal.slice_started_at) + 180_001)
    const stale = receiptFor(snap(), watchId)!
    for (const key of ["last_observation", "terminal_observation"]) {
      expect(stale[key]).toMatchObject({
        stale: true, evidence: "unavailable",
        slice_started_at: terminal.slice_started_at,
        report_received_at: terminal.report_received_at,
      })
    }
    expect(terminal.evidence).toBe("exact_head")
    clock.mockReturnValue(Number(terminal.report_received_at) - 1)
    const backwards = receiptFor(snap(), watchId)!
    expect(backwards.last_observation.gaps).toContain("clock_gap")
    expect(backwards.terminal_observation.gaps).toContain("clock_gap")
  } finally {
    clock.mockRestore()
  }
})

test("a terminal report recorded while delivery is blocked moves delivering to queued and drains with the receipt preserved", async () => {
  const fixture = hostFixture()
  const client = fixture.client
  const post = client.post
  const gate = deferred<{ response: Response }>()
  let promptHeld = false
  client.post = async (request: { url: string; body?: unknown }) => {
    if (request.url === "/session/{id}/prompt_async") {
      promptHeld = true
      return gate.promise
    }
    return post(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([{ stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, head_sha: SHA_BASE, iterations: 1 }) }])
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  const watchId = String(started.watch_id)
  const snap = () => ciWatchSnapshot(SESSION)

  // The prompt post is held in flight, so the terminal report is delivering.
  await until(() => promptHeld, "the held prompt post")
  expect(activeRecord(snap(), watchId)!.phase).toBe("delivering")
  expect(receiptFor(snap(), watchId)!.delivery_state).toBe("delivering")
  gate.resolve({ response: new Response(null, { status: 400 }) })
  await ciWatchSettled(watchId)

  const queuedSnap = snap()
  expect(activeRecord(queuedSnap, watchId)!.phase).toBe("queued")
  const receipt = receiptFor(queuedSnap, watchId)!
  expect(receipt.delivery_state).toBe("queued")
  expect((receipt.terminal_observation as Record<string, any>).status).toBe("success")
  expect((receipt.terminal_observation as Record<string, any>).evidence).toBe("exact_head")

  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_snapshot_drain", output)
  expect(output.parts).toHaveLength(1)
  const drained = receiptFor(snap(), watchId)!
  expect(drained.delivery_state).toBe("drained")
  expect((drained.terminal_observation as Record<string, any>).status).toBe("success")
})

test("success reports without a usable sha project unavailable evidence, never exact_head", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: "not-a-sha", iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  const missing = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "12" } })
  const malformed = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "13" } })
  await ciWatchSettled(String(missing.watch_id))
  await ciWatchSettled(String(malformed.watch_id))
  const snapshot = ciWatchSnapshot(SESSION)

  const missingObservation = receiptFor(snapshot, String(missing.watch_id))!.terminal_observation as Record<string, any>
  expect(missingObservation.status).toBe("success")
  expect(missingObservation.checks).toEqual(COMPLETE_CHECKS)
  expect(missingObservation.evidence).toBe("unavailable")
  expect(missingObservation.sha).toBeNull()
  expect(missingObservation.head_sha).toBeNull()
  const malformedObservation = receiptFor(snapshot, String(malformed.watch_id))!.terminal_observation as Record<string, any>
  expect(malformedObservation.evidence).toBe("unavailable")
  expect(malformedObservation.evidence).not.toBe("exact_head")
})

test("a pr watch that names head_sha projects exact_head only for a report about that commit", async () => {
  const pushed = "6459f13778fcad4b42f771ac4988f55897f635f5"
  const replaced = "79dfa01fdc5852f7bdbe9510c91dc75a1a4712f9"
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: pushed, head_sha: pushed, iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: replaced, head_sha: replaced, iterations: 1 }) },
  ])
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40, spawner })
  const named = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "12" }, head_sha: pushed })
  const other = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "13" }, head_sha: pushed })
  await ciWatchSettled(String(named.watch_id))
  await ciWatchSettled(String(other.watch_id))
  const snapshot = ciWatchSnapshot(SESSION)
  expect((receiptFor(snapshot, String(named.watch_id))!.terminal_observation as Record<string, any>).evidence).toBe("exact_head")
  const mismatched = receiptFor(snapshot, String(other.watch_id))!.terminal_observation as Record<string, any>
  expect(mismatched.evidence).toBe("unavailable")
  expect(mismatched.gaps).toContain("subject_mismatch")
})

test("an adapter-generated error keeps unavailable terminal evidence separate from the earlier observation", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxSliceFailures: 2, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const gate = deferred<void>()
  const calls: string[] = []
  let stateFile = ""
  const spawner: VerbSpawner = async (_argv, stdin) => {
    calls.push(stdin)
    stateFile = String(JSON.parse(stdin).state_file)
    if (calls.length === 1) return { exitCode: 0, stdout: JSON.stringify({ status: "pending", sha: SHA_BASE, state_file: stateFile }), stderr: "" }
    if (calls.length === 2) throw new Error("synthetic slice failure")
    await gate.promise
    throw new Error("second synthetic slice failure")
  }
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  const watchId = String(started.watch_id)
  const snap = () => ciWatchSnapshot(SESSION)

  // One failure is counted on the active record while the pending observation
  // with its exact sha stays intact beside it.
  await until(() => activeRecord(snap(), watchId)!.consecutive_slice_failures === 1, "the counted slice failure")
  const failing = activeRecord(snap(), watchId)!
  expect((failing.last_observation as Record<string, any>).sha).toBe(SHA_BASE)
  expect((failing.last_observation as Record<string, any>).evidence).toBe("exact_head")

  gate.resolve()
  await ciWatchSettled(watchId)
  const terminal = receiptFor(snap(), watchId)!.terminal_observation as Record<string, any>
  expect(terminal.status).toBe("error")
  expect(terminal.evidence).toBe("unavailable")
  expect(terminal.sha).toBeNull()
})

test("repeating a completed selector starts a new watch epoch and retains both receipts", async () => {
  const fixture = hostFixture({ reply: "parented" })
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  const { spawner } = verbFixture([
    { stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, head_sha: SHA_BASE, iterations: 1 }) },
    { stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, head_sha: SHA_BASE, iterations: 1 }) },
  ])
  configureCiWatch({ spawner })
  const firstWatch = await startWatch(fixture)
  const watchId = String(firstWatch.watch_id)
  await ciWatchSettled(watchId)

  // A completed selector is no longer active, so the repeat mints a fresh
  // identity: a new watch_id carrying its own watch_epoch, never ID reuse.
  const repeat = await startWatch(fixture)
  expect(repeat.status).toBe("started")
  const repeatId = String(repeat.watch_id)
  expect(repeatId).not.toBe(watchId)
  await ciWatchSettled(repeatId)

  const settledSnapshot = ciWatchSnapshot(SESSION)
  const receipts = settledSnapshot.receipts as Array<Record<string, any>>
  const firstReceipt = receiptFor(settledSnapshot, watchId)
  const repeatReceipt = receiptFor(settledSnapshot, repeatId)
  // Each identity is selected by its own ID, exactly once.
  expect(firstReceipt).toBeDefined()
  expect(repeatReceipt).toBeDefined()
  expect(receipts.filter((record) => record.watch_id === watchId)).toHaveLength(1)
  expect(receipts.filter((record) => record.watch_id === repeatId)).toHaveLength(1)
  expect(firstReceipt!.watch_epoch).not.toBe(repeatReceipt!.watch_epoch)
})

test("the receipts ring holds 64 delivered watches, reports the eviction floor, and keeps a queued report visible", async () => {
  const fixture = hostFixture({ reply: "parented" })
  let busy = false
  const client = fixture.client
  const get = client.get
  client.get = async (request: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown> }) => {
    if (busy && request.url === "/session/status") {
      return { data: { [SESSION]: { type: "busy" } }, response: new Response(null, { status: 200 }) }
    }
    return get(request)
  }
  hostControlPlane().bind(client)
  bindCiWatchClient(client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, idleTimeoutMs: 30, idlePollMs: 5, confirmPollMs: 2, confirmWindowMs: 40 })
  const spawner: VerbSpawner = async () => ({
    exitCode: 0,
    stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, iterations: 1 }),
    stderr: "",
  })
  configureCiWatch({ spawner })

  const watchIds: string[] = []
  for (let value = 1; value <= 65; value++) {
    const started = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: String(value) } })
    expect(started.status).toBe("started")
    const watchId = String(started.watch_id)
    watchIds.push(watchId)
    await ciWatchSettled(watchId)
  }

  const full = ciWatchSnapshot(SESSION)
  expect(full.history_gap).toBe(false)
  const receipts = full.receipts as Array<Record<string, any>>
  expect(receipts).toHaveLength(64)
  const floor = full.retention_floor as number
  expect(typeof floor).toBe("number")
  for (const receipt of receipts) {
    expect(receipt.terminal_sequence).toBeGreaterThanOrEqual(floor)
  }
  // A sinceSequence below the eviction floor reports the history gap; the
  // floor itself does not.
  expect(ciWatchSnapshot(SESSION, floor - 1).history_gap).toBe(true)
  expect(ciWatchSnapshot(SESSION, floor).history_gap).toBe(false)
  // Exactly the oldest watch was evicted; the other 64 are retained.
  expect(receipts.some((receipt) => receipt.watch_id === watchIds[0])).toBe(false)
  expect(receipts.every((receipt) => watchIds.includes(receipt.watch_id))).toBe(true)

  // A report that only queues stays visible in the active union after the
  // ring has started evicting.
  busy = true
  const queued = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: "66" } })
  expect(queued.status).toBe("started")
  await ciWatchSettled(String(queued.watch_id))
  expect(activeRecord(ciWatchSnapshot(SESSION), String(queued.watch_id))!.phase).toBe("queued")
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_ring_drain", output)
  expect(output.parts).toHaveLength(1)
})

test("a queued adapter error retains its terminal observation after receipt eviction", async () => {
  const fixture = hostFixture({ reply: "parented" })
  const post = fixture.client.post
  // The wake and the report's at-once idle try are both refused, so the report stays queued.
  let rejectPrompts = 2
  fixture.client.post = async (request: { url: string; body?: unknown }) => {
    if (request.url === "/session/{id}/prompt_async" && rejectPrompts > 0) {
      rejectPrompts--
      return { response: new Response(null, { status: 400 }) }
    }
    return post(request)
  }
  hostControlPlane().bind(fixture.client)
  bindCiWatchClient(fixture.client)
  configureCoreBinary("/synthetic/concord")
  configureCiWatch({ stateDir: STATE_DIR, maxSliceFailures: 2, idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 40 })
  let slices = 0
  const spawner: VerbSpawner = async () => {
    slices++
    if (slices === 1) return { exitCode: 0, stdout: JSON.stringify({ status: "pending", sha: SHA_BASE }), stderr: "" }
    if (slices <= 3) throw new Error("synthetic slice failure")
    return { exitCode: 0, stdout: JSON.stringify({ status: "success", checks: COMPLETE_CHECKS, sha: SHA_BASE, head_sha: SHA_BASE }), stderr: "" }
  }
  configureCiWatch({ spawner })
  const started = await startWatch(fixture)
  const watchId = String(started.watch_id)
  await ciWatchSettled(watchId)
  expect(receiptFor(ciWatchSnapshot(SESSION), watchId)!.terminal_observation.status).toBe("error")
  for (let value = 13; value < 77; value++) {
    const next = await startWatch(fixture, { repo: "owner/name", selector: { kind: "pr", value: String(value) } })
    expect(next.status).toBe("started")
    await ciWatchSettled(String(next.watch_id))
  }
  const snapshot = ciWatchSnapshot(SESSION)
  expect(receiptFor(snapshot, watchId)).toBeUndefined()
  const queued = activeRecord(snapshot, watchId)!
  expect(queued.phase).toBe("queued")
  expect(queued.last_observation).toMatchObject({ status: "pending", sha: SHA_BASE })
  expect(queued.terminal_observation).toMatchObject({ status: "error", sha: null, evidence: "unavailable" })
  const output = { parts: [] as unknown[] }
  drainQueuedCiReports(SESSION, "msg_evicted_error", output)
  expect(output.parts).toHaveLength(1)
  expect(activeRecord(ciWatchSnapshot(SESSION), watchId)).toBeUndefined()
})
