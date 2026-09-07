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
}

let leaseFault: string | null = null

export function hostLeaseFault(): string | null {
  return leaseFault
}

/** Test seam: clear a recorded fault, or claim against an injected runner. */
export function configureHostLease(options: { runner?: DispatchRunner; reset?: boolean } = {}) {
  if (options.reset) {
    leaseFault = null
    runner = defaultRunner
  }
  if (options.runner) runner = options.runner
}

let runner: DispatchRunner = defaultRunner

export async function claimHostLease(pid: number): Promise<void> {
  try {
    if (!coreBinary || !releaseRoot) {
      leaseFault = "this adapter copy is not bound to a release, so the session cannot claim a host lease; the tools stay closed until the adapter runs from an installed release (CD-0111 D1)"
      return
    }
    const abort = new AbortController()
    const result = await runner.run([concordBinaryPath(), "host-lease"], JSON.stringify({ pid }), abort.signal)
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
    if (lease.release_root !== releaseRoot || lease.core_binary !== coreBinary) {
      leaseFault = `host lease names ${lease.release_root} but this adapter is stamped against ${releaseRoot}; the paired release constants disagree (CD-0111 D1)`
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
