import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

import { manifestDigest } from "./generated-contracts"

// The adapter's contract manifest pin (issue #885).
//
// `generated-contracts.ts` stamps one digest per release. A release that
// lands while a session runs leaves that session's module holding the
// previous release's digest, and every typed call then refuses on version
// skew — including sessions whose process restarted after the files
// changed, because a session resumed by id restores the pin it booted with.
// The pin therefore cannot be a bare module constant read once per load.
//
// This module keeps the adopted digest mutable and resolves the digest the
// installed files actually stamp. When the core answers with a digest that
// differs from the pin, the caller compares it against the disk value:
// a match means the files on disk are current and only this process's pin
// is stale, so the caller adopts the disk digest and retries the same
// request once. The core's idempotency absorbs the replay for mutations,
// and reads have no effect to absorb.
let adopted: string | null = null

// Test seam: the file the resolver reads. Null resolves the sibling
// generated-contracts.ts of this module, which is the installed source of
// the pin.
let sourceOverride: string | null = null

const digestPattern = /manifestDigest = "(sha256:[0-9a-f]{64})"/

export function activeManifestDigest(): string {
  return adopted ?? manifestDigest
}

export function adoptManifestDigest(digest: string): boolean {
  if (!/^sha256:[0-9a-f]{64}$/.test(digest)) return false
  adopted = digest
  return true
}

export function resetManifestPinForTesting(): void {
  adopted = null
  sourceOverride = null
}

export function setManifestSourceForTesting(path: string | null): void {
  sourceOverride = path
}

// resolveDiskManifestDigest reads the digest the installed contract module
// declares. It answers null when the file cannot be read or carries no
// recognizable declaration: a null is not a digest, so the caller refuses
// rather than adopting a guess.
export function resolveDiskManifestDigest(): string | null {
  try {
    const path = sourceOverride ?? fileURLToPath(new URL("./generated-contracts.ts", import.meta.url))
    const match = readFileSync(path, "utf8").match(digestPattern)
    return match ? match[1] : null
  } catch {
    return null
  }
}
