// CD-0098 D2: the OpenCode move-session route, which retargets the calling
// session at a claimed worktree.
//
// The route is `POST /experimental/control-plane/move-session` on the host's
// own server, carrying `{ sessionID, destination: { directory } }` and
// answering 204 on success. It is experimental and absent from the generated
// SDK, so the adapter reaches it through the server URL the plugin factory
// receives rather than through a typed client method.
//
// Concord requires the route. There is no launch fallback: a host without it
// refuses work start, naming the route and the host version.

import { execFileSync } from "node:child_process"

export const MOVE_SESSION_ROUTE = "/experimental/control-plane/move-session"

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

// HostControlPlane holds the server URL and version the plugin factory
// receives. The tool path and the plugin entry reach the same instance through
// this module, which the OpenCode instance scopes to one running host.
export const hostControlPlane = (): HostControlPlane => shared

export type RouteFetch = (url: URL, init: RequestInit) => Promise<Response>

export class HostControlPlane {
  #serverUrl: URL | null = null
  #fetch: RouteFetch = (url, init) => fetch(url, init)

  // bind records the server URL the plugin factory was handed. A tool call
  // that runs before the factory bound one finds no route, which is the same
  // fail-closed answer as a host that does not serve one. The transport is
  // injectable so a test can exercise the route contract without a server.
  bind(serverUrl: URL | string | undefined, transport?: RouteFetch): void {
    this.#serverUrl = serverUrl ? new URL(String(serverUrl)) : null
    this.#fetch = transport ?? ((url, init) => fetch(url, init))
  }

  // moveSession retargets a running session at an absolute directory. It
  // never asks the host to carry local changes: CD-0098 D3 requires a clean
  // default checkout, so uncommitted work stays where the operator left it
  // rather than travelling with the move.
  async moveSession(sessionID: string, directory: string, signal?: AbortSignal): Promise<void> {
    if (!this.#serverUrl) {
      throw new MoveSessionUnavailable(
        `the OpenCode move-session route ${MOVE_SESSION_ROUTE} is unavailable: this host exposed no server URL (host version ${hostVersion()})`,
      )
    }
    const url = new URL(MOVE_SESSION_ROUTE, this.#serverUrl)
    let response: Response
    try {
      response = await this.#fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionID, destination: { directory } }),
        signal,
      })
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
    if (!this.#serverUrl) {
      throw new MoveSessionUnavailable(
        `cannot read the session directory back: this host exposed no server URL (host version ${hostVersion()})`,
      )
    }
    const url = new URL(`/session/${encodeURIComponent(sessionID)}`, this.#serverUrl)
    let response: Response
    try {
      response = await this.#fetch(url, { method: "GET", headers: { Accept: "application/json" }, signal })
    } catch (error) {
      throw new MoveSessionRefused(
        `cannot read the session directory back: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    if (!response.ok) {
      throw new MoveSessionRefused(
        `cannot read the session directory back: the host answered ${response.status}: ${await readRefusal(response)}`,
      )
    }
    let session: unknown
    try {
      session = await response.json()
    } catch (error) {
      throw new MoveSessionRefused(`the session readback was not JSON: ${error instanceof Error ? error.message : String(error)}`)
    }
    const directory = (session as { directory?: unknown } | null)?.directory
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
