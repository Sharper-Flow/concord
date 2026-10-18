import { SESSION_MESSAGES_ROUTE, SESSION_ROUTE, type RouteResult } from "./move-session"

export type HostPromptClient = {
  get(options: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown>; signal?: AbortSignal }): Promise<RouteResult>
  post(options: { url: string; path?: Record<string, unknown>; body?: unknown; signal?: AbortSignal }): Promise<RouteResult>
}
export type SessionIdentity = { agent?: string; model?: { providerID: string; modelID: string }; variant?: string }
export type SessionMessage = {
  info?: { id?: unknown; role?: unknown; parentID?: unknown; error?: unknown; time?: { created?: number; completed?: number } }
  parts?: Array<{ type?: unknown; text?: unknown }>
}

export async function readSessionIdentity(client: HostPromptClient, sessionID: string): Promise<SessionIdentity> {
  const result = await client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host session record answered ${result.response.status}`)
  const record = (result.data ?? {}) as { agent?: unknown; model?: unknown }
  const identity: SessionIdentity = {}
  if (typeof record.agent === "string" && record.agent.length > 0) identity.agent = record.agent
  if (record.model !== null && typeof record.model === "object") {
    const model = record.model as { id?: unknown; providerID?: unknown; variant?: unknown }
    if (typeof model.providerID === "string" && model.providerID.length > 0 && typeof model.id === "string" && model.id.length > 0) {
      identity.model = { providerID: model.providerID, modelID: model.id }
      if (typeof model.variant === "string" && model.variant.length > 0 && model.variant !== "default") identity.variant = model.variant
    }
  }
  return identity
}

async function sessionMessages(client: HostPromptClient, sessionID: string): Promise<SessionMessage[]> {
  const result = await client.get({ url: SESSION_MESSAGES_ROUTE, path: { id: sessionID }, query: { limit: "20" }, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host session messages route answered ${result.response.status}`)
  return Array.isArray(result.data) ? result.data as SessionMessage[] : []
}

// Persistence and actual activity parented to the injected message are distinct
// from an HTTP acknowledgement or an assistant placeholder.
export async function promptAndConfirm(client: HostPromptClient, sessionID: string, text: string,
  config: { confirmWindowMs: number; confirmPollMs: number }, metadata?: Record<string, unknown>, beforePost?: () => Promise<boolean> | boolean,
): Promise<"delivered" | "unconfirmed"> {
  const identity = await readSessionIdentity(client, sessionID)
  if (beforePost && !await beforePost()) throw new Error("the host prompt was superseded before delivery")
  const part: Record<string, unknown> = { type: "text", text, synthetic: true }
  if (metadata !== undefined) part.metadata = metadata
  const result = await client.post({ url: "/session/{id}/prompt_async", path: { id: sessionID }, body: { ...identity, parts: [part] }, signal: AbortSignal.timeout(10_000) })
  if (!result.response.ok) throw new Error(`the host prompt_async route answered ${result.response.status}`)
  let injected: string | null = null
  let deadline = Date.now() + config.confirmWindowMs
  for (;;) {
    for (const message of await sessionMessages(client, sessionID)) {
      const info = message.info
      if (!info || info.role !== "user" || typeof info.id !== "string") continue
      if (Array.isArray(message.parts) && message.parts.some(part => part?.type === "text" && part.text === text)) injected = info.id
    }
    if (injected !== null) break
    if (Date.now() + config.confirmPollMs > deadline) throw new Error(`the host never persisted the injected report message for session ${sessionID}`)
    await new Promise(resolve => setTimeout(resolve, config.confirmPollMs))
  }
  deadline = Date.now() + config.confirmWindowMs
  for (;;) {
    for (const message of await sessionMessages(client, sessionID)) {
      const info = message.info
      if (!info || info.role !== "assistant" || info.parentID !== injected || (info.error !== undefined && info.error !== null)) continue
      if (Array.isArray(message.parts) && message.parts.some(part => part?.type === "tool" || (part?.type === "text" && typeof part.text === "string" && part.text.trim().length > 0))) return "delivered"
    }
    if (Date.now() + config.confirmPollMs > deadline) return "unconfirmed"
    await new Promise(resolve => setTimeout(resolve, config.confirmPollMs))
  }
}
