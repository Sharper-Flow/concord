// CD-0098 D2: the OpenCode move-session route, which retargets the calling
// session at a claimed worktree.
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
// Concord requires the route. There is no launch fallback: a host without it
// refuses work start, naming the route and the host version.

import { execFileSync } from "node:child_process"

export const MOVE_SESSION_ROUTE = "/experimental/control-plane/move-session"
export const SESSION_ROUTE = "/session/{id}"

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
    let response: Response
    try {
      response = (await client.post({ url: MOVE_SESSION_ROUTE, body: { sessionID, destination: { directory } }, signal })).response
    } catch (error) {
      throw new MoveSessionUnavailable(
        `the OpenCode move-session route ${MOVE_SESSION_ROUTE} is unreachable (host version ${hostVersion()}): ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    if (response.status === 404 || response.status === 405) {
      throw new MoveSessionUnavailable(
        `the OpenCode move-session route ${MOVE_SESSION_ROUTE} is absent from this host (host version ${hostVersion()}); Concord requires it and runs no launch fallback`,
      )
    }
    if (response.status === 204 || response.status === 200) return
    throw new MoveSessionRefused(
      `the OpenCode move-session route refused the retarget with status ${response.status}: ${await readRefusal(response)}`,
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
      throw new MoveSessionRefused(`${prefix}: the host answered ${result.response.status}: ${await readRefusal(result.response)}`)
    }
    const directory = (result.data as { directory?: unknown } | null | undefined)?.directory
    if (typeof directory !== "string" || !directory) {
      throw new MoveSessionRefused("the session readback carried no directory")
    }
    return directory
  }
}

async function readRefusal(response: Response): Promise<string> {
  let body: string
  try {
    body = await response.text()
  } catch {
    return "the host returned no readable refusal"
  }
  try {
    const parsed = JSON.parse(body) as { data?: { message?: unknown }; message?: unknown }
    const message = parsed?.data?.message ?? parsed?.message
    if (typeof message === "string" && message) return message
  } catch {
    // A non-JSON body is reported as the host wrote it.
  }
  return body.slice(0, 512) || "the host returned no readable refusal"
}

const shared = new HostControlPlane()
