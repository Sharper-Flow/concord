// Use the plugin's bound client: it retains host credentials and works without
// a network listener. A server URL is not a substitute for that transport.
export const SESSION_ROUTE = "/session/{id}"
export type RouteResult = { data?: unknown; response: Response }
type DirectoryClient = {
  get: (options: { url: string; path: Record<string, unknown>; signal?: AbortSignal }) => Promise<RouteResult>
}

class SessionDirectoryReader {
  #client: DirectoryClient | null = null

  bind(source: { client?: unknown } | DirectoryClient | undefined): void {
    const candidate = source && "get" in source ? source : (source?.client as { _client?: DirectoryClient } | undefined)?._client
    this.#client = candidate && typeof candidate.get === "function" ? candidate : null
  }

  async sessionDirectory(sessionID: string, signal?: AbortSignal): Promise<string> {
    const prefix = "cannot read the session directory back"
    if (!this.#client) throw new Error(`${prefix}: this host handed the plugin no client`)
    let result: RouteResult
    try {
      result = await this.#client.get({ url: SESSION_ROUTE, path: { id: sessionID }, signal })
    } catch (error) {
      throw new Error(`${prefix}: ${error instanceof Error ? error.message : String(error)}`)
    }
    if (!result.response.ok) throw new Error(`${prefix}: the host answered ${result.response.status}: ${await refusalText(result)}`)
    const directory = (result.data as { directory?: unknown } | null | undefined)?.directory
    if (typeof directory !== "string" || !directory) throw new Error("the session readback carried no directory")
    return directory
  }
}

const NO_REFUSAL = "the host returned no readable refusal"

async function refusalText(result: RouteResult): Promise<string> {
  // The SDK may already have consumed the body. Its parsed value is then the
  // only readable copy of the host's refusal.
  const carried = result.data
  if (typeof carried === "string") return messageFrom(carried)
  if (carried !== null && carried !== undefined) {
    const envelope = carried as { data?: { message?: unknown }; message?: unknown }
    const message = envelope.data?.message ?? envelope.message
    return typeof message === "string" && message ? message : NO_REFUSAL
  }
  try { return messageFrom(await result.response.text()) } catch { return NO_REFUSAL }
}

function messageFrom(body: string): string {
  try {
    const parsed = JSON.parse(body) as { data?: { message?: unknown }; message?: unknown }
    const message = parsed?.data?.message ?? parsed?.message
    if (typeof message === "string" && message) return message
  } catch {
    // Non-JSON refusals retain the host's words.
  }
  return body.slice(0, 512) || NO_REFUSAL
}

const shared = new SessionDirectoryReader()
export const sessionDirectoryReader = (): SessionDirectoryReader => shared
