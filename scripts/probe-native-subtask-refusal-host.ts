import fs from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"

// Exercise the installed host's pending-subtask collection without a real model,
// worker, authority store, credentials, or changes to an operator's histories.
const root = await fs.mkdtemp(path.join(os.tmpdir(), "concord-subtask-refusal-"))
const marker = path.join(root, "cleanup-enabled")
const pluginSource = fileURLToPath(new URL("../adapter/opencode/concord-plugin.ts", import.meta.url))
const hostSource = fileURLToPath(new URL("../adapter/opencode/move-session.ts", import.meta.url))
const loopback = Object.values(os.networkInterfaces()).flat().find(address => address?.internal && address.family === "IPv4")?.address
if (!loopback) throw new Error("the native fixture requires an IPv4 loopback interface")
let modelCalls = 0
const provider = Bun.serve({ port: 0, hostname: loopback, fetch: async request => {
  const body = await request.json() as { messages?: Array<{ role: string }> }
  if (!body.messages?.length) return new Response("missing messages", { status: 400 })
  modelCalls++
  const base = { id: "synthetic-completion", object: "chat.completion.chunk", created: 1, model: "fixture-model" }
  return new Response([
    { ...base, choices: [{ index: 0, delta: { role: "assistant", content: "Synthetic prompt remains usable." }, finish_reason: null }] },
    { ...base, choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } },
  ].map(chunk => `data: ${JSON.stringify(chunk)}\n\n`).join("") + "data: [DONE]\n\n", { headers: { "Content-Type": "text/event-stream" } })
} })
let child: ReturnType<typeof Bun.spawn> | undefined
let stdout = ""
let stderr: Promise<string> | undefined
let pump: Promise<void> | undefined
try {
  await fs.mkdir(path.join(root, ".opencode/plugins"), { recursive: true })
  await fs.writeFile(path.join(root, "opencode.json"), JSON.stringify({
    $schema: "https://opencode.ai/config.json",
    model: "fixture/fixture-model",
    small_model: "fixture/fixture-model",
    provider: { fixture: { npm: "@ai-sdk/openai-compatible", options: { baseURL: `${provider.url}v1`, apiKey: "synthetic" }, models: { "fixture-model": { name: "Synthetic", limit: { context: 100000, output: 100 } } } } },
    snapshot: false,
    agent: { build: {}, title: { disable: true }, "concord-research": { mode: "subagent", description: "Synthetic refused lane" } },
  }))
  await fs.writeFile(path.join(root, ".opencode/plugins/refusal-probe.ts"), `
import ConcordAdapterPlugin from ${JSON.stringify(pluginSource)}
import { hostControlPlane } from ${JSON.stringify(hostSource)}
export default async input => {
  const plugin = await ConcordAdapterPlugin(input)
  const before = plugin["tool.execute.before"]
  plugin["tool.execute.before"] = async (call, output) => {
    // Disable only cleanup to construct the exact unfinished legacy history.
    // The real guard still refuses every attempted lane dispatch.
    if (!await Bun.file(${JSON.stringify(marker)}).exists()) hostControlPlane().bind(undefined)
    try { await before(call, output) } finally { hostControlPlane().bind(input) }
  }
  return plugin
}
`)
  child = Bun.spawn([process.env.OPENCODE_BIN ?? "opencode", "serve", "--hostname", loopback, "--port", "0"], {
    cwd: root, stdout: "pipe", stderr: "pipe",
    env: { PATH: process.env.PATH ?? "", HOME: root, XDG_CONFIG_HOME: path.join(root, "config"),
      XDG_DATA_HOME: path.join(root, "data"), XDG_CACHE_HOME: path.join(root, "cache"),
      OPENCODE_DISABLE_DEFAULT_PLUGINS: "1", OPENCODE_DISABLE_EXTERNAL_SKILLS: "1",
      OPENCODE_DISABLE_MODELS_FETCH: "1", OPENCODE_DISABLE_AUTOUPDATE: "1" },
  })
  stderr = new Response(child.stderr).text()
  let started!: (url: string) => void
  const ready = new Promise<string>(resolve => { started = resolve })
  pump = (async () => {
    for await (const chunk of child!.stdout as ReadableStream<Uint8Array>) {
      stdout += new TextDecoder().decode(chunk)
      const url = /server listening on (http:\/\/\S+)/.exec(stdout)?.[1]
      if (url) started(url)
    }
  })()
  const timer = setTimeout(() => child?.kill(), 120_000)
  const base = await Promise.race([ready, child.exited.then(code => { throw new Error(`host exited before readiness: ${code}`) })])
  clearTimeout(timer)
  const request = async (route: string, body?: unknown) => {
    const response = await fetch(`${base}${route}`, { method: body === undefined ? "GET" : "POST",
      headers: { "Content-Type": "application/json" }, ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(30_000) })
    const text = await response.text()
    let data: any
    try { data = JSON.parse(text) } catch { data = text }
    return { response, data }
  }
  const require = (condition: unknown, message: string) => { if (!condition) throw new Error(message) }
  for (const legacy of [false, true]) {
    const session = await request("/session", { title: "Synthetic refusal probe" })
    require(session.response.ok, `session creation failed: ${JSON.stringify(session.data)}`)
    const id = session.data.id
    const route = `/session/${id}/message`
    const queued = await request(route, { agent: "build", model: { providerID: "fixture", modelID: "fixture-model" }, noReply: true,
      parts: [{ type: "subtask", agent: "concord-research", description: "Synthetic refused packet", prompt: "Synthetic packet with no open dispatch window" }] })
    require(queued.response.ok, `queue failed: ${JSON.stringify(queued.data)}`)
    if (legacy) {
      await fs.rm(marker, { force: true })
      for (let replay = 0; replay < 2; replay++) {
        const refusal = await request(route, { parts: [{ type: "text", text: "Synthetic legacy retry" }] })
        require(!refusal.response.ok, "the disabled-cleanup guard must refuse the replay")
      }
      const history = await request(route)
      require(history.data.flatMap((m: any) => m.parts).filter((p: any) => p.type === "tool" && p.state.status === "running").length === 2, "legacy history did not retain both running Task parts")
    }
    await fs.writeFile(marker, "enabled")
    const refusal = await request(route, { parts: [{ type: "text", text: "Synthetic refused turn" }] })
    require(!refusal.response.ok, `the guard did not refuse: ${JSON.stringify(refusal.data)}`)
    const repaired = await request(route)
    const parts = repaired.data.flatMap((m: any) => m.parts)
    require(!parts.some((p: any) => p.type === "subtask"), "a refused subtask remains executable")
    const tasks = parts.filter((p: any) => p.type === "tool" && p.tool === "task")
    require(tasks.length === (legacy ? 3 : 1) && tasks.every((p: any) => p.state.status === "error" && p.state.error.includes("no open dispatch window")), "a refused Task still appears running or lost its refusal")
    require(parts.some((p: any) => p.type === "text" && p.text.includes("Concord refused native subtask")), "the refusal has no visible history text")
    const followup = await request(route, { parts: [{ type: "text", text: "Synthetic follow-up must remain usable" }] })
    require(followup.response.ok, `follow-up failed: ${JSON.stringify(followup.data)}`)
    require(followup.data.parts.some((p: any) => p.type === "text" && p.text.includes("Synthetic prompt remains usable.")), `follow-up did not complete: ${JSON.stringify(followup.data)}`)
    console.log(`PASS ${legacy ? "already-stuck history" : "fresh refusal"}: task failed, source retired, follow-up completed`)
  }
  const sessions = await request("/session")
  require(sessions.data.length === 2 && sessions.data.every((s: any) => !s.parentID), "an unauthorized worker session was created")
  require(modelCalls === 2, `expected only the two synthetic follow-up model calls, got ${modelCalls}`)
} finally {
  child?.kill()
  if (child) await child.exited
  await pump
  process.stdout.write(stdout)
  if (stderr) process.stderr.write(await stderr)
  provider.stop(true)
  await fs.rm(root, { recursive: true, force: true })
}
