import { concordBinaryPath, defaultRunner, type DispatchRunner } from "./dispatch"

const CONTINUITY_TTL_MS = 10_000
const START_SENTINEL = "<!-- concord:continuity:v1 -->"
const END_SENTINEL = "<!-- /concord:continuity:v1 -->"
const IDENTITY = /^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$/
const SENTINEL_BLOCK = new RegExp(`${escapeRegExp(START_SENTINEL)}[\\s\\S]*?${escapeRegExp(END_SENTINEL)}`)

type ContinuityOutput = { system: string[] }
type ContinuityOptions = { runner?: DispatchRunner; now?: () => number }
type CacheEntry = { attemptedAt: number; block?: string }

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}

function selectedIdentityValue(name: string): string {
  const value = process.env[name] ?? ""
  return IDENTITY.test(value) ? value : ""
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
  const cache = new Map<string, CacheEntry>()

  return async (_input: unknown, output: ContinuityOutput): Promise<void> => {
    try {
      const productID = selectedIdentityValue("CONCORD_SELECTED_PRODUCT_ID")
      const workID = selectedIdentityValue("CONCORD_SELECTED_WORK_ID")
      if (!productID || !workID) return

      const identity = `${productID}\u0000${workID}`
      const attemptedAt = now()
      const cached = cache.get(identity)
      if (cached && attemptedAt >= cached.attemptedAt && attemptedAt - cached.attemptedAt < CONTINUITY_TTL_MS) {
        if (cached.block) applyBlock(output, cached.block)
        return
      }

      cache.set(identity, { attemptedAt })
      const result = await runner.run([concordBinaryPath(), "continuity-block"], "", new AbortController().signal)
      if (result.exitCode !== 0 || typeof result.stdout !== "string" || result.stdout.length === 0) return

      const block = renderBlock(result.stdout)
      cache.set(identity, { attemptedAt, block })
      applyBlock(output, block)
    } catch {
      return
    }
  }
}
