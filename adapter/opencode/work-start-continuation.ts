import { randomUUID } from "node:crypto"
import { hostControlPlane } from "./move-session"
import type { SessionMessage } from "./host-prompt"

const MARKER = "concord.work_start_continuation"
type Entry = {
  sessionID: string
  messageID: string
  directory: string
  workID: string
  request: string
  token: string
  phase: "pending" | "posting" | "delivered" | "failed" | "superseded"
  delivery?: Promise<void>
}

// A spent entry survives a stale replay until landing, vacate or session
// deletion retires it; a refused replay must not start a wake loop.
const entries = new Map<string, Entry>()

export function queueWorkStartContinuation(request: object, context: { sessionID: string; messageID: string; abort: AbortSignal }, directory: string, workID: string): boolean {
  if (!context.sessionID || !context.messageID || !workID || context.abort.aborted) return false
  const held = entries.get(context.sessionID)
  const serialized = JSON.stringify(request)
  if (held) return held.directory === directory && held.workID === workID && held.request === serialized && held.phase === "pending"
  entries.set(context.sessionID, { sessionID: context.sessionID, messageID: context.messageID, directory, workID, request: serialized, token: randomUUID(), phase: "pending" })
  return true
}

export function clearWorkStartContinuation(sessionID: string): void {
  entries.delete(sessionID)
}

export function observeWorkStartMessage(sessionID: string, parts: unknown): void {
  const entry = entries.get(sessionID)
  if (!entry) return
  const own = Array.isArray(parts) && parts.length > 0 && parts.every(part =>
    part?.type === "text" && part.synthetic === true && part.metadata?.[MARKER] === entry.token)
  if (!own) entry.phase = "superseded"
}

function current(entry: Entry): boolean {
  return entries.get(entry.sessionID) === entry && entry.phase === "posting"
}

async function eligible(entry: Entry): Promise<boolean> {
  // The host also aborts its stream signal on a normal completion. After the
  // tool returns, persisted errors and session.error distinguish cancellation.
  if (!current(entry)) return false
  const reader = hostControlPlane().sessionReader()
  if (!reader) return false
  const session = await reader.get(entry.sessionID, AbortSignal.timeout(5_000))
  const identity = session.data as { id?: unknown; directory?: unknown } | undefined
  if (!session.response.ok || identity?.id !== entry.sessionID || identity.directory !== entry.directory) return false
  const result = await reader.messages(entry.sessionID, 20, undefined, AbortSignal.timeout(5_000))
  if (!result.response.ok || !Array.isArray(result.data)) return false
  const messages = result.data as SessionMessage[]
  const source = messages.find(message => message?.info?.id === entry.messageID)?.info
  if (!source || source.role !== "assistant" || typeof source.parentID !== "string" || typeof source.time?.created !== "number" || source.error != null) return false
  const created = source.time.created
  if (messages.some(message => {
    const info = message?.info
    if (!info || typeof info.time?.created !== "number" || info.time.created < created) return false
    return (info.role === "user" && info.id !== source.parentID) || (info.role === "assistant" && info.error != null)
  })) return false
  return current(entry)
}

async function deliver(entry: Entry): Promise<void> {
  if (!await eligible(entry)) throw new Error("the unlanded work-start continuation was superseded or its originating run cannot be verified")
  const text = `[concord work-start continuation ${entry.token}] The previous work_start for work ${JSON.stringify(entry.workID)} moved this session's metadata but its tool context had not landed. Verify that the new turn's actual tool context resolves in ${JSON.stringify(entry.directory)}. Only then replay this exact concord_work_start request: ${entry.request}. This message is not placement proof or workflow approval. If the context still has not landed, report the refusal and stop; do not schedule another continuation.`
  const outcome = await hostControlPlane().promptSession(entry.sessionID, text, { [MARKER]: entry.token }, () => eligible(entry))
  if (outcome !== "delivered") throw new Error("the work-start continuation persisted but no parented assistant activity confirmed delivery; no resend will run")
  if (current(entry)) entry.phase = "delivered"
}

export function continueWorkStartOnEvent(event: unknown, log: (message: string) => void): void {
  if (!event || typeof event !== "object") return
  const { type, properties } = event as { type?: unknown; properties?: { sessionID?: unknown; info?: { id?: unknown } } }
  const sessionID = properties?.sessionID ?? properties?.info?.id
  if (typeof sessionID !== "string") return
  const entry = entries.get(sessionID)
  if (!entry) return
  if (type === "session.deleted") {
    entries.delete(sessionID)
    return
  }
  if (type === "session.error") {
    entry.phase = "superseded"
    return
  }
  if (type !== "session.idle" || entry.phase !== "pending") return
  entry.phase = "posting"
  entry.delivery = deliver(entry).catch(error => {
    if (entries.get(sessionID) === entry && entry.phase === "posting") entry.phase = "failed"
    log(`work-start continuation for ${sessionID}: ${error instanceof Error ? error.message : String(error)}. The placement gate stays closed; verify the actual context before replaying the original work_start request.`)
  })
}

export function workStartContinuationSettled(sessionID: string): Promise<void> {
  return entries.get(sessionID)?.delivery ?? Promise.resolve()
}

// Test teardown mirrors the host process losing its ephemeral delivery data.
export function resetWorkStartContinuations(): void {
  entries.clear()
}
