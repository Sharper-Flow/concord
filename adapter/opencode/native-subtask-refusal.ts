import { isDeepStrictEqual } from "node:util"
import { SESSION_MESSAGES_ROUTE, type RouteClient } from "./move-session"

type RecordValue = Record<string, any>
type Message = { info: RecordValue; parts: RecordValue[] }
const record = (value: unknown): value is RecordValue => value !== null && typeof value === "object" && !Array.isArray(value)
const after = (a: RecordValue, b: RecordValue) => a.time.created > b.time.created || (a.time.created === b.time.created && a.id > b.id)

function sameTask(input: RecordValue, args: RecordValue): boolean {
  return input.prompt === args.prompt && input.description === args.description && input.subagent_type === args.subagent_type && input.command === args.command
}

function nativeTask(message: Message, part: RecordValue, args: RecordValue): boolean {
  return message.info.role === "assistant" && message.info.agent === args.subagent_type && message.info.mode === args.subagent_type &&
    !message.info.finish && message.parts.length === 1 && part.type === "tool" && part.tool === "task" &&
    record(part.state) && part.state.status === "running" && record(part.state.input) && sameTask(part.state.input, args) &&
    record(part.state.time) && Number.isFinite(part.state.time.start)
}

// The native before-hook's callID is the stored part ID, unlike a model Task's
// tool-call ID. The host leaves that part running when a before-hook throws.
// Retire only its exact pending subtask copies and unfinished native replays;
// the guard still throws and no new execution authority is created.
export async function cleanupRefusedSubtask(client: RouteClient | null, input: { sessionID: string; callID: string }, args: RecordValue,
  error: string, activeCallID?: string): Promise<void> {
  if (!client || typeof args.prompt !== "string" || typeof args.description !== "string" || typeof args.subagent_type !== "string") return
  const signal = AbortSignal.timeout(10_000)
  const messages: Message[] = []
  const seen = new Set<string>()
  let before: string | undefined
  let finished: RecordValue | undefined
  // Bound host reads even for an imported history with no finished assistant.
  for (let page = 0; ; page++) {
    if (page === 100) throw new Error("native subtask cleanup exceeded 100 message pages")
    const result = await client.get({ url: SESSION_MESSAGES_ROUTE, path: { id: input.sessionID }, query: { limit: "100", ...(before ? { before } : {}) }, signal })
    if (!result.response.ok || !Array.isArray(result.data)) throw new Error(`native subtask cleanup could not read messages (status ${result.response.status})`)
    for (const value of result.data) {
      if (!record(value) || !record(value.info) || value.info.sessionID !== input.sessionID || typeof value.info.id !== "string" ||
        !record(value.info.time) || !Number.isFinite(value.info.time.created) || !Array.isArray(value.parts)) {
        throw new Error("native subtask cleanup received malformed session messages")
      }
      const message = value as Message
      if (!message.parts.every(part => record(part) && typeof part.id === "string" && part.sessionID === input.sessionID && part.messageID === message.info.id)) {
        throw new Error("native subtask cleanup received foreign or malformed parts")
      }
      messages.push(message)
      if (message.info.role === "assistant" && message.info.finish && (!finished || after(message.info, finished))) finished = message.info
    }
    if (finished) break
    const next = result.response.headers.get("X-Next-Cursor")
    if (!next) break
    if (seen.has(next)) throw new Error("native subtask cleanup received a repeated message cursor")
    seen.add(next)
    before = next
  }
  const pending = messages.filter(message => !finished || after(message.info, finished))
  const current = pending.find(message => message.parts.some(part => part.id === input.callID && nativeTask(message, part, args)))
  if (!current || input.callID === activeCallID) return
  const sources = pending.flatMap(message => message.info.role !== "user" ? [] : message.parts.filter(part =>
    part.type === "subtask" && part.agent === args.subagent_type && part.prompt === args.prompt &&
    part.description === args.description && part.command === args.command))
  if (!client.patch) throw new Error("native subtask cleanup requires the host part-update route")
  const patch = async (part: RecordValue) => {
    const body = JSON.parse(JSON.stringify(part))
    const result = await client.patch!({ url: "/session/{id}/message/{messageID}/part/{partID}",
      path: { id: input.sessionID, messageID: part.messageID, partID: part.id }, body, signal })
    if (!result.response.ok || !isDeepStrictEqual(result.data, body)) {
      throw new Error(`native subtask cleanup could not confirm part ${part.id} (status ${result.response.status})`)
    }
  }
  // Persist failure before removing executable inputs. A failed write leaves
  // the source available for the next refusal to retry the repair.
  for (const message of pending) {
    for (const part of message.parts) {
      if (part.id === activeCallID || !nativeTask(message, part, args)) continue
      await patch({ ...part, state: { status: "error", input: part.state.input, error,
        metadata: part.state.metadata, time: { start: part.state.time.start, end: Date.now() } } })
    }
  }
  for (const source of sources) {
    await patch({ id: source.id, sessionID: source.sessionID, messageID: source.messageID, type: "text", synthetic: true,
      text: `Concord refused native subtask ${args.subagent_type}: ${error}`,
      metadata: { concord_refused_subtask: source } })
  }
}
