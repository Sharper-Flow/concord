// The host control plane uses the client the plugin factory supplies.
//
// The adapter reaches host routes through the client the plugin factory receives, never
// through a URL it rebuilds. That client dispatches in process and carries the
// host's own headers, so it reaches the route on a host that binds no socket
// and on one that requires credentials. The plugin's `serverUrl` is a fallback
// placeholder when the host runs no listener, which is why nothing here reads
// it.
//
// `GET/PATCH /session/{id}` owns persistent Task participation metadata and
// the session title.

import { execFileSync } from "node:child_process"
import { promptAndConfirm } from "./host-prompt"

export const SESSION_ROUTE = "/session/{id}"
export const SESSION_MESSAGES_ROUTE = "/session/{id}/message"
export const MANAGED_TASK_SCOPE_KEY = "concord.task_scope"

// hostVersion reports the running OpenCode version so an unavailable-route
// refusal names the host it was refused by. The probe runs only on that path,
// and a host that cannot report a version is named as unknown rather than
// blocking the refusal it is being reported inside.
function hostVersion(): string {
  try {
    return execFileSync(process.env.OPENCODE_BIN ?? "opencode", ["--version"], {
      encoding: "utf8",
      timeout: 5_000,
      stdio: ["ignore", "pipe", "ignore"],
    })
      .trim()
      .split("\n")[0] || "unknown"
  } catch {
    return "unknown"
  }
}

export class MoveSessionUnavailable extends Error {}
export class SessionScopeUnavailable extends Error {}
class HostSessionMissing extends Error {}

// HostControlPlane holds the route client the plugin factory received. The
// tool path and the plugin entry reach the same instance through this module,
// which the OpenCode instance scopes to one running host.
export const hostControlPlane = (): HostControlPlane => shared

export type RouteResult = { data?: unknown; response: Response }

// RouteClient is the request surface the SDK client exposes beneath its
// generated methods. It takes the route as data, so it reaches routes the
// generated methods omit while keeping the host's transport and headers.
export type RouteClient = {
  get: (options: { url: string; path?: Record<string, unknown>; query?: Record<string, unknown>; signal?: AbortSignal }) => Promise<RouteResult>
  post: (options: { url: string; path?: Record<string, unknown>; body?: unknown; signal?: AbortSignal }) => Promise<RouteResult>
  patch?: (options: { url: string; path?: Record<string, unknown>; body?: unknown; signal?: AbortSignal }) => Promise<RouteResult>
}

// SessionReader is the host API used to confirm continuation delivery:
// one session record and one bounded page of its messages per call. The
// result is the same route pair the generic client returns, so a test can
// supply its own reader without a server.
export interface SessionReader {
  get(sessionID: string, signal?: AbortSignal): Promise<RouteResult>
  messages(sessionID: string, limit: number, before: string | undefined, signal?: AbortSignal): Promise<RouteResult>
}

// PluginClientHost is the part of the host plugin input this module consumes.
// The SDK client keeps its configured request surface on a member the
// generated class does not publish, so the reach is declared here once rather
// than cast at each call site.
export type PluginClientHost = { client?: unknown }

function routeClientOf(client: unknown): RouteClient | null {
  const raw = (client as { _client?: Partial<RouteClient> } | null | undefined)?._client
  if (!raw || typeof raw.get !== "function" || typeof raw.post !== "function") return null
  return raw as RouteClient
}

function isRouteClient(source: PluginClientHost | RouteClient): source is RouteClient {
  return typeof (source as RouteClient).post === "function" && typeof (source as RouteClient).get === "function"
}

export class HostControlPlane {
  #client: RouteClient | null = null

  // bind records the client the plugin factory was handed. A tool call that
  // runs before the factory bound one finds no route, which is the same
  // fail-closed answer as a host that serves none. A route client may be
  // supplied directly so a test can exercise the route contract without a
  // server.
  bind(source: PluginClientHost | RouteClient | undefined): void {
    if (!source) {
      this.#client = null
      return
    }
    this.#client = isRouteClient(source) ? source : routeClientOf(source.client)
  }

  available(): boolean {
    return this.#client !== null
  }

  async runningVersion(signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<string | null> {
    try {
      const result = await this.#client?.get({ url: "/global/health", signal })
      const data = result?.data
      if (!result?.response.ok || data === null || typeof data !== "object" || Array.isArray(data)) return null
      const version = (data as Record<string, unknown>).version
      return typeof version === "string" && version.length > 0 ? version : null
    } catch {
      return null
    }
  }

  async promptSession(sessionID: string, text: string, metadata: Record<string, unknown>, beforePost: () => Promise<boolean>): Promise<"delivered" | "unconfirmed"> {
    return promptAndConfirm(this.#require("the native session prompt route is unavailable"), sessionID, text,
      { confirmWindowMs: 30_000, confirmPollMs: 100 }, metadata, beforePost)
  }

  // A host that supplies no client cannot confirm continuation delivery.
  sessionReader(): SessionReader | null {
    const client = this.#client
    if (!client) return null
    return {
      async get(sessionID, signal) {
        return client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal })
      },
      async messages(sessionID, limit, before, signal) {
        const query: Record<string, unknown> = { limit: String(limit) }
        if (before !== undefined) query.before = before
        return client.get({ url: SESSION_MESSAGES_ROUTE, path: { id: sessionID }, query, signal })
      },
    }
  }

  // sessionDirectory reads where the host actually runs a session.
  async sessionDirectory(sessionID: string, signal?: AbortSignal): Promise<string> {
    return this.#session(sessionID, "cannot read the session directory back", signal)
  }

  // Participation is a host session policy, not a stored copy of a worktree or
  // workflow. Parent traversal resolves inherited participation for native children.
  // Reads are not cached: an agent switch or plugin reload cannot change scope.
  async taskScope(sessionID: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<"managed" | "unmanaged" | null> {
    const visited = new Set<string>()
    let current = sessionID
    for (;;) {
      signal.throwIfAborted()
      if (visited.has(current)) throw new SessionScopeUnavailable("cannot resolve managed Task scope: the host session ancestry contains a cycle")
      visited.add(current)
      let session: { metadata: Record<string, unknown>; parentID?: string }
      try {
        session = await this.#scopeSession(current, signal)
      } catch (error) {
        if (error instanceof HostSessionMissing && current === sessionID) return null
        throw error
      }
      if (session.metadata[MANAGED_TASK_SCOPE_KEY] === "managed") return "managed"
      if (session.parentID === undefined) return "unmanaged"
      current = session.parentID
    }
  }

  // Host ancestry distinguishes a native child from its coordinator without
  // trusting the selected agent or its prompt.
  async hasManagedParent(sessionID: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<boolean> {
    signal.throwIfAborted()
    let current = sessionID
    const visited = new Set<string>()
    visited.add(current)
    let session: { metadata: Record<string, unknown>; parentID?: string }
    try {
      session = await this.#scopeSession(current, signal)
    } catch (error) {
      if (error instanceof HostSessionMissing) throw new SessionScopeUnavailable("cannot resolve managed Task parent: the calling host session does not exist")
      throw error
    }
    for (;;) {
      signal.throwIfAborted()
      if (session.parentID === undefined) return false
      current = session.parentID
      if (visited.has(current)) throw new SessionScopeUnavailable("cannot resolve managed Task parent: the host session ancestry contains a cycle")
      visited.add(current)
      try {
        session = await this.#scopeSession(current, signal)
      } catch (error) {
        if (error instanceof HostSessionMissing) throw new SessionScopeUnavailable("cannot resolve managed Task parent: an ancestor session does not exist")
        throw error
      }
      if (session.metadata[MANAGED_TASK_SCOPE_KEY] === "managed") return true
    }
  }

  // The host PATCH replaces metadata. Preserve the other namespaces and read
  // the saved session back before admitting any Concord operation that starts
  // managed work. Repeating enrollment does not write a second policy record.
  async manageSession(sessionID: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<void> {
    signal.throwIfAborted()
    const session = await this.#scopeSession(sessionID, signal)
    if (session.metadata[MANAGED_TASK_SCOPE_KEY] === "managed") return
    const client = this.#require("managed Task scope metadata update is unavailable: this host handed the plugin no client")
    if (typeof client.patch !== "function") throw new SessionScopeUnavailable("managed Task scope metadata update is unavailable on this host client")
    let result: RouteResult
    try {
      result = await client.patch({ url: SESSION_ROUTE, path: { id: sessionID }, body: { metadata: { ...session.metadata, [MANAGED_TASK_SCOPE_KEY]: "managed" } }, signal })
    } catch {
      throw new SessionScopeUnavailable("the host managed Task scope metadata update failed; participation may have been recorded, so verify the host session before retrying")
    }
    if (!result.response.ok) throw new SessionScopeUnavailable(`the host refused the managed Task scope metadata update with status ${result.response.status}`)
    const saved = await this.#scopeSession(sessionID, signal)
    if (saved.metadata[MANAGED_TASK_SCOPE_KEY] !== "managed") throw new SessionScopeUnavailable("the host did not persist the managed Task scope")
  }

  // setSessionTitle writes the host session title through PATCH /session/{id}
  // with a { title } body. The title names the work goal, so the write is best
  // effort by construction: no bound client, a client without a patch surface,
  // a 404 or 405, or a failed call answers false rather than throwing, and an
  // empty title writes nothing. The caller warns and keeps its recorded
  // outcome either way.
  async setSessionTitle(sessionID: string, title: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<boolean> {
    if (title.length === 0) return false
    if (!this.#client || typeof this.#client.patch !== "function") return false
    try {
      const result = await this.#client.patch({ url: SESSION_ROUTE, path: { id: sessionID }, body: { title }, signal })
      if (result.response.status === 404 || result.response.status === 405) return false
      return result.response.ok
    } catch {
      return false
    }
  }

  // sessionTitle reads the host session title back through GET /session/{id}.
  // It is best effort like the write: any absent route, refusal, or record
  // without a title answers null, so a caller that reads the title for
  // context decides on what the host actually holds.
  async sessionTitle(sessionID: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<string | null> {
    if (!this.#client) return null
    try {
      const result = await this.#client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal })
      const title = (result.data as { title?: unknown } | null | undefined)?.title
      return typeof title === "string" && title.length > 0 ? title : null
    } catch {
      return null
    }
  }

  async #scopeSession(sessionID: string, signal: AbortSignal): Promise<{ metadata: Record<string, unknown>; parentID?: string }> {
    const value = await this.#sessionRecord(sessionID, "cannot read managed Task scope", signal)
    if (!isRecord(value) || value.id !== sessionID) throw new SessionScopeUnavailable("cannot resolve managed Task scope: the host returned a different session identity")
    const metadata = value.metadata === undefined ? {} : value.metadata
    if (!isRecord(metadata)) throw new SessionScopeUnavailable("cannot resolve managed Task scope: session metadata is not an object")
    if (Object.hasOwn(metadata, MANAGED_TASK_SCOPE_KEY) && metadata[MANAGED_TASK_SCOPE_KEY] !== "managed") throw new SessionScopeUnavailable("cannot resolve managed Task scope: the participation value is not declared")
    if (value.parentID !== undefined && (typeof value.parentID !== "string" || value.parentID.length === 0)) throw new SessionScopeUnavailable("cannot resolve managed Task scope: the parent session identity is invalid")
    return { metadata, ...(typeof value.parentID === "string" ? { parentID: value.parentID } : {}) }
  }

  #require(message: string): RouteClient {
    if (!this.#client) throw new MoveSessionUnavailable(`${message} (host version ${hostVersion()})`)
    return this.#client
  }

  async #session(sessionID: string, prefix: string, signal?: AbortSignal): Promise<string> {
    const session = await this.#sessionRecord(sessionID, prefix, signal)
    const directory = (session as { directory?: unknown } | null | undefined)?.directory
    if (typeof directory !== "string" || !directory) {
      throw new Error("the session readback carried no directory")
    }
    return directory
  }

  async #sessionRecord(sessionID: string, prefix: string, signal?: AbortSignal): Promise<unknown> {
    const client = this.#require(`${prefix}: this host handed the plugin no client`)
    let result: RouteResult
    try {
      result = await client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal })
    } catch (error) {
      throw new Error(`${prefix}: ${error instanceof Error ? error.message : String(error)}`)
    }
    if (!result.response.ok) {
      const message = `${prefix}: the host answered ${result.response.status}: ${await refusalText(result)}`
      if (result.response.status === 404) throw new HostSessionMissing(message)
      throw new Error(message)
    }
    return result.data
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

const NO_REFUSAL = "the host returned no readable refusal"

// refusalText reports why the host refused, in the host's own words.
//
// The route client parses the body when it can, and `result.data` is then the
// only readable copy: the response is already consumed, so reading it again
// reports that the host said nothing when it said exactly why. When the client
// carried no parsed value the body is still unread, and it holds the answer.
async function refusalText(result: RouteResult): Promise<string> {
  const carried = result.data
  if (typeof carried === "string") return messageFrom(carried)
  if (carried !== null && carried !== undefined) {
    const envelope = carried as { data?: { message?: unknown }; message?: unknown }
    const message = envelope.data?.message ?? envelope.message
    return typeof message === "string" && message ? message : NO_REFUSAL
  }
  let body: string
  try {
    body = await result.response.text()
  } catch {
    return NO_REFUSAL
  }
  return messageFrom(body)
}

function messageFrom(body: string): string {
  try {
    const parsed = JSON.parse(body) as { data?: { message?: unknown }; message?: unknown }
    const message = parsed?.data?.message ?? parsed?.message
    if (typeof message === "string" && message) return message
  } catch {
    // A non-JSON body is reported as the host wrote it.
  }
  return body.slice(0, 512) || NO_REFUSAL
}

const shared = new HostControlPlane()
