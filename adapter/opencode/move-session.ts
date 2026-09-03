// The host control plane: the OpenCode routes the adapter reaches directly,
// through the server URL the plugin factory receives.
//
// The route is `POST /experimental/control-plane/move-session` on the host's
// own server, carrying `{ sessionID, destination: { directory } }` and
// answering 204 on success.
//
// The adapter reaches it through the client the plugin factory receives, never
// through a URL it rebuilds. That client dispatches in process and carries the
// host's own headers, so it reaches the route on a host that binds no socket
// and on one that requires credentials. The plugin's `serverUrl` is a fallback
// placeholder when the host runs no listener, which is why nothing here reads
// it.
//
// The route has a typed method only on the v2 SDK client, and the v1 plugin
// input supplies a v1 client. Importing the v2 SDK would make this the
// adapter's first runtime dependency on a host package, resolved from the
// operator's config directory at whatever version is installed there. The
// generic request surface underneath the client reaches the same route with no
// such dependency, so the route path stays a constant here.
//
// Issue #722 adds two more. `GET /session` reports every live session and the
// directory it runs in, which is the fact a worktree removal needs and the
// store cannot hold. `POST /tui/show-toast` puts one line in front of the
// operator without an agent relaying it.

import { execFileSync } from "node:child_process"

export const MOVE_SESSION_ROUTE = "/experimental/control-plane/move-session"
export const SESSION_ROUTE = "/session/{id}"
export const SESSION_LIST_ROUTE = "/session"
export const SHOW_TOAST_ROUTE = "/tui/show-toast"

// ObservedSessionDirectory is one live host session and the directory it runs
// in. It is the wire shape the core's occupancy gate consumes, so the field
// names match the typed operation input rather than the host's own record.
export type ObservedSessionDirectory = { session_ref: string; directory: string }

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
export class MoveSessionRefused extends Error {}

// HostControlPlane holds the route client the plugin factory received. The
// tool path and the plugin entry reach the same instance through this module,
// which the OpenCode instance scopes to one running host.
export const hostControlPlane = (): HostControlPlane => shared

export type RouteResult = { data?: unknown; response: Response }

// RouteClient is the request surface the SDK client exposes beneath its
// generated methods. It takes the route as data, so it reaches routes the
// generated methods omit while keeping the host's transport and headers.
export type RouteClient = {
  get: (options: { url: string; path?: Record<string, unknown>; signal?: AbortSignal }) => Promise<RouteResult>
  post: (options: { url: string; body?: unknown; signal?: AbortSignal }) => Promise<RouteResult>
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

  // moveSession retargets a running session at an absolute directory. It
  // never asks the host to carry local changes: CD-0098 D3 requires a clean
  // default checkout, so uncommitted work stays where the operator left it
  // rather than travelling with the move.
  async moveSession(sessionID: string, directory: string, signal?: AbortSignal): Promise<void> {
    const client = this.#require(
      `the OpenCode move-session route ${MOVE_SESSION_ROUTE} is unavailable: this host handed the plugin no client (host version ${hostVersion()})`,
    )
    let result: RouteResult
    try {
      result = await client.post({ url: MOVE_SESSION_ROUTE, body: { sessionID, destination: { directory } }, signal })
    } catch (error) {
      throw new MoveSessionUnavailable(
        `the OpenCode move-session route ${MOVE_SESSION_ROUTE} is unreachable (host version ${hostVersion()}): ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    const response = result.response
    if (response.status === 404 || response.status === 405) {
      throw new MoveSessionUnavailable(
        `the OpenCode move-session route ${MOVE_SESSION_ROUTE} is absent from this host (host version ${hostVersion()}); Concord requires it and runs no launch fallback`,
      )
    }
    if (response.status === 204 || response.status === 200) return
    throw new MoveSessionRefused(
      `the OpenCode move-session route refused the retarget with status ${response.status}: ${await refusalText(result)}`,
    )
  }

  // sessionDirectory reads back where the host now runs a session. CD-0098 D3
  // refuses success unless the move landed on the claimed worktree, so the
  // answer comes from the host rather than from the request that asked for it.
  async sessionDirectory(sessionID: string, signal?: AbortSignal): Promise<string> {
    return this.#session(sessionID, "cannot read the session directory back", signal)
  }

  // probe answers whether this session can reach its host at all. CD-0098 D2
  // makes the retarget the only route into a claimed worktree, so a session
  // that cannot reach the host cannot start work, and the answer is worth
  // having before anything is captured or claimed. It reads the same session
  // route the readback uses, so it proves reachability and that the host knows
  // this session, without a route of its own.
  async probe(sessionID: string, signal?: AbortSignal): Promise<void> {
    await this.#session(sessionID, "the host control plane is unreachable", signal)
  }

  // liveSessionDirectories reports every session the host currently holds and
  // the directory each runs in (issue #722). A worktree removal cannot decide
  // safety without it, and the store cannot hold it: no event records a
  // session leaving a directory, so a stored answer would go stale silently.
  // A failure throws rather than answering with an empty list, because "no
  // session occupies this worktree" and "I could not look" must not read the
  // same to the caller that is about to delete a directory.
  async liveSessionDirectories(signal?: AbortSignal): Promise<ObservedSessionDirectory[]> {
    const prefix = "cannot read the host session list"
    const client = this.#require(`${prefix}: this host handed the plugin no client (host version ${hostVersion()})`)
    let result: RouteResult
    try {
      result = await client.get({ url: SESSION_LIST_ROUTE, signal })
    } catch (error) {
      throw new MoveSessionRefused(`${prefix}: ${error instanceof Error ? error.message : String(error)}`)
    }
    if (!result.response.ok) {
      throw new MoveSessionRefused(`${prefix}: the host answered ${result.response.status}: ${await refusalText(result)}`)
    }
    if (!Array.isArray(result.data)) {
      throw new MoveSessionRefused("the session list was not an array")
    }
    const observed: ObservedSessionDirectory[] = []
    for (const session of result.data) {
      const id = (session as { id?: unknown } | null)?.id
      const directory = (session as { directory?: unknown } | null)?.directory
      // `directory` is required on every session record. A record missing it
      // is a host contract the adapter does not recognize, and skipping it
      // would silently narrow the population the gate decides on.
      if (typeof id !== "string" || !id || typeof directory !== "string" || !directory) {
        throw new MoveSessionRefused("the session list carried a record without an id and a directory")
      }
      observed.push({ session_ref: id, directory })
    }
    return observed
  }

  // showToast puts one line in front of the operator. Delivery is best effort
  // by construction: every caller reports an effect that already happened, so
  // a host without an attached TUI must not turn a completed operation into a
  // failure. The caller keeps the same text in its own result.
  async showToast(message: string, variant: "info" | "warning", signal?: AbortSignal): Promise<boolean> {
    if (!this.#client) return false
    try {
      const result = await this.#client.post({
        url: SHOW_TOAST_ROUTE,
        body: { title: "Concord", message, variant },
        signal,
      })
      return result.response.ok
    } catch {
      return false
    }
  }

  #require(message: string): RouteClient {
    if (!this.#client) throw new MoveSessionUnavailable(message)
    return this.#client
  }

  // session reads one session record. Both callers want the same request and
  // differ only in what a failure means to them, so the wording arrives as a
  // prefix rather than each caller repeating the request.
  async #session(sessionID: string, prefix: string, signal?: AbortSignal): Promise<string> {
    const client = this.#require(`${prefix}: this host handed the plugin no client (host version ${hostVersion()})`)
    let result: RouteResult
    try {
      result = await client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal })
    } catch (error) {
      throw new MoveSessionRefused(`${prefix}: ${error instanceof Error ? error.message : String(error)}`)
    }
    if (!result.response.ok) {
      throw new MoveSessionRefused(`${prefix}: the host answered ${result.response.status}: ${await refusalText(result)}`)
    }
    const directory = (result.data as { directory?: unknown } | null | undefined)?.directory
    if (typeof directory !== "string" || !directory) {
      throw new MoveSessionRefused("the session readback carried no directory")
    }
    return directory
  }
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
