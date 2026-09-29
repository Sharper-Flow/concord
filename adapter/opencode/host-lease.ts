// host-lease — the session's claim on the release it runs (CD-0111 D1/D2).
//
// The plugin factory claims the lease at load: it runs the core binary this
// adapter is stamped against, with the host process pid, and the core writes
// the lease beside the database. The installer reads the lease set to decide
// which release directories it may remove, and concord upgrade refuses while
// a lease predates a pending breaking migration.
//
// A session that cannot claim a lease is not an unprotected session that
// runs anyway. Every adapter tool call refuses until the lease exists, so
// the failure reaches the operator instead of a release directory quietly
// disappearing under a live session.

import fs from "node:fs"
import path from "node:path"
import { concordBinaryPath, defaultRunner, type DispatchRunner } from "./dispatch"
import { coreBinary, releaseRoot } from "./generated-release"
import { manifestDigest } from "./generated-contracts"

type HostLease = {
  pid: number
  pid_start: number
  release_root: string
  core_binary: string
  schema_version: number
  manifest_digest: string
  directory?: string
  worktree?: string
}

let leaseFault: string | null = null

export function hostLeaseFault(): string | null {
  return leaseFault
}

/** Test seam: clear a recorded fault, claim against an injected runner, or
 * stamp a release identity the way the installer would. */
export function configureHostLease(options: { runner?: DispatchRunner; reset?: boolean; release?: { coreBinary: string; releaseRoot: string } } = {}) {
  if (options.reset) {
    leaseFault = null
    runner = defaultRunner
    claimedCoreBinary = coreBinary
    claimedReleaseRoot = releaseRoot
  }
  if (options.runner) runner = options.runner
  if (options.release) {
    claimedCoreBinary = options.release.coreBinary
    claimedReleaseRoot = options.release.releaseRoot
  }
}

let runner: DispatchRunner = defaultRunner
let claimedCoreBinary: string = coreBinary
let claimedReleaseRoot: string = releaseRoot

/** Session location named in the lease so a breaking-migration refusal can
 * point the operator at the exact terminal to end. */
export type LeaseLocation = { directory?: string; worktree?: string }

// CD-0191: installed-versus-session release staleness. The adapter is the
// only component that sees both facts: the pinned releaseRoot it is stamped
// against and the host's installed release, which the installer repoints by
// rewriting the `current` symlink beside the pinned root (the pinned root's
// parent directory is the data root). A session whose pinned release differs
// from the installed release still runs (CD-0111 D1 keeps the pair it started
// with), but the lane definitions its process holds were rewritten on disk,
// so staleness is visible on every result and lane dispatch refuses.
export type ReleaseStaleness = {
  pinnedReleaseRoot: string
  installedReleaseRoot: string
  pinnedRelease: string
  installedRelease: string
}

/** releaseDisplayName names a release by its directory, which the installer
 * derives from the version. A root without a basename falls back to the root
 * itself so the display never empties. */
export function releaseDisplayName(root: string): string {
  const base = path.basename(root)
  return base === "" || base === "/" || base === "." ? root : base
}

/** resolveInstalledReleaseRoot reads the host's installed release through the
 * `current` symlink beside the pinned releaseRoot. One readlink per call. It
 * returns null when this adapter copy is unstamped or the link is absent or
 * unreadable: staleness is never guessed from a missing observation. */
export function resolveInstalledReleaseRoot(): string | null {
  const pinned = claimedReleaseRoot
  if (!pinned) return null
  const dataRoot = path.dirname(pinned)
  let target: string
  try {
    target = fs.readlinkSync(path.join(dataRoot, "current"))
  } catch {
    return null
  }
  return path.resolve(dataRoot, target)
}

/** releaseStaleness compares the pinned releaseRoot with the installed
 * release once. Any difference is stale, in both directions: an upgrade and
 * a rollback both rewrite the lane files a running process still holds in
 * memory. Returns null when the session is fresh or staleness cannot be
 * determined. */
export function releaseStaleness(): ReleaseStaleness | null {
  const pinned = claimedReleaseRoot
  if (!pinned) return null
  const installed = resolveInstalledReleaseRoot()
  if (installed === null || installed === path.resolve(pinned)) return null
  return {
    pinnedReleaseRoot: pinned,
    installedReleaseRoot: installed,
    pinnedRelease: releaseDisplayName(pinned),
    installedRelease: releaseDisplayName(installed),
  }
}

export async function claimHostLease(pid: number, location: LeaseLocation = {}): Promise<void> {
  try {
    if (!claimedCoreBinary || !claimedReleaseRoot) {
      leaseFault = "this adapter copy is not bound to a release, so the session cannot claim a host lease; the tools stay closed until the adapter runs from an installed release (CD-0111 D1)"
      return
    }
    const abort = new AbortController()
    const result = await runner.run([concordBinaryPath(), "host-lease"], JSON.stringify({ pid, directory: location.directory ?? "", worktree: location.worktree ?? "" }), abort.signal)
    if (result.exitCode !== 0) {
      leaseFault = `host lease claim failed with exit ${result.exitCode}: ${result.stderr.slice(0, 400)}`
      return
    }
    let lease: HostLease
    try {
      lease = JSON.parse(result.stdout.trim().split("\n").pop() ?? "") as HostLease
    } catch {
      leaseFault = `host lease claim returned an unreadable response: ${result.stdout.slice(0, 200)}`
      return
    }
    if (lease.release_root !== claimedReleaseRoot || lease.core_binary !== claimedCoreBinary) {
      leaseFault = `host lease names ${lease.release_root} but this adapter is stamped against ${claimedReleaseRoot}; the paired release constants disagree (CD-0111 D1)`
      return
    }
    if (lease.manifest_digest !== manifestDigest) {
      leaseFault = `host lease names contract digest ${lease.manifest_digest} but this adapter carries ${manifestDigest}; the paired release constants disagree (CD-0111 D1)`
      return
    }
    leaseFault = null
  } catch (error) {
    leaseFault = `host lease claim threw: ${error instanceof Error ? error.message : String(error)}`
  }
}
