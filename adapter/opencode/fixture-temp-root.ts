// Fixture-temp-root helper for case files that run under the external owner
// (fixture-root-owner.py). The owner — one plain Python process per fixture
// suite, outside bun:test — allocates one short private run root recorded
// with an ownership marker and nonce, launches the case file as a child
// `bun test` run, and removes that exact root only after this process can no
// longer write and every owned descendant is killed and reaped (the kernel's
// own ECHILD boundary). Nothing here removes anything: removal belongs to
// the external owner alone, so there is no in-process scope registry, no
// file-level afterAll sweep, no signal sweep, and no per-command supervisor.
//
// All fixture allocations go through fixtureTempRoot(), which places them
// under the owner's run root and nowhere else. The global TMPDIR is NOT
// redirected, so go-build temp roots and other foreign temp directories stay
// outside this run's cleanup. The run-root inputs ride TEST_CONCORD_* keys —
// the repository's test-child convention — because the test preload strips
// every live CONCORD_* key in each bun:test child, including this one. Commands spawned through runFixtureProcess run
// directly (production ExecGitRunner stays unchanged); their separately
// grouped descendants are contained by the owner's subreaper drain at run
// end, and a late AbortSignal stays connected to the spawned child.
import { mkdtemp } from "node:fs/promises"
import { readFileSync } from "node:fs"
import { join } from "node:path"

export const OWNED_ROOT_MARKER = ".concord-owned-fixture-root.json"

let validatedRoot: string | undefined

// The run root the external owner allocated for this suite, proven by the
// ownership marker's nonce. Without it — a standalone `bun test` of a case
// file — this throws and the run refuses to start: case files never execute
// unowned.
export function ownedFixtureRunRoot(): string {
  if (validatedRoot) return validatedRoot
  const root = process.env.TEST_CONCORD_FIXTURE_RUN_ROOT
  const nonce = process.env.TEST_CONCORD_FIXTURE_RUN_NONCE
  if (!root || !nonce) {
    throw new Error("refusing unowned execution: no owned fixture run root; run this suite through its .test.ts launcher, which starts fixture-root-owner.py")
  }
  const marker = JSON.parse(readFileSync(join(root, OWNED_ROOT_MARKER), "utf8")) as { nonce?: string }
  if (marker.nonce !== nonce) {
    throw new Error(`refusing execution: fixture run root ${root} does not carry this run's nonce`)
  }
  validatedRoot = root
  return root
}

// Module-load refusal for case files: call at the top of the file so an
// unowned invocation fails before any test body or fixture allocation runs.
export function requireOwnedFixtureRun(): void {
  ownedFixtureRunRoot()
}

// One fixture allocation under the owner's run root. The exact path registers
// the instant mkdtemp returns; removal belongs to the owner alone, after this
// process cannot write again.
export async function fixtureTempRoot(tag = "fx"): Promise<string> {
  return mkdtemp(join(ownedFixtureRunRoot(), `${tag}-`))
}

export interface FixtureProcessOptions {
  cwd?: string
  env?: Record<string, string | undefined>
  // Connected to the spawned child for its whole life: an abort that fires
  // after spawn terminates the child immediately.
  signal?: AbortSignal
}

export interface FixtureProcessResult {
  exitCode: number
  stdout: string
  stderr: string
  pid: number
}

const ABORT_GRACE_MS = 800

// Run one fixture-owned command directly: stdin fed, output captured, cwd and
// env as given, and a late abort still reaching the child through SIGTERM
// with a SIGKILL escalation. The returned promise settles when the direct
// child exits; descendants it leaves behind are the owner's to drain — never
// removed early, never raced with a per-root rm.
export async function runFixtureProcess(argv: string[], input = "", options: FixtureProcessOptions = {}): Promise<FixtureProcessResult> {
  const child = Bun.spawn(argv, {
    cwd: options.cwd,
    env: options.env ?? { ...process.env },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  let abortTimer: ReturnType<typeof setTimeout> | undefined
  const abort = (): void => {
    try {
      child.kill("SIGTERM")
    } catch {
      // Already gone.
    }
    abortTimer = setTimeout(() => {
      try {
        child.kill("SIGKILL")
      } catch {
        // Already gone.
      }
    }, ABORT_GRACE_MS)
    abortTimer.unref?.()
  }
  if (options.signal) {
    if (options.signal.aborted) abort()
    else options.signal.addEventListener("abort", abort, { once: true })
  }
  try {
    await child.stdin.write(input)
    await child.stdin.end()
  } catch {
    // The child already exited or was cancelled; its status still resolves.
  }
  const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  if (abortTimer) clearTimeout(abortTimer)
  if (options.signal) options.signal.removeEventListener("abort", abort)
  return { exitCode, stdout, stderr, pid: child.pid }
}
