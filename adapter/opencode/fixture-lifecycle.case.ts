// Lifecycle-case driver for the fixture-lifecycle regression matrix. The
// parent spawns this file once per case as a REAL `bun test` child run (the
// scenario arrives in CONCORD_LIFECYCLE_CASE), so bun:test's own timeout,
// onTestFinished hook, run end, and exit statuses are the ones exercised —
// expiration is never simulated. Each case allocates a real fixture root and
// real fixture-owned children, then ends along one lifecycle path. The case
// composes { scenario, synopsis, findings, journal } into
// CONCORD_LIFECYCLE_REPORT at run end; the signal paths flush the bare
// journal through the lifecycle's synchronous removal-and-exit block. No
// model call happens anywhere in this matrix.
//
// Cases and their exit statuses:
//   success                    explicit close, exit 0
//   assertion-failure          close through the failure path, exit 1
//   timeout-resumption         real 50ms timeout; body resumes during the
//                              hook's drain, writes, is refused new spawns,
//                              settles, and removal happens at settlement
//   timeout-nosettle           real 50ms timeout; body never settles; the
//                              run-end sweep removes the root, exit 1
//   teardown-exception         the body's finally throws before close; the
//                              run-end sweep removes the root, exit 1
//   cooperative-abort          AbortSignal cancels a supervised child, exit 0
//   cleanup-failure-passing    body passes, removal fails: the run fails, exit 1
//   cleanup-failure-failing    body fails, removal fails: the ORIGINAL error
//                              is preserved and the removal is visible, exit 1
//   overlapping-owners         two live scopes; a child bound to the first
//                              survives the second scope's release, exit 0
//   unknown-ownership          releasing an unregistered root reports failure
//                              and touches nothing, exit 0
//   grouped-writer             a separately grouped, SIGTERM-ignoring writing
//                              descendant is adopted, killed, and reaped by
//                              the supervisor before removal, exit 0
//   sigint                     async drain, then sync removal-and-exit, 130
//   sigterm                    async drain, then sync removal-and-exit, 143
//   sigkill                    parent kills this process: no in-process
//                              cleanup runs; the leftover is identifiable
import { afterAll, test } from "bun:test"
import { chmod, mkdtemp } from "node:fs/promises"
import { existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { beginOwnedFixture, fixtureJournal, releaseFixtureRoot, runFixtureProcess, sweepPendingFixtureScopes, trackFixtureChild, type FixtureJournalEntry } from "./fixture-lifecycle"

const WRITER = join(import.meta.dir, "fixture-lifecycle.writer.ts")
const scenario = process.env.CONCORD_LIFECYCLE_CASE ?? ""
const token = process.env.CONCORD_LIFECYCLE_TOKEN ?? "case"
const reportPath = process.env.CONCORD_LIFECYCLE_REPORT ?? ""
const prefix = `concord-lifecycle-${token}-${scenario}-`
const TEST_TIMEOUTS: Record<string, number> = { "timeout-resumption": 50, "timeout-nosettle": 50 }

const findings: Record<string, unknown> = { scenario }
const synopsis: Record<string, unknown> = { scenario, token }

function alive(pid: number | undefined): boolean {
  if (!pid) return false
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

async function ignoringChild(extra: string[] = []): Promise<Bun.Subprocess> {
  const script = `process.on("SIGTERM", () => {}); process.on("SIGINT", () => {}); setInterval(() => {}, 60000); ${extra.join(";")}`
  return Bun.spawn([process.execPath, "-e", script], { stdin: "ignore", stdout: "ignore", stderr: "ignore" })
}

const caseBody: Record<string, () => Promise<void>> = {
  async success() {
    const scope = await beginOwnedFixture(prefix)
    await scope.run(async () => {
      const quick = await runFixtureProcess([process.execPath, "-e", "process.stdout.write('ok')"])
      if (quick.exitCode !== 0 || quick.stdout !== "ok") throw new Error(`quick child failed: ${quick.exitCode} ${quick.stderr}`)
      synopsis.root = scope.path
      synopsis.childPid = quick.pid
    })
    await scope.close("success_case")
    scope.ensureReleased()
  },

  async "assertion-failure"() {
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      try {
        throw new Error("synthetic assertion failure")
      } finally {
        // The finally still runs on this path, as it does under bun:test —
        // but through the scope's close, and the original error propagates.
        await scope.close("assertion_failure")
      }
    })
    scope.ensureReleased()
  },

  async "timeout-resumption"() {
    // The exact no_ship counterexample: a real bun:test 50ms timeout, a body
    // that resumes at 150ms — during the expiry hook's drain — writes into
    // the root, is refused new spawns, and settles. The root must still exist
    // at resumption, and removal must happen at settlement, never at expiry.
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async (root) => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      await new Promise((resolve) => setTimeout(resolve, 150))
      findings.resumedAfterExpiry = true
      findings.rootPresentAtResumption = existsSync(root)
      await Bun.write(join(root, "late-write.txt"), "body resumed after expiry")
      findings.wroteAfterExpiry = existsSync(join(root, "late-write.txt"))
      let refused = ""
      try {
        await runFixtureProcess([process.execPath, "-e", "process.exit(0)"])
      } catch (error) {
        refused = error instanceof Error ? error.message : String(error)
      }
      findings.spawnRefused = refused.includes("refused new work")
      findings.expiryJournaledBeforeResumption = fixtureJournal().some((entry) => entry.kind === "child_cancel" && entry.root === root)
    })
    const release = await scope.close("body_settled")
    findings.settledRemovalReason = "body_settled"
    findings.rootRemovedAtSettlement = release.removed && !existsSync(scope.path)
    scope.ensureReleased()
  },

  async "timeout-nosettle"() {
    // A timed-out body that never settles: the hook drains at expiry, the
    // root stays owned, and the run-end sweep removes it before the process
    // exits (bun:test runs no exit hooks).
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    void scope.run(async () => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      findings.allocated = true
      await new Promise<void>(() => {})
    })
    await new Promise<void>(() => {})
  },

  async "teardown-exception"() {
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      try {
        findings.bodyReached = true
      } finally {
        // The teardown itself throws before any close: the run-end sweep owns
        // the removal from here.
        throw new Error("synthetic teardown exception before release")
      }
    })
    scope.ensureReleased()
  },

  async "cooperative-abort"() {
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      const controller = new AbortController()
      const late = runFixtureProcess([process.execPath, "-e", "setTimeout(() => {}, 30000)"], "", { signal: controller.signal })
      await new Promise((resolve) => setTimeout(resolve, 120))
      controller.abort()
      const aborted = await late
      findings.abortedExitCode = aborted.exitCode
      if (aborted.exitCode === 0) throw new Error("aborted child must not exit cleanly")
    })
    await scope.close("cooperative_abort")
    scope.ensureReleased()
  },

  async "cleanup-failure-passing"() {
    // A cleanup failure after an otherwise passing body must fail the run:
    // ensureReleased throws, the removal error is visible, the root and its
    // collected evidence stay in place.
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async (root) => {
      await Bun.write(join(root, "evidence.txt"), "collected evidence\n")
      await chmod(root, 0o500)
      findings.evidenceWritten = true
    })
    const release = await scope.close("cleanup_failure_passing")
    findings.removalFailed = !release.removed
    scope.ensureReleased()
    findings.ensureReleasedThrew = false
  },

  async "cleanup-failure-failing"() {
    // A failing body plus a failing removal: the ORIGINAL body error is the
    // reported failure; the removal error is visible beside it.
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async (root) => {
      await Bun.write(join(root, "evidence.txt"), "collected evidence\n")
      await chmod(root, 0o500)
      try {
        throw new Error("original body failure")
      } finally {
        await scope.close("cleanup_failure_failing")
      }
    })
    scope.ensureReleased()
  },

  async "overlapping-owners"() {
    // The exact no_ship counterexample: allocate A, then B; a child of A's
    // body (cwd A) must be owned by A — releasing B must not kill it, and
    // releasing A must.
    const scopeA = await beginOwnedFixture(`${token}-overlapping-a-${scenario}-`)
    const scopeB = await beginOwnedFixture(`${token}-overlapping-b-${scenario}-`)
    synopsis.root = scopeA.path
    synopsis.rootB = scopeB.path
    await scopeA.run(async (rootA) => {
      const tracked = Bun.spawn([process.execPath, "-e", "setTimeout(() => {}, 30000)"], { cwd: rootA, stdin: "ignore", stdout: "ignore", stderr: "ignore" })
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      await new Promise((resolve) => setTimeout(resolve, 60))
      const releaseB = await scopeB.close("overlapping_b")
      findings.childAliveAfterBRelease = alive(tracked.pid)
      findings.rootBRemoved = releaseB.removed && !existsSync(scopeB.path)
      findings.journalChildrenOnlyUnderA = fixtureJournal().every((entry) => entry.root !== scopeB.path || (entry.kind !== "child_cancel" && entry.kind !== "child_drained"))
      const releaseA = await scopeA.close("overlapping_a")
      findings.childDeadAfterARelease = !alive(tracked.pid)
      findings.rootARemoved = releaseA.removed && !existsSync(scopeA.path)
    })
    scopeA.ensureReleased()
    scopeB.ensureReleased()
  },

  async "unknown-ownership"() {
    // Unknown ownership must never report successful removal: the
    // unregistered sibling is refused, untouched, and still there.
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const sibling = String(findings.sibling ?? "")
      const release = await releaseFixtureRoot(sibling)
      findings.unknownRefused = release.removed === false && release.errors.length > 0
      findings.siblingUntouched = existsSync(join(sibling, "unregistered.txt"))
    })
    await scope.close("unknown_ownership")
    scope.ensureReleased()
  },

  async "grouped-writer"() {
    // Descendant containment: the supervised command moves into its own
    // process group and leaves behind a separately grouped descendant that
    // ignores SIGTERM and keeps writing. The supervisor must adopt, kill,
    // and reap it — before the root is removed.
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const heartbeat = String(findings.heartbeatPath ?? "")
      const run = await runFixtureProcess([process.execPath, WRITER, "grouped-parent", heartbeat])
      findings.writerExitCode = run.exitCode
      findings.supervisorPid = run.pid
      const drained = fixtureJournal().find((entry) => entry.kind === "descendants_drained" && entry.root === scope.path)
      findings.descendantsDrained = drained?.detail
    })
    await scope.close("grouped_writer")
    scope.ensureReleased()
  },

  async "sigint"() {
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      const heartbeat = String(findings.heartbeatPath ?? "")
      void runFixtureProcess([process.execPath, WRITER, "grouped-parent", heartbeat])
      await new Promise((resolve) => setTimeout(resolve, 120))
      process.kill(process.pid, "SIGINT")
      await new Promise<void>(() => {})
    })
  },

  async "sigterm"() {
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const tracked = await ignoringChild()
      trackFixtureChild(tracked)
      synopsis.childPid = tracked.pid
      const heartbeat = String(findings.heartbeatPath ?? "")
      void runFixtureProcess([process.execPath, WRITER, "grouped-parent", heartbeat])
      await new Promise((resolve) => setTimeout(resolve, 120))
      process.kill(process.pid, "SIGTERM")
      await new Promise<void>(() => {})
    })
  },

  async "sigkill"() {
    // SIGKILL runs no handler: the parent kills this process after the root
    // appears. This leftover is exactly the case the module does not claim to
    // reclaim; the ownership marker identifies it. Only supervised children
    // run here — the supervisor notices the dead parent and drains its own
    // command tree even though this process can run nothing.
    const scope = await beginOwnedFixture(prefix)
    synopsis.root = scope.path
    await scope.run(async () => {
      const heartbeat = String(findings.heartbeatPath ?? "")
      void runFixtureProcess([process.execPath, WRITER, "grouped-parent", heartbeat])
      await new Promise((resolve) => setTimeout(resolve, 120))
      findings.ready = true
      await new Promise<void>(() => {})
    })
  },
}

async function main(): Promise<void> {
  if (!caseBody[scenario]) throw new Error(`unknown scenario: ${scenario || "(none)"}`)
  // An unregistered same-prefix sibling: every cleanup path must remove the
  // registered root only, never a wildcard prefix match. The unknown-
  // ownership and writer cases also use it (refusal target, heartbeat file).
  const sibling = await mkdtemp(join(tmpdir(), prefix))
  await Bun.write(join(sibling, "unregistered.txt"), "not owned by this run\n")
  findings.sibling = sibling
  findings.heartbeatPath = join(sibling, "heartbeat.txt")
  await caseBody[scenario]()
}

afterAll(async () => {
  // The run-end sweep: bun:test runs no exit hooks, so scopes whose bodies
  // never settled (or whose removal failed) are settled here, before the
  // process exits.
  await sweepPendingFixtureScopes("run_end")
  if (reportPath) {
    await Bun.write(reportPath, `${JSON.stringify({ scenario, synopsis, findings, journal: fixtureJournal() as FixtureJournalEntry[] })}\n`)
  }
})

test(scenario || "unconfigured", async () => {
  await main()
}, TEST_TIMEOUTS[scenario] ?? 60_000)
