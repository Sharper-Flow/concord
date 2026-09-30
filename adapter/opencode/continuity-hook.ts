import { concordBinaryPath, defaultRunner, type DispatchRunner } from "./dispatch"
import { hostControlPlane } from "./move-session"

const CONTINUITY_TTL_MS = 10_000
// Session identities the render cache can hold. The cache is process-local
// and otherwise unbounded, so the cap evicts the least recently rendered
// session, mirroring the agent-switch hook's bound.
const MAX_CACHED_SESSIONS = 512
const START_SENTINEL = "<!-- concord:continuity:v1 -->"
const END_SENTINEL = "<!-- /concord:continuity:v1 -->"
const SENTINEL_BLOCK = new RegExp(`${escapeRegExp(START_SENTINEL)}[\\s\\S]*?${escapeRegExp(END_SENTINEL)}`)

type ContinuityOutput = { system: string[] }
// The host transform input carries the session identity when the render
// belongs to a session. The Agent.generate path carries none.
type ContinuityInput = { sessionID?: string }
type CacheEntry = { attemptedAt: number; block?: string }

// ContinuitySessionSource is the host session surface the transform reads:
// the managed-parent boundary and the live session directory.
export type ContinuitySessionSource = {
  hasManagedParent(sessionID: string): Promise<boolean>
  sessionDirectory(sessionID: string): Promise<string>
}

type ContinuityOptions = { runner?: DispatchRunner; now?: () => number; sessions?: ContinuitySessionSource }

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}

function renderBlock(stdout: string): string {
  return `${START_SENTINEL}\n${stdout}\n${END_SENTINEL}`
}

function applyBlock(output: ContinuityOutput, block: string): void {
  if (!Array.isArray(output.system) || typeof output.system[0] !== "string") return
  output.system[0] = SENTINEL_BLOCK.test(output.system[0])
    ? output.system[0].replace(SENTINEL_BLOCK, block)
    : output.system[0] + block
}

export function createContinuityTransform(options: ContinuityOptions = {}) {
  const runner = options.runner ?? defaultRunner
  const now = options.now ?? Date.now
  const sessions = options.sessions ?? hostControlPlane()
  const cache = new Map<string, CacheEntry>()

  return async (input: ContinuityInput, output: ContinuityOutput): Promise<void> => {
    try {
      // A transform without a session identity (the Agent.generate path) has
      // no session to resolve, so it renders no block and does not throw.
      const sessionID = typeof input?.sessionID === "string" ? input.sessionID : ""
      if (!sessionID) return
      // A session with a managed parent is a dispatched lane; the dispatch
      // packet is the lane's complete Concord context (CD-0017 D4), so the
      // lane gets no continuity block of its parent's work.
      if (await sessions.hasManagedParent(sessionID)) return
      // The coordinator resolves its work from the directory the host runs
      // the session in, through the claimed worktree — never from the
      // launcher environment, which goes stale when work_start moves the
      // session. A directory that resolves no active claim prints no packet,
      // so the session renders no block rather than the launch item's.
      const directory = await sessions.sessionDirectory(sessionID)
      if (!directory) return

      const identity = `${sessionID}\u0000${directory}`
      const attemptedAt = now()
      const cached = cache.get(identity)
      if (cached && attemptedAt >= cached.attemptedAt && attemptedAt - cached.attemptedAt < CONTINUITY_TTL_MS) {
        if (cached.block) applyBlock(output, cached.block)
        return
      }

      while (cache.size >= MAX_CACHED_SESSIONS) {
        const oldest = cache.keys().next()
        if (oldest.done) break
        cache.delete(oldest.value)
      }
      cache.set(identity, { attemptedAt })
      const result = await runner.run([concordBinaryPath(), "continuity-block", directory], "", new AbortController().signal)
      if (result.exitCode !== 0 || typeof result.stdout !== "string" || result.stdout.length === 0) return

      const block = renderBlock(result.stdout)
      cache.set(identity, { attemptedAt, block })
      applyBlock(output, block)
    } catch {
      return
    }
  }
}
