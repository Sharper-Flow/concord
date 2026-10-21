import { spawn } from "node:child_process"
import { randomUUID } from "node:crypto"
import { readFileSync } from "node:fs"
import { networkInterfaces } from "node:os"

const lanes = JSON.parse(readFileSync(new URL("../../../contracts/agent-lanes.v1.json", import.meta.url), "utf8")).lanes
const eventTypes = { text: "text", tool: "tool_use", "step-start": "step_start", "step-finish": "step_finish", reasoning: "reasoning" }

export function childRunStream(messages, agent) {
  const answers = messages.filter(message => message.info.role === "assistant")
  if (!answers.length || answers.some(message => message.info.agent !== agent)) throw new Error("native eval executor identity mismatch")
  if (answers.at(-1).info.error || answers.at(-1).info.finish !== "stop") throw new Error("native eval child did not complete")
  return answers.flatMap(message => message.parts.filter(part => eventTypes[part.type]).map(part => JSON.stringify({ type: eventTypes[part.type], sessionID: message.info.sessionID, part }))).join("\n")
}

export default class NativeLaneProvider {
  constructor(options) {
    if (!lanes.some(lane => lane.id === options.config?.lane)) throw new Error("native eval requires a registered lane")
    this.agent = `concord-${options.config.lane}`
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
        const match = stdout.match(/listening on (http:\/\/[^\s]+)/)
        if (match) resolve(match[1])
      })
      server.once("error", reject)
      server.once("close", () => reject(new Error(`native eval host exited: ${stderr}`)))
      signal.addEventListener("abort", () => reject(signal.reason), { once: true })
    })
    let reader
    try {
      const url = await ready
      const call = async (route, body) => {
        const response = await fetch(url + route, { method: body === undefined ? "GET" : "POST", headers: { "content-type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body), signal })
        if (!response.ok) throw new Error(`${route}: ${response.status} ${await response.text()}`)
        return response.status === 204 ? undefined : response.json()
      }
      const configured = await call("/agent")
      if (!configured.some(agent => agent.name === this.agent && agent.mode === "subagent" && agent.hidden === true)) throw new Error("native eval requires a hidden subagent definition")
      const parent = await call("/session", { title: `Advisory ${this.agent} eval` })
      const events = await fetch(url + "/event", { signal })
      if (!events.ok || !events.body) throw new Error("native eval event stream is unavailable")
      reader = events.body.getReader()
      await call(`/session/${parent.id}/prompt_async`, {
        agent: parentAgent,
        parts: [this.taskMessage(prompt)],
      })
      const decoder = new TextDecoder()
      let buffer = ""
      let child
      while (true) {
        const next = await reader.read()
        if (next.done) throw new Error("native eval event stream ended before the child completed")
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
            return { output: childRunStream(await call(`/session/${child}/message`), this.agent) }
          }
        }
      }
    } finally {
      await reader?.cancel().catch(() => {})
      server.kill()
      await closed
    }
  }
}
