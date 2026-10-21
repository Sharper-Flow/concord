// Opt-in mode and eval-transport check: native host and Task executor, fixture model.
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { mkdtempSync, rmSync } from "node:fs"
import { networkInterfaces, tmpdir } from "node:os"
import { join } from "node:path"
import { pathToFileURL } from "node:url"
import NativeLaneProvider from "./native-lane.mjs"
import { readWorkerReportTexts, WORKER_REPORT_PROTOCOL } from "../worker-report-protocol.js"

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
const scratch = mkdtempSync(join(tmpdir(), "concord-native-task-"))
const lanes = ["research", "implement", "design", "review", "verify"]
let currentLane = ""
const provider = Bun.serve({ hostname: loopback, port: 0, async fetch(request) {
  const body = await request.json() as { tools?: Array<{ function?: { name?: string } }>; messages?: Array<{ role: string }> }
  const chunk = (delta: object, finish_reason: string | null) => `data: ${JSON.stringify({ id: "chatcmpl-task-fixture", object: "chat.completion.chunk", created: 1, model: "test-model", choices: [{ index: 0, delta, finish_reason }] })}\n\n`
  if (body.tools?.some(tool => tool.function?.name === "task") && !body.messages?.some(message => message.role === "tool")) {
    const call = { index: 0, id: "call-native-task", type: "function", function: { name: "task", arguments: JSON.stringify({ subagent_type: `concord-${currentLane}`, description: "Advisory native Task", prompt: "Return the synthetic completion marker." }) } }
    return new Response(chunk({ tool_calls: [call] }, null) + chunk({}, "tool_calls") + "data: [DONE]\n\n", { headers: { "content-type": "text/event-stream" } })
  }
  return new Response(chunk({ content: "native-subagent-complete" }, null) + chunk({}, "stop") + "data: [DONE]\n\n", { headers: { "content-type": "text/event-stream" } })
} })
const config = {
  $schema: "https://opencode.ai/config.json", ...testProviderConfig(`${provider.url}v1`),
  model: "test/test-model", small_model: "test/test-model", enabled_providers: ["test"],
  agent: Object.fromEntries(lanes.map(lane => [`concord-${lane}`, {
    mode: "subagent", hidden: true, model: "test/test-model", permission: { task: { "*": "deny" } },
  }])),
}
const env = { ...process.env }
try {
  Object.assign(process.env, {
    HOME: scratch, OPENCODE_TEST_HOME: scratch, OPENCODE_BIN: binary,
    XDG_CONFIG_HOME: join(scratch, "config"), XDG_DATA_HOME: join(scratch, "data"),
    XDG_STATE_HOME: join(scratch, "state"), XDG_CACHE_HOME: join(scratch, "cache"),
    OPENCODE_CONFIG_CONTENT: JSON.stringify(config), OPENCODE_DISABLE_PROJECT_CONFIG: "1",
    OPENCODE_DISABLE_AUTOUPDATE: "1", OPENCODE_DISABLE_AUTOCOMPACT: "1",
    OPENCODE_DISABLE_MODELS_FETCH: "1", OPENCODE_DISABLE_EXTERNAL_SKILLS: "1", OPENCODE_AUTH_CONTENT: "{}",
  })
  delete process.env.OPENCODE_CONFIG
  delete process.env.OPENCODE_CONFIG_DIR
  delete process.env.CONCORD_DB_PATH
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  const completedAgents: string[] = []
  for (const lane of lanes) {
    currentLane = lane
    const evaluated = await new NativeLaneProvider({ config: { lane } }).callApi("Return the synthetic completion marker.")
    const collected = readWorkerReportTexts(evaluated.output, { protocol: WORKER_REPORT_PROTOCOL })
    assert.equal(collected.detail, undefined)
    assert.deepEqual(collected.texts, ["native-subagent-complete"])
    completedAgents.push(`concord-${lane}`)
  }
  assert.deepEqual(completedAgents, lanes.map(lane => `concord-${lane}`))
  console.log(JSON.stringify({ outcome: "pass", host_version: version, source_pin: pin, fixture_model: true, completed_agents: completedAgents, native_eval: true }))
} finally {
  for (const key of Object.keys(process.env)) if (!(key in env)) delete process.env[key]
  Object.assign(process.env, env)
  provider.stop(true)
  rmSync(scratch, { recursive: true, force: true })
}
