// Opt-in native host witness. Only the core authorization is a fixture; Task,
// child sessions, Code Mode, MCP transport and the adapter hook are unpatched.
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { mkdtempSync, mkdirSync, readFileSync, rmSync, watch, writeFileSync } from "node:fs"
import { networkInterfaces, tmpdir } from "node:os"
import { join } from "node:path"
import { pathToFileURL } from "node:url"

const binary = process.env.OPENCODE_BIN ?? Bun.which("opencode")
assert(binary, "the native host is required")
const version = execFileSync(binary, ["--version"], { encoding: "utf8", timeout: 5_000 }).trim()
assert.equal(version, "1.18.35")
const loopback = Object.values(networkInterfaces()).flat().find(address => address?.internal && address.family === "IPv4")?.address
assert(loopback)
const scratch = mkdtempSync(join(tmpdir(), "concord-native-capabilities-"))
const directory = join(scratch, "repository")
const home = join(scratch, "home")
const configDirectory = join(home, "config", "opencode")
const log = join(scratch, "hooks.jsonl")
const plugin = join(scratch, "fixture-plugin.ts")
mkdirSync(directory)
mkdirSync(join(configDirectory, "agents"), { recursive: true })
writeFileSync(log, "")
for (const lane of ["verify", "research", "implement"]) {
  writeFileSync(join(configDirectory, "agents", `concord-${lane}.md`), readFileSync(new URL(`../../.opencode/agents/concord-${lane}.md`, import.meta.url)))
}
const adapterURL = (name: string) => new URL(name, import.meta.url).href
writeFileSync(plugin, `
import Plugin from ${JSON.stringify(adapterURL("./concord-plugin.ts"))}
import { dispatchWindows } from ${JSON.stringify(adapterURL("./dispatch-window.ts"))}
import { agentLanes } from ${JSON.stringify(adapterURL("./generated-agent-lanes.ts"))}
import { appendFileSync } from "node:fs"
const write = value => appendFileSync(${JSON.stringify(log)}, JSON.stringify(value) + "\\n")
export default async input => {
  const hooks = await Plugin(input)
  return {
    async "tool.execute.before"(event, output) {
      if (event.tool === "task") {
        const lane = agentLanes.find(lane => "concord-" + lane.id === output.args.subagent_type)
        const packet = {
          schema_version: "1.0", attempt_id: output.args.prompt, work_id: "work-native-capabilities", step_id: "execute",
          lane_id: lane.id, lane_digest: lane.digest,
          inputs: { task: "Exercise native MCP admission", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: lane.evidence_obligations[0] } }
        }
        dispatchWindows().open(event.sessionID, packet, "sha256:" + "a".repeat(64), input.directory)
      }
      write({ kind: "before", ...event, args: output.args })
      try { await hooks["tool.execute.before"](event, output) }
      catch (error) { write({ kind: "refused", ...event, message: String(error) }); throw error }
    },
    async "tool.execute.after"(event, output) {
      write({ kind: "after", ...event, output })
      // This fixture proves admission, not terminal report signing or core writes.
      if (event.tool === "task") dispatchWindows().takeInFlight(event.sessionID)
      else await hooks["tool.execute.after"](event, output)
    }
  }
}
`)

const transports: Array<{ server: string; tool: string; args: unknown }> = []
const definitions: Record<string, string[]> = { lgrep: ["search_text", "invalidate_cache"], exa: ["web_search_exa"], context7: ["query-docs"], linear: ["update_issue"], vision: ["vision_remove"], concord: ["worker_read"], apply: ["patch"] }
const mcp = Bun.serve({ hostname: loopback, port: 0, async fetch(request) {
  if (request.method !== "POST") return new Response(null, { status: 405 })
  const server = new URL(request.url).pathname.slice(1)
  const body = await request.json() as { id?: number; method: string; params?: { name: string; arguments: unknown } }
  if (body.id === undefined) return new Response(null, { status: 202 })
  let result: unknown
  if (body.method === "initialize") result = { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: server, version: "1.0.0" } }
  else if (body.method === "tools/list") result = { tools: definitions[server].map(name => ({ name, description: name, inputSchema: { type: "object", properties: { path: { type: "string" } } } })) }
  else if (body.method === "tools/call") {
    transports.push({ server, tool: body.params!.name, args: body.params!.arguments })
    result = { content: [{ type: "text", text: "native transport reached" }] }
  } else return Response.json({ jsonrpc: "2.0", id: body.id, error: { code: -32601, message: "Unknown method" } })
  return Response.json({ jsonrpc: "2.0", id: body.id, result })
} })

const script = `
const result = {};
const calls = [
  ["repository", () => tools.lgrep.search_text({path: ${JSON.stringify(directory)}})],
  ["foreign", () => tools.lgrep.search_text({path: ${JSON.stringify(scratch)}})],
  ["cache_mutation", () => tools.lgrep.invalidate_cache({path: ${JSON.stringify(directory)}})],
  ["linear", () => tools.linear.update_issue({})],
  ["vision", () => tools.vision.vision_remove({})],
  ["exa", () => tools.exa.web_search_exa({})],
  ["context7", () => tools.context7["query-docs"]({})],
  ["plugin_collision", () => tools.concord.worker_read({})],
  ["native_collision", () => tools.apply.patch({})]
];
for (const [key, call] of calls) { try { result[key] = await call(); } catch (error) { result[key] = { refused: true }; } }
return result;
`
let sequence = 0
const provider = Bun.serve({ hostname: loopback, port: 0, async fetch(request) {
  const body = await request.json() as { messages: Array<{ role: string; content?: unknown; tool_calls?: unknown[] }> }
  const text = JSON.stringify(body.messages)
  const child = text.includes("agent-lane-packet.v1")
  const usedTool = body.messages.some(message => message.role === "tool")
  let delta: object = { content: "Native fixture complete." }
  let finish = "stop"
  if (!usedTool) {
    const name = child ? "execute" : "task"
    const args = child ? { code: script } : { subagent_type: "concord-verify", prompt: "attempt-native-verify", description: "Native capability witness" }
    delta = { tool_calls: [{ index: 0, id: `call_${++sequence}`, type: "function", function: { name, arguments: JSON.stringify(args) } }] }
    finish = "tool_calls"
  } else if (!child && !text.includes("attempt-native-research")) {
    delta = { tool_calls: [{ index: 0, id: `call_${++sequence}`, type: "function", function: { name: "task", arguments: JSON.stringify({ subagent_type: "concord-research", prompt: "attempt-native-research", description: "Native public-source witness" }) } }] }
    finish = "tool_calls"
  } else if (!child && !text.includes("attempt-native-implement")) {
    delta = { tool_calls: [{ index: 0, id: `call_${++sequence}`, type: "function", function: { name: "task", arguments: JSON.stringify({ subagent_type: "concord-implement", prompt: "attempt-native-implement", description: "Native editor collision witness" }) } }] }
    finish = "tool_calls"
  }
  const chunk = (delta: object, finish_reason: string | null) => `data: ${JSON.stringify({ id: "chatcmpl-native-capabilities", object: "chat.completion.chunk", created: 1, model: "test-model", choices: [{ index: 0, delta, finish_reason }] })}\n\n`
  return new Response(chunk(delta, null) + chunk({}, finish) + "data: [DONE]\n\n", { headers: { "content-type": "text/event-stream" } })
} })

const config = {
  $schema: "https://opencode.ai/config.json", formatter: false, lsp: false,
  provider: { test: { name: "Test", id: "test", env: [], npm: "@ai-sdk/openai-compatible", models: { "test-model": { id: "test-model", name: "Test Model", attachment: false, reasoning: false, temperature: false, tool_call: true, release_date: "2025-01-01", limit: { context: 100_000, output: 10_000 }, cost: { input: 0, output: 0 }, options: {} } }, options: { apiKey: "test-key", baseURL: `${provider.url}v1` } } },
  mcp: Object.fromEntries(Object.keys(definitions).map(server => [server, { type: "remote", url: `${mcp.url}${server}` }])),
  model: "test/test-model", small_model: "test/test-model", enabled_providers: ["test"],
  plugin: [pathToFileURL(plugin).href], share: "disabled", snapshot: false, permission: "allow",
  default_agent: "native-parent", agent: { "native-parent": { mode: "primary", model: "test/test-model" }, "concord-verify": { model: "test/test-model" }, "concord-research": { model: "test/test-model" }, "concord-implement": { model: "test/test-model" }, title: { disable: true } },
}
const env: NodeJS.ProcessEnv = { ...process.env, HOME: home, OPENCODE_TEST_HOME: home, XDG_CONFIG_HOME: join(home, "config"), XDG_DATA_HOME: join(home, "data"), XDG_STATE_HOME: join(home, "state"), XDG_CACHE_HOME: join(home, "cache"), OPENCODE_CONFIG_CONTENT: JSON.stringify(config), OPENCODE_DISABLE_PROJECT_CONFIG: "1", OPENCODE_PURE: "0", OPENCODE_DISABLE_DEFAULT_PLUGINS: "1", OPENCODE_DISABLE_AUTOUPDATE: "1", OPENCODE_DISABLE_AUTOCOMPACT: "1", OPENCODE_DISABLE_MODELS_FETCH: "1", OPENCODE_EXPERIMENTAL_CODE_MODE: "true", OPENCODE_AUTH_CONTENT: "{}", OPENCODE_SERVER_PASSWORD: "" }
for (const key of Object.keys(env)) if (key.startsWith("CONCORD_") || ["OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR"].includes(key)) delete env[key]
let host: ReturnType<typeof Bun.spawn> | undefined
let stdout = ""
let stderr = ""
let observer: ReturnType<typeof watch> | undefined
try {
  execFileSync("git", ["init", "--quiet", "--initial-branch=main", directory], { env })
  host = Bun.spawn([binary, "serve", "--hostname", loopback, "--port", "0"], { cwd: directory, env, stdin: "ignore", stdout: "pipe", stderr: "pipe" })
  const server = host
  const ready = new Promise<string>((resolve, reject) => {
    void (async () => {
      for await (const bytes of server.stdout as ReadableStream<Uint8Array>) {
        stdout += new TextDecoder().decode(bytes)
        const match = stdout.match(/listening on (http:\/\/[^\s]+)/)
        if (match) resolve(match[1])
      }
      reject(new Error("native host exited before listening"))
    })().catch(reject)
  })
  const readErrors = (async () => { for await (const bytes of server.stderr as ReadableStream<Uint8Array>) stderr += new TextDecoder().decode(bytes) })()
  const signal = AbortSignal.timeout(60_000)
  const expired = new Promise<never>((_, reject) => signal.addEventListener("abort", () => reject(new Error("native capability witness exceeded 60 seconds")), { once: true }))
  const records = () => readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map(line => JSON.parse(line))
  await Promise.race([(async () => {
    const url = await ready
    const call = async (route: string, body?: object) => {
      const response = await fetch(url + route, { method: body === undefined ? "GET" : "POST", headers: { "content-type": "application/json", "x-opencode-directory": directory }, body: body === undefined ? undefined : JSON.stringify(body), signal })
      assert(response.ok, `${route}: ${response.status} ${await response.clone().text()}`)
      return response.status === 204 ? undefined : response.json()
    }
    assert.equal(((await call("/global/health")) as { version: string }).version, version)
    const completed = new Promise<void>(resolve => {
      observer = watch(log, () => { if (records().filter(record => record.kind === "after" && record.tool === "task").length === 3) resolve() })
    })
    const parent = await call("/session", { title: "Native capability fixture" }) as { id: string }
    await call(`/session/${parent.id}/prompt_async`, { agent: "native-parent", model: { providerID: "test", modelID: "test-model" }, parts: [{ type: "text", text: "Run the native capability witnesses." }] })
    await completed
    const observed = records()
    const executions = observed.filter(record => record.kind === "after" && record.tool === "execute")
    assert.equal(executions.length, 3)
    for (const [index, execution] of executions.entries()) {
      const session = await call(`/session/${execution.sessionID}`) as { id: string; parentID: string }
      assert.equal(session.parentID, parent.id)
      const nested = observed.filter(record => record.kind === "before" && record.sessionID === session.id && record.callID.startsWith(execution.callID + "/"))
      const result = JSON.parse(execution.output.output)
      assert.deepEqual(result.plugin_collision, { refused: true })
      const expectedTools = ["lgrep_search_text", "lgrep_search_text", "lgrep_invalidate_cache", "linear_update_issue", "vision_vision_remove", "exa_web_search_exa", "context7_query-docs", "concord_worker_read"]
      if (index === 2) expectedTools.push("apply_patch")
      assert.deepEqual(nested.map(record => record.tool), expectedTools)
      assert.equal(result.repository, "native transport reached")
      const refusals = observed.filter(record => record.kind === "refused" && record.sessionID === session.id)
      assert.match(refusals.find(record => record.callID.endsWith("/2")).message, /dispatched repository/)
      for (const key of ["foreign", "cache_mutation", "linear", "vision"]) assert.deepEqual(result[key], { refused: true })
      for (const suffix of ["/3", "/4", "/5"]) assert.match(refusals.find(record => record.callID.endsWith(suffix)).message, /does not grant/)
      assert.deepEqual(result.native_collision, { refused: true })
      for (const suffix of index === 2 ? ["/8", "/9"] : ["/8"]) assert.match(refusals.find(record => record.callID.endsWith(suffix)).message, /does not grant MCP tool/)
    }
    assert.deepEqual(transports.map(call => [call.server, call.tool]), [["lgrep", "search_text"], ["lgrep", "search_text"], ["exa", "web_search_exa"], ["context7", "query-docs"], ["lgrep", "search_text"]])
    assert.deepEqual(JSON.parse(executions[0].output.output).exa, { refused: true })
    assert.deepEqual(JSON.parse(executions[0].output.output).context7, { refused: true })
    assert.equal(JSON.parse(executions[1].output.output).exa, "native transport reached")
    console.log(JSON.stringify({ outcome: "pass", host_version: version, native_children: executions.map(record => record.sessionID), nested_hooks: 25, allowed_transports: transports.length, refused_before_transport: 22 }))
  })(), expired])
  server.kill()
  await server.exited
  await readErrors
} catch (error) {
  console.error(JSON.stringify({ outcome: "fail", error: String(error), host_stdout: stdout, host_stderr: stderr, hooks: readFileSync(log, "utf8"), transports }))
  throw error
} finally {
  observer?.close()
  host?.kill()
  await host?.exited
  provider.stop(true)
  mcp.stop(true)
  rmSync(scratch, { recursive: true, force: true })
}
