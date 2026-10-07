// Owner-level lifecycle cases. The external owner (fixture-root-owner.py)
// spawns this file as a REAL `bun test` child run with the scenario in
// CONCORD_OWNER_CASE, so bun:test's own timeouts, hooks, run end, and exit
// statuses are the exercised ones — nothing is simulated, and this file
// refuses unowned standalone execution. Each case allocates real fixture
// roots and real fixture-owned descendants under the owner's run root, then
// ends along one lifecycle path; the owner's journal (its @@concord-owner
// events) and this file's findings report (written OUTSIDE the disposable
// root) are what the matrix asserts on. No model call happens anywhere here.
//
// Cases and their expected ends:
//   success                 pass; owner removes the root, exit 0
//   assertion-failure       synthetic failure, exit 1; root still removed
//   timeout-resumption      real 50ms timeout; the body resumes at 200ms
//                           during another awaited hook, writes and recreates
//                           a directory under the root, then the run ends and
//                           the owner removes the root — after the process
//                           cannot write again
//   timeout-nosettle        real 50ms timeout; the body never settles
//   teardown-exception      the body's finally throws, exit 1
//   cooperative-abort       a late AbortSignal cancels a fixture command; a
//                           SIGTERM-ignoring child stays for the owner
//   cleanup-failure-passing the suite passes but chmods the run root away;
//                           the owner's removal fails: exit nonzero,
//                           removal_error visible, evidence retained
//   cleanup-failure-failing the suite fails too: exit 1 preserved with the
//                           removal error still visible
//   chain-writer            a separately sessioned, SIGTERM-ignoring
//                           six-link writer chain outlives the passing suite
//   daemon-writer           a double-forked daemonized writer outlives the
//                           passing suite
//   sleepy                  allocates a root and a writer chain, then sleeps;
//                           the matrix drives the owner (SIGINT, SIGTERM,
//                           EOF, SIGKILL) while this run is live
import { afterAll, test } from "bun:test"
import { chmod } from "node:fs/promises"
import { existsSync } from "node:fs"
import { join } from "node:path"
import { fixtureTempRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"

requireOwnedFixtureRun()

const WRITER = join(import.meta.dir, "fixture-owner.writer.ts")
const scenario = process.env.CONCORD_OWNER_CASE ?? ""
const reportPath = process.env.CONCORD_OWNER_REPORT ?? ""
const pulsePath = process.env.CONCORD_OWNER_PULSE ?? ""
const findings: Record<string, unknown> = { scenario }
const TEST_TIMEOUTS: Record<string, number> = { "timeout-resumption": 50, "timeout-nosettle": 50, sleepy: 60_000 }

function spawnChain(links: number): void {
  const child = Bun.spawn([process.execPath, WRITER, "chain", pulsePath, String(links - 1)], {
    stdin: "ignore",
    stdout: "ignore",
    stderr: "ignore",
  })
  child.unref()
  findings.chainRootPid = child.pid
}

const caseBody: Record<string, () => Promise<void>> = {
  async success() {
    const root = await fixtureTempRoot("success")
    findings.root = root
    await Bun.write(join(root, "evidence.txt"), "collected evidence\n")
    const quick = await runFixtureProcess([process.execPath, "-e", "process.stdout.write('ok')"])
    if (quick.exitCode !== 0 || quick.stdout !== "ok") throw new Error(`quick child failed: ${quick.exitCode} ${quick.stderr}`)
    findings.underOwnedRoot = root.startsWith(process.env.CONCORD_FIXTURE_RUN_ROOT ?? "")
  },

  async "assertion-failure"() {
    const root = await fixtureTempRoot("failing")
    findings.root = root
    throw new Error("synthetic assertion failure")
  },

  async "timeout-resumption"() {
    // The regression shape: a real bun:test 50ms timeout, a body
    // that resumes at 200ms — during another awaited hook — and writes into
    // the root. The root must still exist at resumption, and removal happens
    // only after this process can no longer write.
    const root = await fixtureTempRoot("resume")
    findings.root = root
    await new Promise((resolve) => setTimeout(resolve, 200))
    findings.resumedAfterTimeout = true
    findings.rootPresentAtResumption = existsSync(root)
    await Bun.write(join(root, "late-write.txt"), "body resumed after timeout")
    await Bun.write(join(root, "recreated", "probe.txt"), "recreated directory")
    findings.wroteAfterTimeout = existsSync(join(root, "late-write.txt"))
  },

  async "timeout-nosettle"() {
    const root = await fixtureTempRoot("nosettle")
    findings.root = root
    findings.allocated = true
    await new Promise<void>(() => {})
  },

  async "teardown-exception"() {
    const root = await fixtureTempRoot("teardown")
    findings.root = root
    try {
      findings.bodyReached = true
    } finally {
      throw new Error("synthetic teardown exception before release")
    }
  },

  async "cooperative-abort"() {
    const root = await fixtureTempRoot("abort")
    findings.root = root
    const controller = new AbortController()
    const late = runFixtureProcess([process.execPath, "-e", "setTimeout(() => {}, 30000)"], "", { signal: controller.signal })
    await new Promise((resolve) => setTimeout(resolve, 120))
    controller.abort()
    const aborted = await late
    findings.abortedExitCode = aborted.exitCode
    if (aborted.exitCode === 0) throw new Error("aborted child must not exit cleanly")
    // A SIGTERM-ignoring child the abort cannot reach: containment is the
    // owner's drain, never a per-command claim.
    const ignoring = Bun.spawn([process.execPath, "-e", "process.on('SIGTERM', () => {}); setInterval(() => {}, 60000)"], {
      stdin: "ignore",
      stdout: "ignore",
      stderr: "ignore",
    })
    ignoring.unref()
    findings.ignoringPid = ignoring.pid
  },

  async "cleanup-failure-passing"() {
    const root = await fixtureTempRoot("retain")
    findings.root = root
    await Bun.write(join(root, "evidence.txt"), "collected evidence\n")
    findings.evidenceWritten = true
    // Make the owner's removal fail: no write permission on the run root.
    await chmod(process.env.CONCORD_FIXTURE_RUN_ROOT ?? root, 0o500)
  },

  async "cleanup-failure-failing"() {
    const root = await fixtureTempRoot("retainfail")
    findings.root = root
    await Bun.write(join(root, "evidence.txt"), "collected evidence\n")
    await chmod(process.env.CONCORD_FIXTURE_RUN_ROOT ?? root, 0o500)
    throw new Error("original body failure")
  },

  async "chain-writer"() {
    const root = await fixtureTempRoot("chain")
    findings.root = root
    spawnChain(6)
    await new Promise((resolve) => setTimeout(resolve, 150))
  },

  async "daemon-writer"() {
    const root = await fixtureTempRoot("daemon")
    findings.root = root
    const daemon = Bun.spawn([process.execPath, WRITER, "daemon", pulsePath, "0"], {
      stdin: "ignore",
      stdout: "ignore",
      stderr: "ignore",
    })
    daemon.unref()
    findings.daemonPid = daemon.pid
    // Let the double fork land so the writer is orphaned — and adopted by
    // the owner — while this suite still runs.
    await new Promise((resolve) => setTimeout(resolve, 400))
  },

  async sleepy() {
    const root = await fixtureTempRoot("sleepy")
    findings.root = root
    spawnChain(6)
    await new Promise((resolve) => setTimeout(resolve, 150))
    findings.ready = true
    await new Promise<void>(() => {})
  },
}

test(scenario || "unconfigured", async () => {
  if (!caseBody[scenario]) throw new Error(`unknown scenario: ${scenario || "(none)"}`)
  await caseBody[scenario]()
}, TEST_TIMEOUTS[scenario] ?? 60_000)

// A later awaited hook for the resumption case: it runs while the timed-out
// body's 200ms timer is still pending — a live writer in the root exactly
// when removal must not yet happen.
test("later awaited hook", async () => {
  await new Promise((resolve) => setTimeout(resolve, 400))
})

afterAll(async () => {
  findings.stillAliveAtRunEnd = existsSync(String(findings.root ?? ""))
  if (reportPath) {
    await Bun.write(reportPath, `${JSON.stringify({ scenario, findings })}\n`)
  }
})
