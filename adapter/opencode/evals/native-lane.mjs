import { spawn } from "node:child_process"
import { randomUUID } from "node:crypto"
import { readFileSync } from "node:fs"
import { get } from "node:http"
import { networkInterfaces } from "node:os"
import { Readable } from "node:stream"

const lanes = JSON.parse(readFileSync(new URL("../../../contracts/agent-lanes.v1.json", import.meta.url), "utf8")).lanes
const modelSchema = JSON.parse(readFileSync(new URL("../../../contracts/agent-lane-report.schema.json", import.meta.url), "utf8")).properties.readback_model
const eventTypes = { text: "text", tool: "tool_use", "step-start": "step_start", "step-finish": "step_finish", reasoning: "reasoning" }
// Promptfoo records only the outer message, so name the cause chain by type,
// code, and HTTP status; host-supplied message text stays in error.cause.
const causeChain = error => {
  const links = []
  for (let current = error; current instanceof Error && links.length < 4; current = current.cause) links.push([current.name, current.code, current.status].filter(value => value !== undefined).join(" "))
  return links.join(" <- ")
}

/** @returns {{output: string, metadata: {readback_model: string, readback_agent: string, session_id: string}}} */
export function childRunStream(messages, agent, child) {
  const answers = messages.filter(message => message.info.role === "assistant")
  if (typeof child !== "string" || !child || !answers.length || answers.some(message => message.info.agent !== agent || message.info.sessionID !== child)) throw new Error("native eval executor identity mismatch")
  if (answers.some(message => message.info.error) || answers.at(-1).info.finish !== "stop") throw new Error("native eval child did not complete")
  const models = new Set(answers.map(({ info }) => {
    if (typeof info.providerID !== "string" || info.providerID.includes("/") || typeof info.modelID !== "string") throw new Error("native eval child model readback is missing or invalid")
    const model = `${info.providerID}/${info.modelID}`
    if (model.length < modelSchema.minLength || model.length > modelSchema.maxLength || !new RegExp(modelSchema.pattern).test(model)) throw new Error("native eval child model readback is missing or invalid")
    return model
  }))
  if (models.size !== 1) throw new Error("native eval child model readback is ambiguous")
  return {
    output: answers.flatMap(message => message.parts.filter(part => eventTypes[part.type]).map(part => JSON.stringify({ type: eventTypes[part.type], sessionID: message.info.sessionID, part }))).join("\n"),
    metadata: { readback_model: [...models][0], readback_agent: agent, session_id: child },
  }
}

export default class NativeLaneProvider {
  constructor(options) {
    if (!lanes.some(lane => lane.id === options.config?.lane)) throw new Error("native eval requires a registered lane")
    this.agent = `concord-${options.config.lane}`
    // Promptfoo's scheduler otherwise reruns the whole host and model run on
    // errors whose text looks transient; each eval is one observed execution.
    this.config = { ...options.config, maxRetries: 0 }
  }

  id() { return `native-${this.agent}` }

  taskMessage(prompt) {
    return { type: "text", text: `Invoke the native Task tool exactly once with these arguments, then return its result:\n${JSON.stringify({ subagent_type: this.agent, description: `Advisory ${this.agent} eval`, prompt })}` }
  }

  async callApi(prompt) {
    const loopback = Object.values(networkInterfaces()).flat().find(address => address?.internal && address.family === "IPv4")?.address
    if (!loopback) throw new Error("native eval requires an IPv4 loopback interface")
    const parentAgent = `concord-eval-${randomUUID()}`
    const inherited = JSON.parse(process.env.OPENCODE_CONFIG_CONTENT || "{}")
    const config = {
      ...inherited, share: "disabled", snapshot: false,
      agent: { ...inherited.agent, title: { disable: true }, [parentAgent]: {
        mode: "primary", hidden: true,
        permission: { "*": "deny", task: { "*": "deny", [this.agent]: "allow" } },
      } },
    }
    // Advisory evals do not load the Concord plugin or acquire workflow authority.
    const server = spawn(process.env.OPENCODE_BIN || "opencode", ["serve", "--hostname", loopback, "--port", "0"], {
      env: { ...process.env, OPENCODE_CONFIG_CONTENT: JSON.stringify(config), OPENCODE_PURE: "1", OPENCODE_SERVER_PASSWORD: "", OPENCODE_DISABLE_DEFAULT_PLUGINS: "1" },
      stdio: ["ignore", "pipe", "pipe"],
    })
    const closed = new Promise(resolve => server.once("close", resolve))
    let stdout = ""
    let stderr = ""
    server.stderr.on("data", bytes => { stderr += bytes })
    const signal = AbortSignal.timeout(300_000)
    const ready = new Promise((resolve, reject) => {
      server.stdout.on("data", bytes => {
        stdout += bytes
        const match = stdout.match(/listening on (http:\/\/[^\s]+)\r?\n/)
        if (match) resolve(match[1])
      })
      server.once("error", reject)
      server.once("close", () => reject(new Error(`native eval host exited: ${stderr}`)))
      signal.addEventListener("abort", () => reject(signal.reason), { once: true })
    })
    let reader
    let eventRequest
    try {
      const url = await ready
      const failed = (method, route, cause) => new Error(`native eval ${method} ${route} failed: ${causeChain(cause)}`, { cause })
      const call = async (route, body) => {
        const method = body === undefined ? "GET" : "POST"
        try {
          const response = await fetch(url + route, { method, headers: { "content-type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body), signal })
          if (!response.ok) throw Object.assign(new Error(`${route}: ${response.status} ${await response.text()}`), { status: response.status })
          return response.status === 204 ? undefined : await response.json()
        } catch (cause) {
          throw failed(method, route, cause)
        }
      }
      const configured = await call("/agent")
      if (!configured.some(agent => agent.name === this.agent && agent.mode === "subagent" && agent.hidden === true)) throw new Error("native eval requires a hidden subagent definition")
      const parent = await call("/session", { title: `Advisory ${this.agent} eval` })
      // The event stream owns an unpooled connection. A pooled one outlives
      // this host and is handed to the next host bound to the same origin.
      const events = await new Promise((resolve, reject) => {
        eventRequest = get(url + "/event", { agent: false, signal }, resolve).on("error", reject)
      }).catch(cause => { throw failed("GET", "/event", cause) })
      reader = Readable.toWeb(events).getReader()
      if (events.statusCode !== 200) throw failed("GET", "/event", Object.assign(new Error(`/event: ${events.statusCode}`), { status: events.statusCode }))
      await call(`/session/${parent.id}/prompt_async`, {
        agent: parentAgent,
        parts: [this.taskMessage(prompt)],
      })
      const decoder = new TextDecoder()
      let buffer = ""
      let child
      while (true) {
        let next
        try {
          next = await reader.read()
        } catch (error) {
          // node:http reports its own abort to the response as ECONNRESET.
          const cause = signal.aborted ? signal.reason : error
          throw new Error(`native eval GET /event stream read failed: ${causeChain(cause)}`, { cause })
        }
        if (next.done) throw new Error("native eval GET /event stream ended before the child completed")
        buffer += decoder.decode(next.value, { stream: true })
        let boundary
        while ((boundary = /\r?\n\r?\n/.exec(buffer))) {
          const frame = buffer.slice(0, boundary.index)
          buffer = buffer.slice(boundary.index + boundary[0].length)
          const data = frame.split(/\r?\n/).filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n")
          if (!data) continue
          const event = JSON.parse(data)
          if (event.type === "session.created" && event.properties.info.parentID === parent.id) {
            if (child || event.properties.info.agent !== this.agent) throw new Error("native eval child identity mismatch")
            child = event.properties.info.id
          }
          if (event.type === "session.error" && [parent.id, child].includes(event.properties.sessionID)) throw new Error(JSON.stringify(event.properties.error))
          if (child && event.type === "session.idle" && event.properties.sessionID === child) {
            return childRunStream(await call(`/session/${child}/message`), this.agent, child)
          }
        }
      }
    } finally {
      await reader?.cancel().catch(() => {})
      eventRequest?.destroy()
      server.kill()
      await closed
    }
  }
}
