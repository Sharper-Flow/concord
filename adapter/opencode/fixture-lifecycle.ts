// Process-owned fixture-root lifecycle for adapter tests (issue-confirmed
// retention: roots allocated with mkdtemp survived the run whenever the test
// callback's finally never executed — a timed-out or hung await, a teardown
// exception, or a SIGINT/SIGTERM that killed the process outright).
//
// Ownership lives with the run, not with one callback's finally block. Every
// allocated root registers its exact path in a process-scoped registry the
// instant mkdtemp returns, before any other await can strand it, and three
// independent paths remove registered roots:
//
//   1. releaseFixtureRoot — the explicit teardown a completed test calls.
//   2. a per-root deadline timer — armed from the test's declared timeout, it
//      cancels and drains the root's children and removes the root even when
//      the test's own finally never runs (the timeout and hung-await path).
//   3. process-level guards — an exit hook sweeps synchronously when the run
//      ends for any other reason, and SIGINT/SIGTERM handlers sweep and then
//      exit with the conventional 130/143 so a signal keeps its status.
//
// Only exact registered paths are removed. A directory that this process never
// registered — a same-prefix sibling, another run's artifact root — is never
// touched, so concurrent runs cannot delete each other. SIGKILL and OOM kill
// the process before any in-process path can run; each root carries an
// ownership marker file so such leftovers stay identifiable afterwards, but
// this module never claims to reclaim them.
//
// Every removal attempt records a journal entry (cleanup entry, removal
// entry, completion) and a failed removal records a visible removal error on
// stderr and stays registered for the exit sweep to retry — cleanup failure is
// reported, never swallowed, and never widens into a wildcard delete.
import { mkdtemp, rm } from "node:fs/promises"
import { rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

// Written into every allocated root so a leftover from a SIGKILL or OOM —
// neither of which can execute in-process cleanup — is still identifiable as
// this harness's owned fixture root.
export const OWNED_ROOT_MARKER = ".concord-owned-fixture-root.json"

export type FixtureJournalKind =
  | "cleanup_entry"
  | "child_cancel"
  | "child_drained"
  | "removal_entry"
  | "completion"
  | "removal_error"

export interface FixtureJournalEntry {
  kind: FixtureJournalKind
  root: string
  detail?: string
}

export interface FixtureRelease {
  root: string
  removed: boolean
  errors: string[]
}

interface OwnedFixtureRoot {
  path: string
  prefix: string
  children: Set<Bun.Subprocess>
  deadline: Timer | undefined
}

// How long a cancelled child gets to exit on SIGTERM before the drain forces
// it out with SIGKILL. Fixture children are short-lived CLI processes; a brief
// grace keeps their own cleanup hooks running without stalling teardown.
const CHILD_DRAIN_GRACE_MS = 1_500

const ownedRoots = new Map<string, OwnedFixtureRoot>()
const unscopedChildren = new Set<Bun.Subprocess>()
const journal: FixtureJournalEntry[] = []
// The root children attach to when no explicit root is named: the most
// recently allocated root. Tests allocate their root before spawning, so each
// test's children attach to that test's root and a late deadline cleanup for
// one root can never drain a later test's children.
let currentScope: string | undefined

export function fixtureJournal(): readonly FixtureJournalEntry[] {
  return [...journal]
}

export function registeredFixtureRoots(): readonly string[] {
  return [...ownedRoots.keys()]
}

function record(entry: FixtureJournalEntry): void {
  journal.push(entry)
  if (entry.kind === "removal_error") {
    // A removal failure must be visible in the run's output, not only in the
    // in-memory journal the dead process takes with it.
    console.error(`fixture-lifecycle removal_error ${entry.root}: ${entry.detail ?? "unknown error"}`)
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? `${error.name}: ${error.message}` : String(error)
}

function forgetChild(children: Set<Bun.Subprocess>, child: Bun.Subprocess): void {
  children.delete(child)
}

// Attach a spawned child to a root so cleanup cancels and drains it before
// removing the root. Without an explicit root the child attaches to the
// current scope; with no live scope it lands in a fallback set that only the
// process-level sweep drains, so it is never dropped and never misattributed.
export function trackFixtureChild(child: Bun.Subprocess, root?: string): void {
  const owned = root !== undefined ? ownedRoots.get(root) : currentScope !== undefined ? ownedRoots.get(currentScope) : undefined
  const children = owned?.children ?? unscopedChildren
  children.add(child)
  void child.exited.then(
    () => forgetChild(children, child),
    () => forgetChild(children, child),
  )
}

async function cancelAndDrainChildren(owned: OwnedFixtureRoot, reason: string): Promise<void> {
  const live = [...owned.children]
  if (live.length === 0) return
  for (const child of live) {
    try {
      child.kill("SIGTERM")
    } catch {
      // Already gone: the drain below still waits out its exit.
    }
  }
  record({ kind: "child_cancel", root: owned.path, detail: `${reason}: ${live.length} child(ren)` })
  const drained = Promise.allSettled(live.map((child) => child.exited))
  const grace = new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, CHILD_DRAIN_GRACE_MS)
    timer.unref?.()
  })
  await Promise.race([drained, grace])
  for (const child of live) {
    if (!owned.children.has(child)) continue
    try {
      child.kill("SIGKILL")
    } catch {
      // Already gone.
    }
  }
  await Promise.allSettled(live.map((child) => child.exited))
  record({ kind: "child_drained", root: owned.path, detail: `${reason}: ${live.length} child(ren)` })
}

let guardsInstalled = false

function installProcessGuards(): void {
  if (guardsInstalled) return
  guardsInstalled = true
  process.on("exit", () => sweepOwnedRootsSync("process_exit"))
  // A signal must clean up the registered roots and still die as a signal
  // death: the handlers sweep synchronously, then exit with the conventional
  // status (128 + signal number) so a failing run keeps its failing status.
  process.on("SIGINT", () => {
    sweepOwnedRootsSync("sigint")
    process.exit(130)
  })
  process.on("SIGTERM", () => {
    sweepOwnedRootsSync("sigterm")
    process.exit(143)
  })
}

// The synchronous sweep for paths that get no further await: process exit and
// signal deaths. Children are killed outright — there is no time for a
// graceful drain — and each registered root is removed by exact path. A failed
// removal records a visible error and the root stays registered, identified by
// its marker file for the operator; nothing is wildcard-deleted.
export function sweepOwnedRootsSync(reason: string): void {
  for (const child of unscopedChildren) {
    try {
      child.kill("SIGKILL")
    } catch {
      // Already gone.
    }
  }
  for (const owned of [...ownedRoots.values()]) {
    if (owned.deadline) clearTimeout(owned.deadline)
    for (const child of owned.children) {
      try {
        child.kill("SIGKILL")
      } catch {
        // Already gone.
      }
    }
    record({ kind: "cleanup_entry", root: owned.path, detail: reason })
    record({ kind: "removal_entry", root: owned.path, detail: reason })
    try {
      rmSync(owned.path, { recursive: true, force: true })
      ownedRoots.delete(owned.path)
      record({ kind: "completion", root: owned.path, detail: reason })
    } catch (error) {
      record({ kind: "removal_error", root: owned.path, detail: `${reason}: ${errorMessage(error)}` })
    }
  }
}

export interface AllocateFixtureRootOptions {
  // The test's declared timeout. The armed deadline mirrors it: when the test
  // times out, its finally never runs, and this timer performs the removal
  // instead. The timer never holds the process open.
  timeoutMs?: number
}

export async function allocateFixtureRoot(prefix: string, options: AllocateFixtureRootOptions = {}): Promise<string> {
  const path = await mkdtemp(join(tmpdir(), prefix))
  // Register the exact allocated root immediately: the next statement is
  // synchronous, so no intermediate failure can strand an unregistered root.
  const owned: OwnedFixtureRoot = { path, prefix, children: new Set(), deadline: undefined }
  ownedRoots.set(path, owned)
  currentScope = path
  installProcessGuards()
  await Bun.write(join(path, OWNED_ROOT_MARKER), JSON.stringify({
    owner: "concord-adapter-test-fixture",
    pid: process.pid,
    prefix,
    allocated_at: new Date().toISOString(),
  }, null, 2) + "\n")
  if (options.timeoutMs !== undefined) {
    const timeoutMs = options.timeoutMs
    const deadline = setTimeout(() => {
      void releaseFixtureRoot(path, `deadline_after_${timeoutMs}ms`)
    }, timeoutMs)
    deadline.unref?.()
    owned.deadline = deadline
  }
  return path
}

// Remove one registered root: cancel and drain its children first, then remove
// the exact path. Removal failure records a visible error and leaves the root
// registered so the process-exit sweep retries it; the returned outcome lets a
// caller that still runs assert the failure instead of guessing.
export async function releaseFixtureRoot(root: string, reason = "test_teardown"): Promise<FixtureRelease> {
  const owned = ownedRoots.get(root)
  if (!owned) return { root, removed: true, errors: [] }
  if (owned.deadline) clearTimeout(owned.deadline)
  if (currentScope === root) currentScope = undefined
  record({ kind: "cleanup_entry", root, detail: reason })
  await cancelAndDrainChildren(owned, reason)
  record({ kind: "removal_entry", root, detail: reason })
  try {
    await rm(root, { recursive: true, force: true })
    ownedRoots.delete(root)
    record({ kind: "completion", root, detail: reason })
    return { root, removed: true, errors: [] }
  } catch (error) {
    const message = errorMessage(error)
    record({ kind: "removal_error", root, detail: message })
    return { root, removed: false, errors: [message] }
  }
}

export interface FixtureProcessOptions {
  cwd?: string
  env?: Record<string, string>
  // Connected to the spawned child for its whole life: an abort that fires
  // after spawn kills the child immediately, where the old
  // `if (signal.aborted) child.kill()` pattern only covered an abort that had
  // already happened at spawn time.
  signal?: AbortSignal
}

export interface FixtureProcessResult {
  exitCode: number
  stdout: string
  stderr: string
}

// Spawn a fixture-owned child: tracked for cancel-and-drain cleanup, with late
// AbortSignal cancellation connected. Resolves only after the child exits and
// its output streams close, so a returned child is a drained child.
export async function runFixtureProcess(argv: string[], input = "", options: FixtureProcessOptions = {}): Promise<FixtureProcessResult> {
  const child = Bun.spawn(argv, {
    cwd: options.cwd,
    env: options.env ?? { ...process.env },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  trackFixtureChild(child)
  let abortListener: (() => void) | undefined
  if (options.signal) {
    const signal = options.signal
    abortListener = () => child.kill("SIGTERM")
    if (signal.aborted) abortListener()
    else signal.addEventListener("abort", abortListener, { once: true })
  }
  try {
    await child.stdin.write(input)
    await child.stdin.end()
  } catch {
    // The child already exited or was cancelled: its exit status and any
    // captured output still resolve below.
  }
  const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  if (abortListener && options.signal) options.signal.removeEventListener("abort", abortListener)
  return { exitCode, stdout, stderr }
}
