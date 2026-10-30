// Opt-in real-host check. The core is a fixture; the host, move route, prompt
// route, message store and every executing ToolContext are native and unpatched.
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { mkdtempSync, mkdirSync, readFileSync, rmSync, watch, writeFileSync } from "node:fs"
import { networkInterfaces, tmpdir } from "node:os"
import { join } from "node:path"
import { pathToFileURL } from "node:url"

const pin = "53d1eabb61e21162157817bf677da0a4ad3332e3"
const source = process.env.CONCORD_NATIVE_HOST_SOURCE
assert(source, "CONCORD_NATIVE_HOST_SOURCE must name the pinned public host checkout")
assert.equal(execFileSync("git", ["-C", source, "rev-parse", "HEAD"], { encoding: "utf8" }).trim(), pin)
const binary = process.env.OPENCODE_BIN ?? Bun.which("opencode")
assert(binary, "the installed native host is required")
const version = execFileSync(binary, ["--version"], { encoding: "utf8", timeout: 5_000 }).trim()
assert.equal(version, "1.18.35")
const loopback = Object.values(networkInterfaces()).flat().find(address => address?.internal && address.family === "IPv4")?.address
assert(loopback, "the native fixture requires an IPv4 loopback interface")
const { testProviderConfig } = await import(pathToFileURL(join(source, "packages/opencode/test/lib/test-provider.ts")).href)
const scratch = mkdtempSync(join(tmpdir(), "concord-native-start-"))
const origin = join(scratch, "main")
const target = join(scratch, "claimed")
const home = join(scratch, "home")
const log = join(scratch, "context.jsonl")
const plugin = join(scratch, "fixture-plugin.ts")
const request = { title: "Native continuation fixture", value_statement: "Verify native placement", kind: "task", task: "Observe native contexts", idempotency_key: "native-start-key" }
mkdirSync(origin)
mkdirSync(home)
writeFileSync(log, "")
const adapterURL = (name: string) => new URL(name, import.meta.url).href
writeFileSync(plugin, `
import Plugin from ${JSON.stringify(adapterURL("./concord-plugin.ts"))}
import { configureConcordAdapter } from ${JSON.stringify(adapterURL("./concord.ts"))}
import { configureCoreBinary } from ${JSON.stringify(adapterURL("./core-runtime.ts"))}
import { configureHostLease } from ${JSON.stringify(adapterURL("./host-lease.ts"))}
import { armedClaimedWorktree, unlandedClaimedWorktree } from ${JSON.stringify(adapterURL("./claimed-worktree.ts"))}
import { appendFileSync } from "node:fs"
const write = value => appendFileSync(${JSON.stringify(log)}, JSON.stringify(value) + "\\n")
const bootstrapKeys = new Set()
let lastContext
export default async input => {
  const hooks = await Plugin(input)
  configureCoreBinary("concord")
  configureHostLease({ reset: true })
  configureConcordAdapter({ runner: { async run(argv, body) {
    const request = JSON.parse(body || "{}")
    let data
    switch (argv[1]) {
      case "project-resolve": data = { project_id: "project-native", product_ids: ["product-native"], scope_version: "1", main_worktree: request.directory === ${JSON.stringify(origin)} }; break
      case "work-bootstrap": data = { schema_version: "1.0", operation_id: "operation-native", replayed: bootstrapKeys.has(request.idempotency_key), product_id: "product-native", project_id: "project-native", work_id: "work-native", work_version: 1, worktree: { set_id: "set-native", path: ${JSON.stringify(target)}, branch: "fixture", base_sha: "a".repeat(40), state: "active" } }; bootstrapKeys.add(request.idempotency_key); break
      case "session-prepare": data = { schema_version: "1.0", agent: request.agent, directory: ${JSON.stringify(target)}, product_id: "product-native", work_id: "work-native", title: "Native fixture", prompt: "Observe native contexts" }; break
      default: throw new Error("unexpected fixture core call: " + argv.join(" "))
    }
    write({ kind: "core", command: argv[1], request })
    return { exitCode: 0, stdout: JSON.stringify(data), stderr: "" }
  } } })
  const start = hooks.tool.concord_work_start
  return { ...hooks,
    async event(input) {
      if (["session.idle", "session.error", "session.deleted"].includes(input.event.type)) write({ kind: "event", event: input.event, aborted: lastContext?.abort.aborted })
      await hooks.event(input)
    },
    async "chat.message"(input, output) {
      write({ kind: "chat", sessionID: input.sessionID, messageID: input.messageID, parts: output.parts })
      await hooks["chat.message"](input, output)
    },
    tool: { ...hooks.tool,
    concord_work_start: { ...start, async execute(args, context) {
      lastContext = context
      const value = await start.execute(args, context)
      write({ kind: "start", args, directory: context.directory, sessionID: context.sessionID, messageID: context.messageID, result: JSON.parse(value.output), armed: armedClaimedWorktree(context.sessionID), pending: unlandedClaimedWorktree(context.sessionID), aborted: context.abort.aborted })
      return value
    } },
    fixture_context: { description: "Observe the native context after a successful work start", args: {}, async execute(args, context) {
      const value = { kind: "ordinary", directory: context.directory, sessionID: context.sessionID, armed: armedClaimedWorktree(context.sessionID), pending: unlandedClaimedWorktree(context.sessionID) }
      write(value)
      return JSON.stringify(value)
    } }
  } }
}
`)

let sequence = 0
const provider = Bun.serve({ hostname: loopback, port: 0, async fetch(incoming) {
  const body = await incoming.json() as { messages: Array<{ role: string; content?: unknown }> }
  const last = body.messages.at(-1)
  let delta: Record<string, unknown> = { content: "Waiting for the native continuation." }
  let finish = "stop"
  if (last?.role === "user") {
    delta = { tool_calls: [{ index: 0, id: `call_${++sequence}`, type: "function", function: { name: "concord_work_start", arguments: JSON.stringify(request) } }] }
    finish = "tool_calls"
  } else if (last?.role === "tool" && (typeof last.content === "string" ? last.content : JSON.stringify(last.content)).includes('"outcome":"ok"')) {
    delta = { tool_calls: [{ index: 0, id: `call_${++sequence}`, type: "function", function: { name: "fixture_context", arguments: "{}" } }] }
    finish = "tool_calls"
  }
  const chunk = (value: Record<string, unknown>, reason: string | null) => `data: ${JSON.stringify({ id: "chatcmpl-native-fixture", object: "chat.completion.chunk", created: 1, model: "test-model", choices: [{ index: 0, delta: value, finish_reason: reason }] })}\n\n`
  return new Response(chunk(delta, null) + chunk({}, finish) + "data: [DONE]\n\n", { headers: { "content-type": "text/event-stream" } })
} })

const config = { $schema: "https://opencode.ai/config.json", ...testProviderConfig(`${provider.url}v1`), model: "test/test-model", small_model: "test/test-model", enabled_providers: ["test"], plugin: [pathToFileURL(plugin).href], share: "disabled", snapshot: false, permission: "allow", default_agent: "concord-1", agent: { "concord-1": { mode: "primary", model: "test/test-model" }, title: { disable: true } } }
const env: NodeJS.ProcessEnv = { ...process.env, HOME: home, OPENCODE_TEST_HOME: home, XDG_CONFIG_HOME: join(home, "config"), XDG_DATA_HOME: join(home, "data"), XDG_STATE_HOME: join(home, "state"), XDG_CACHE_HOME: join(home, "cache"), OPENCODE_CONFIG_CONTENT: JSON.stringify(config), OPENCODE_DISABLE_PROJECT_CONFIG: "1", OPENCODE_PURE: "0", OPENCODE_DISABLE_DEFAULT_PLUGINS: "1", OPENCODE_DISABLE_AUTOUPDATE: "1", OPENCODE_DISABLE_AUTOCOMPACT: "1", OPENCODE_DISABLE_MODELS_FETCH: "1", OPENCODE_AUTH_CONTENT: "{}", OPENCODE_SERVER_PASSWORD: "" }
delete env.OPENCODE_CONFIG
delete env.OPENCODE_CONFIG_DIR
delete env.CONCORD_DB_PATH
delete env.CONCORD_SELECTED_PRODUCT_ID
let host: ReturnType<typeof Bun.spawn> | undefined
let output = ""
let errors = ""
let observer: ReturnType<typeof watch> | undefined
try {
  const git = (args: string[]) => execFileSync("git", ["-C", origin, ...args], { env, encoding: "utf8" })
  git(["init", "--quiet", "--initial-branch=main"])
  git(["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "fixture"])
  git(["worktree", "add", "--quiet", "--detach", target])
  host = Bun.spawn([binary, "serve", "--hostname", loopback, "--port", "0"], { cwd: origin, env, stdout: "pipe", stderr: "pipe", stdin: "ignore" })
  const server = host
  const ready = new Promise<string>((resolve, reject) => {
    void (async () => {
      for await (const bytes of server.stdout as ReadableStream<Uint8Array>) {
        output += new TextDecoder().decode(bytes)
        const match = output.match(/listening on (http:\/\/[^\s]+)/)
        if (match) resolve(match[1])
      }
      reject(new Error("native host exited before listening"))
    })().catch(reject)
  })
  const stderr = (async () => {
    for await (const bytes of server.stderr as ReadableStream<Uint8Array>) errors += new TextDecoder().decode(bytes)
  })()
  const deadline = AbortSignal.timeout(30_000)
  const expired = new Promise<never>((_, reject) => deadline.addEventListener("abort", () => reject(new Error("native fixture exceeded its 30-second host bound")), { once: true }))
  const run = async () => {
    const url = await ready
    const call = async (route: string, body?: object) => {
      const response = await fetch(url + route, { method: body === undefined ? "GET" : "POST", headers: { "content-type": "application/json", "x-opencode-directory": origin }, body: body === undefined ? undefined : JSON.stringify(body), signal: deadline })
      assert(response.ok, `${route}: ${response.status} ${await response.clone().text()}`)
      return response.status === 204 ? undefined : response.json()
    }
    const completed = new Promise<void>((resolve, reject) => {
      observer = watch(log, () => {
        const records = readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map(line => JSON.parse(line))
        const starts = records.filter(record => record.kind === "start")
        const failed = starts.find(record => record.result.outcome === "error" && record.result.error.kind !== "session_directory_mismatch")
        if (failed) reject(new Error(`native work start refused: ${JSON.stringify(failed.result.error)}`))
        else if (starts.length > 1 && starts.at(-1).result.outcome !== "ok") reject(new Error("the automatic replay still has not landed"))
        else if (records.some(record => record.kind === "ordinary")) resolve()
      })
    })
    const session = await call("/session", { title: "Native continuation fixture" }) as { id: string }
    await call(`/session/${session.id}/prompt_async`, { agent: "concord-1", model: { providerID: "test", modelID: "test-model" }, parts: [{ type: "text", text: "Start the native work fixture." }] })
    await completed
    const records = readFileSync(log, "utf8").trim().split("\n").map(line => JSON.parse(line))
    const starts = records.filter(record => record.kind === "start")
    assert.equal(starts.length, 2)
    assert(starts.every(record => record.sessionID === session.id && JSON.stringify(record.args) === JSON.stringify(request)))
    assert.equal(starts[0].directory, origin)
    assert.equal(starts[0].result.error.kind, "session_directory_mismatch")
    assert.equal(starts[0].armed, null)
    assert.equal(starts[1].directory, target)
    assert.equal(starts[1].result.outcome, "ok")
    assert.equal(starts[1].result.work_id, starts[0].result.work_id)
    assert.equal(starts[1].armed, target)
    const ordinary = records.find(record => record.kind === "ordinary")
    assert.equal(ordinary.directory, target)
    assert.equal(ordinary.armed, target)
    assert.equal(ordinary.pending, null)
    const messages = await call(`/session/${session.id}/message`) as Array<{ info: { role: string }; parts: Array<{ synthetic?: boolean; metadata?: Record<string, unknown> }> }>
    const wakes = messages.filter(message => message.info.role === "user" && message.parts.some(part => part.synthetic === true && typeof part.metadata?.["concord.work_start_continuation"] === "string"))
    assert.equal(wakes.length, 1)
    console.log(JSON.stringify({ outcome: "pass", host_version: version, source_pin: pin, fixture_core: true, records }))
  }
  await Promise.race([run(), expired])
  server.kill()
  await server.exited
  await stderr
} catch (error) {
  console.error(JSON.stringify({ outcome: "fail", error: String(error), host_stdout: output, host_stderr: errors, contexts: readFileSync(log, "utf8") }))
  throw error
} finally {
  observer?.close()
  host?.kill()
  await host?.exited
  provider.stop(true)
  rmSync(scratch, { recursive: true, force: true })
}
