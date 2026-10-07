// Process-owned fixture-root lifecycle for adapter tests (issue-confirmed
// retention: roots allocated with mkdtemp survived the run whenever the test
// callback's finally never executed — a timed-out or hung await, a teardown
// exception, or a SIGINT/SIGTERM that killed the process outright).
//
// One callback-bound FixtureScope owns a root and every process spawned for
// it. The scope is the owner, never a most-recent-root heuristic: children
// bind to the scope whose body is running (AsyncLocalStorage), so overlapping
// scopes never trade children, and a scope refuses new work once expired.
//
// Expiry and settlement are separate. Bun:test runs the onTestFinished hook
// on a real timeout before the body settles, so at expiry the scope cancels
// and drains its children but removes nothing — the body can still resume and
// write into its owned root. Removal happens at body settlement (the test's
// close), and a body that never settles stays owned until the run's end sweep
// (bun:test never runs process "exit" hooks), where the root is finally
// removed. Unknown ownership is never reported as successful removal.
//
// Every fixture command runs under the Linux subreaper supervisor
// (fixture-supervisor.ts), which adopts and reaps separately grouped command
// descendants the outer process group cannot reach. SIGINT/SIGTERM drain all
// owned children asynchronously — a real drain with SIGTERM, a grace
// interval, SIGKILL, and awaited exits, never a synchronous kill described as
// one — and then run one final synchronous removal-and-exit block that
// preserves the conventional statuses 130/143 with no late callback write
// window. Only exact registered paths are removed; SIGKILL and OOM can run
// no in-process cleanup, and each root carries an ownership marker so such
// leftovers stay identifiable without any reclamation claim. Every removal
// attempt journals a cleanup entry, a removal entry, and a completion, and a
// failed removal records a visible error and stays registered for retry.
import { mkdtemp, rm } from "node:fs/promises"
import { existsSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { AsyncLocalStorage } from "node:async_hooks"
import { onTestFinished } from "bun:test"

// Written into every allocated root so a leftover from a SIGKILL or OOM —
// neither of which can execute in-process cleanup — is still identifiable as
// this harness's owned fixture root.
export const OWNED_ROOT_MARKER = ".concord-owned-fixture-root.json"

const SUPERVISOR = join(import.meta.dir, "fixture-supervisor.ts")

// How long a cancelled child gets to exit on SIGTERM before the drain forces
// it out with SIGKILL. This window must exceed the supervisor's own worst
// case (command SIGTERM grace + adopted-descendant grace + reap) so a
// supervised command tree always finishes its internal drain before this
// side escalates — killing the supervisor mid-drain would strand exactly the
// separately grouped descendants it is draining.
const CHILD_DRAIN_GRACE_MS = 2_500

export type FixtureJournalKind =
  | "cleanup_entry"
  | "child_cancel"
  | "child_drained"
  | "spawn_refused"
  | "descendants_drained"
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

export interface FixtureProcessOptions {
  cwd?: string
  env?: Record<string, string>
  // Connected to the spawned child for its whole life: an abort that fires
  // after spawn terminates the child immediately.
  signal?: AbortSignal
  // Overrides the AsyncLocalStorage scope for callers that track ownership
  // explicitly.
  scope?: FixtureScope
}

export interface FixtureProcessResult {
  exitCode: number
  stdout: string
  stderr: string
  // The supervisor PID, so a caller can observe containment of a live run.
  pid: number
}

export interface FixtureScopeDriverReport {
  argv0: string
  subreaper: boolean
  reason: string
  signalled: number[]
  killed: number[]
  reaped: number[]
}

const journal: FixtureJournalEntry[] = []
const registry = new Map<string, FixtureScope>()
const unscopedChildren = new Set<Bun.Subprocess>()
const scopeStorage = new AsyncLocalStorage<FixtureScope>()
let guardsInstalled = false
let terminating = false

export function fixtureJournal(): readonly FixtureJournalEntry[] {
  return [...journal]
}

export function registeredFixtureRoots(): readonly string[] {
  return [...registry.keys()]
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

async function drainChildren(children: Set<Bun.Subprocess>, root: string, reason: string): Promise<void> {
  const live = [...children]
  if (live.length === 0) return
  for (const child of live) {
    try {
      child.kill("SIGTERM")
    } catch {
      // Already gone: the wait below still settles its exit.
    }
  }
  record({ kind: "child_cancel", root, detail: `${reason}: ${live.length} child(ren)` })
  const drained = Promise.allSettled(live.map((child) => child.exited))
  const grace = new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, CHILD_DRAIN_GRACE_MS)
    timer.unref?.()
  })
  await Promise.race([drained, grace])
  for (const child of live) {
    try {
      child.kill("SIGKILL")
    } catch {
      // Already gone.
    }
  }
  await Promise.allSettled(live.map((child) => child.exited))
  record({ kind: "child_drained", root, detail: `${reason}: ${live.length} child(ren)` })
}

// The single owner of one allocated fixture root and every process bound to
// it. Expiry (bun:test's timeout hook) cancels and drains children but leaves
// the root in place — the body may still resume and write. Settlement (close)
// drains again and removes the root; a removal that fails keeps the root
// registered for retry and is reported, never swallowed.
export class FixtureScope {
  readonly path: string
  readonly prefix: string
  private readonly children = new Set<Bun.Subprocess>()
  private expired = false
  private settledWith: FixtureRelease | undefined
  private drainInFlight: Promise<void> | undefined

  constructor(path: string, prefix: string) {
    this.path = path
    this.prefix = prefix
  }

  get expired(): boolean {
    return this.expired
  }

  attach(child: Bun.Subprocess): void {
    this.children.add(child)
    void child.exited.then(
      () => this.children.delete(child),
      () => this.children.delete(child),
    )
  }

  private async drain(reason: string): Promise<void> {
    if (this.drainInFlight) return this.drainInFlight
    this.drainInFlight = drainChildren(this.children, this.path, reason).finally(() => {
      this.drainInFlight = undefined
    })
    return this.drainInFlight
  }

  // Synchronous kill for the last-resort sweeps, which cannot await. This is
  // an honest kill, never a drain: nothing here waits for an exit.
  killChildrenSync(): void {
    for (const child of this.children) {
      try {
        child.kill("SIGKILL")
      } catch {
        // Already gone.
      }
    }
  }

  // Expiry: the owning test's callback can no longer be trusted to finish in
  // order, so its children are cancelled and drained now. The root itself is
  // deliberately kept: a timed-out body can still resume and write into it,
  // and removing it early would let that resumption recreate the directory
  // while the registry reports it gone. New work is refused from here on.
  async expire(reason: string): Promise<void> {
    if (this.expired) return
    this.expired = true
    await this.drain(reason)
  }

  // Settlement: the body is done (or the run is ending). Drain any remaining
  // children, then remove the exact registered root. The outcome is returned
  // and journaled — a failure is visible and the root stays registered — and
  // the caller decides how a failed removal fails its test.
  async close(reason = "test_teardown"): Promise<FixtureRelease> {
    if (this.settledWith?.removed) return this.settledWith
    await this.drain(reason)
    record({ kind: "cleanup_entry", root: this.path, detail: reason })
    record({ kind: "removal_entry", root: this.path, detail: reason })
    try {
      await rm(this.path, { recursive: true, force: true })
      if (existsSync(this.path)) {
        throw new Error(`root still present after removal: ${this.path}`)
      }
      registry.delete(this.path)
      record({ kind: "completion", root: this.path, detail: reason })
      this.settledWith = { root: this.path, removed: true, errors: [] }
      return this.settledWith
    } catch (error) {
      const message = errorMessage(error)
      record({ kind: "removal_error", root: this.path, detail: `${reason}: ${message}` })
      this.settledWith = { root: this.path, removed: false, errors: [message] }
      return this.settledWith
    }
  }

  // Fails the test when the body passed but its cleanup did not: a cleanup
  // failure must never turn a passing run into exit 0. Call it after the
  // body's finally block, so an original body error propagates untouched.
  ensureReleased(): void {
    if (this.settledWith && !this.settledWith.removed) {
      throw new Error(`fixture cleanup failed for ${this.path}: ${this.settledWith.errors.join("; ")}`)
    }
  }

  // Run a body bound to this scope: every runFixtureProcess call inside it
  // binds its children to this scope however scopes interleave around it.
  run<T>(body: (root: string) => Promise<T>): Promise<T> {
    return scopeStorage.run(this, body, this.path)
  }
}

// The synchronous last block: process exit and signal deaths. Children that
// are somehow still alive are killed outright — recorded honestly as a
// synchronous kill, never as a drain — and each registered root is removed by
// exact path, then the journal is flushed to the file the run asked for.
function sweepSync(reason: string): void {
  for (const child of unscopedChildren) {
    try {
      child.kill("SIGKILL")
    } catch {
      // Already gone.
    }
  }
  for (const scope of [...registry.values()]) {
    scope.killChildrenSync()
    record({ kind: "cleanup_entry", root: scope.path, detail: reason })
    record({ kind: "removal_entry", root: scope.path, detail: reason })
    try {
      rmSync(scope.path, { recursive: true, force: true })
      if (existsSync(scope.path)) throw new Error(`root still present after removal: ${scope.path}`)
      registry.delete(scope.path)
      record({ kind: "completion", root: scope.path, detail: reason })
    } catch (error) {
      record({ kind: "removal_error", root: scope.path, detail: `${reason}: ${errorMessage(error)}` })
    }
  }
  const journalPath = process.env.CONCORD_FIXTURE_JOURNAL
  if (journalPath) {
    try {
      writeFileSync(journalPath, `${JSON.stringify({ journal })}\n`)
    } catch {
      // The sweep must still exit with its status.
    }
  }
}

async function terminateForSignal(signal: "SIGINT" | "SIGTERM", code: number): Promise<void> {
  if (terminating) return
  terminating = true
  // Asynchronous drain first: every owned child gets SIGTERM, a grace
  // interval, SIGKILL, and an awaited exit — including the supervised command
  // trees, whose separately grouped descendants are adopted and reaped by the
  // supervisor before it exits. Only when every drain settles does the final
  // synchronous removal-and-exit block below run.
  await Promise.allSettled([...registry.values()].map((scope) => scope.expire(`signal_${signal}`)))
  await drainChildren(unscopedChildren, "(unscoped)", `signal_${signal}`)
  sweepSync(`signal_${signal}`)
  process.exitCode = code
  process.exit(code)
}

function installProcessGuards(): void {
  if (guardsInstalled) return
  guardsInstalled = true
  // Under `bun test` the "exit" event never fires (verified on Bun 1.4.0:
  // neither passing nor failing runs execute exit handlers), so this backstop
  // covers only non-test consumers; test files end with the afterAll run-end
  // sweep instead. It kills leftover children without pretending to drain
  // them and removes the exact registered roots.
  process.on("exit", () => sweepSync("process_exit"))
  process.on("SIGINT", () => void terminateForSignal("SIGINT", 130))
  process.on("SIGTERM", () => void terminateForSignal("SIGTERM", 143))
}

// Allocate a fixture root and bind its scope to the running test: the exact
// path registers the instant mkdtemp returns, before any other await can
// strand it, the ownership marker identifies SIGKILL/OOM leftovers, and the
// bun:test hook owns expiry. The test's close() owns removal.
export async function beginOwnedFixture(prefix: string): Promise<FixtureScope> {
  const path = await mkdtemp(join(tmpdir(), prefix))
  const scope = new FixtureScope(path, prefix)
  registry.set(path, scope)
  installProcessGuards()
  onTestFinished(() => scope.expire("test_finished"))
  await Bun.write(join(path, OWNED_ROOT_MARKER), JSON.stringify({
    owner: "concord-adapter-test-fixture",
    pid: process.pid,
    prefix,
    allocated_at: new Date().toISOString(),
  }, null, 2) + "\n")
  return scope
}

// The run-end sweep for bun:test files: bun:test never runs process "exit"
// hooks, so a body that timed out and never settled keeps its root owned
// until the file's afterAll calls this. It removes exactly the registered
// roots that are still present, and retries any removal that failed.
export async function sweepPendingFixtureScopes(reason = "run_end"): Promise<void> {
  for (const scope of [...registry.values()]) {
    await scope.close(reason)
  }
}

// Release a root by path. A path this process never registered — a same-prefix
// sibling, another run's artifact root — is never touched and never reported
// as removed: unknown ownership is not successful removal.
export async function releaseFixtureRoot(root: string, _reason = "manual"): Promise<FixtureRelease> {
  const scope = registry.get(root)
  if (!scope) {
    const message = `not a root registered by this process: ${root}`
    record({ kind: "removal_error", root, detail: message })
    return { root, removed: false, errors: [message] }
  }
  return scope.close("manual_release")
}

// Attach an already-spawned child to a scope (or the scope whose body is
// running) so cleanup cancels and drains it before removal. A child spawned
// with no live scope lands in the fallback set only the process-level guards
// drain: never dropped, never misattributed.
export function trackFixtureChild(child: Bun.Subprocess, scope?: FixtureScope): void {
  const owner = scope ?? scopeStorage.getStore()
  if (owner) owner.attach(child)
  else {
    unscopedChildren.add(child)
    void child.exited.then(
      () => unscopedChildren.delete(child),
      () => unscopedChildren.delete(child),
    )
  }
}

// Spawn a fixture-owned command under the subreaper supervisor: tracked for
// cancel-and-drain cleanup, with late AbortSignal cancellation connected, and
// with separately grouped descendants adopted and reaped by the supervisor
// before this promise resolves. Refuses new work after scope expiry. Resolves
// only after the supervised command tree is fully drained, so a returned
// child is a contained child.
export async function runFixtureProcess(argv: string[], input = "", options: FixtureProcessOptions = {}): Promise<FixtureProcessResult> {
  const scope = options.scope ?? scopeStorage.getStore()
  if (scope?.expired) {
    record({ kind: "spawn_refused", root: scope.path, detail: argv[0] })
    throw new Error(`fixture scope ${scope.path} refused new work after expiry: ${argv[0]}`)
  }
  const env = { ...(options.env ?? { ...process.env }), CONCORD_FIXTURE_CWD: options.cwd ?? "" }
  const child = Bun.spawn([process.execPath, SUPERVISOR, ...argv], {
    env,
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  trackFixtureChild(child, scope)
  let abortListener: (() => void) | undefined
  if (options.signal) {
    const signal = options.signal
    abortListener = () => {
      try {
        child.kill("SIGTERM")
      } catch {
        // Already gone.
      }
    }
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
  // The supervisor reports the command tree it drained (adopted descendants,
  // kills, reaps) on stderr; record it in the journal and keep it out of the
  // caller's stderr so existing assertions on command output stay stable.
  let visibleStderr = stderr
  for (const line of stderr.split("\n")) {
    if (!line.startsWith("@@concord-supervisor ")) continue
    visibleStderr = visibleStderr.replace(`${line}\n`, "").replace(line, "")
    try {
      const report = JSON.parse(line.slice("@@concord-supervisor ".length)) as FixtureScopeDriverReport
      record({ kind: "descendants_drained", root: scope?.path ?? "(unscoped)", detail: JSON.stringify(report) })
    } catch {
      // A malformed report line never breaks the command result.
    }
  }
  return { exitCode, stdout, stderr: visibleStderr, pid: child.pid }
}
