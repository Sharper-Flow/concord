import { concordBinaryPath, defaultRunner, type DispatchRunner } from "./dispatch"
import { formatWorkStateLine } from "./workflow-status"

const CONTINUITY_TTL_MS = 10_000
const MAX_SESSION_IDENTITIES = 512
const START_SENTINEL = "<!-- concord:continuity:v1 -->"
const END_SENTINEL = "<!-- /concord:continuity:v1 -->"
const IDENTITY = /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/
const SENTINEL_BLOCK = new RegExp(`${escapeRegExp(START_SENTINEL)}[\\s\\S]*?${escapeRegExp(END_SENTINEL)}`)

type ContinuityOutput = { system: string[] }
type ContinuityOptions = { runner?: DispatchRunner; now?: () => number }
type CacheEntry = { attemptedAt: number; block?: string }
type SessionIdentity = { productID: string; workID: string }

const sessionIdentities = new Map<string, SessionIdentity>()

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}

function selectedIdentityValue(name: string): string {
  const value = process.env[name] ?? ""
  return IDENTITY.test(value) ? value : ""
}

function validSessionID(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 128
}

export function bindWorkStartSessionIdentity(sessionID: string, productID: string, workID: string): void {
  if (!validSessionID(sessionID) || !IDENTITY.test(productID) || !IDENTITY.test(workID)) return
  sessionIdentities.delete(sessionID)
  sessionIdentities.set(sessionID, { productID, workID })
  while (sessionIdentities.size > MAX_SESSION_IDENTITIES) {
    const oldest = sessionIdentities.keys().next()
    if (oldest.done) return
    sessionIdentities.delete(oldest.value)
  }
}

function sessionIdentity(input: unknown): { cacheKey: string; identity: SessionIdentity } | null {
  const sessionID = input !== null && typeof input === "object" && !Array.isArray(input)
    ? (input as { sessionID?: unknown }).sessionID
    : undefined
  if (validSessionID(sessionID)) {
    const registered = sessionIdentities.get(sessionID)
    if (registered) {
      sessionIdentities.delete(sessionID)
      sessionIdentities.set(sessionID, registered)
      return { cacheKey: `session:${sessionID}\u0000${registered.productID}\u0000${registered.workID}`, identity: registered }
    }
  }
  const productID = selectedIdentityValue("CONCORD_SELECTED_PRODUCT_ID")
  const workID = selectedIdentityValue("CONCORD_SELECTED_WORK_ID")
  return productID && workID ? { cacheKey: `launcher:${productID}\u0000${workID}`, identity: { productID, workID } } : null
}

function renderBlock(stdout: string): string {
  let stateLine = ""
  try {
    const packet = JSON.parse(stdout)
    const continuity = packet && typeof packet === "object" && !Array.isArray(packet) ? packet.continuity : undefined
    const pinned = continuity && typeof continuity === "object" && !Array.isArray(continuity) ? continuity.pinned : undefined
    const pin = pinned && typeof pinned === "object" && !Array.isArray(pinned) ? pinned.work_pin : undefined
    stateLine = formatWorkStateLine(pin) ?? ""
  } catch {
    stateLine = ""
  }
  return `${START_SENTINEL}\n${stateLine ? `${stateLine}\n` : ""}${stdout}\n${END_SENTINEL}`
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
  const cache = new Map<string, CacheEntry>()

  return async (input: unknown, output: ContinuityOutput): Promise<void> => {
    try {
      const selected = sessionIdentity(input)
      if (!selected) return

      const { cacheKey, identity } = selected
      const attemptedAt = now()
      const cached = cache.get(cacheKey)
      if (cached && attemptedAt >= cached.attemptedAt && attemptedAt - cached.attemptedAt < CONTINUITY_TTL_MS) {
        if (cached.block) applyBlock(output, cached.block)
        return
      }

      cache.set(cacheKey, { attemptedAt })
      const result = await runner.run([concordBinaryPath(), "continuity-block"], "", new AbortController().signal, {
        env: {
          CONCORD_SELECTED_PRODUCT_ID: identity.productID,
          CONCORD_SELECTED_WORK_ID: identity.workID,
        },
      })
      if (result.exitCode !== 0 || typeof result.stdout !== "string" || result.stdout.length === 0) return

      const block = renderBlock(result.stdout)
      cache.set(cacheKey, { attemptedAt, block })
      applyBlock(output, block)
    } catch {
      return
    }
  }
}
