// Regression matrix for the callback-bound fixture lifecycle
// (fixture-lifecycle.ts) and the subreaper supervisor (fixture-supervisor.ts).
// Every case runs as a REAL `bun test` child — bun:test's own timeout, its
// onTestFinished hook, and its exit statuses are the exercised ones — with a
// per-matrix token so only this run's exact registered roots are inventoried
// and concurrent matrices or foreign sentinels can never contaminate the
// result. Every lifecycle case runs three consecutive times and asserts zero
// remaining owned fixture roots, an unregistered same-prefix sibling and a
// concurrent foreign-run sentinel untouched, original failures preserved,
// visible removal errors, drain-before-removal ordering, and descendant
// containment. SIGKILL — which can run no in-process cleanup — leaves an
// identifiable, confined leftover rather than a reclamation claim. No model
// call happens anywhere in this file.
import { expect, test } from "bun:test"
import { chmod, mkdtemp, readdir, readFile, rm, stat } from "node:fs/promises"
import { existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { OWNED_ROOT_MARKER, type FixtureJournalEntry } from "./fixture-lifecycle"

const CASE_FILE = join(import.meta.dir, "fixture-lifecycle.case.ts")
const TOKEN = `m${Date.now().toString(36)}${process.pid.toString(36)}`
const OWNED_PREFIX = `concord-lifecycle-${TOKEN}-`
const FOREIGN_PREFIX = "concord-lifecycle-foreign-"

const SIGNAL_NUMBERS: Record<string, number> = { SIGINT: 2, SIGTERM: 15, SIGKILL: 9 }

interface CaseReport {
  scenario: string
  synopsis: { root?: string; rootB?: string; childPid?: number }
  findings: Record<string, unknown>
  journal: FixtureJournalEntry[]
}

interface CaseResult {
  scenario: string
  exitCode: number
  stdout: string
  stderr: string
  report: CaseReport | undefined
  journal: FixtureJournalEntry[]
}

// The failing and passing exit statuses the matrix must preserve: a cleanup
// path never converts a failing run into a passing one, and a cleanup failure
// after a passing body fails the run.
const EXPECTED_EXIT: Record<string, number> = {
  success: 0,
  "assertion-failure": 1,
  "timeout-resumption": 1,
  "timeout-nosettle": 1,
  "teardown-exception": 1,
  "cooperative-abort": 0,
  "cleanup-failure-passing": 1,
  "cleanup-failure-failing": 1,
  "overlapping-owners": 0,
  "unknown-ownership": 0,
  "grouped-writer": 0,
  sigint: 130,
  sigterm: 143,
}
// Cases whose root is intentionally retained (removal failed): the leftover
// and its collected evidence stay in place, visibly.
const RETAINING_SCENARIOS = new Set(["cleanup-failure-passing", "cleanup-failure-failing"])
const REMOVING_SCENARIOS = Object.keys(EXPECTED_EXIT).filter((scenario) => !RETAINING_SCENARIOS.has(scenario))

async function pathExists(path: string): Promise<boolean> {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}

async function runCase(scenario: string, workDir: string): Promise<CaseResult> {
  const reportPath = join(workDir, `${scenario}.report.json`)
  const journalPath = join(workDir, `${scenario}.journal.json`)
  const child = Bun.spawn([process.execPath, "test", CASE_FILE], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      CONCORD_LIFECYCLE_CASE: scenario,
      CONCORD_LIFECYCLE_TOKEN: TOKEN,
      CONCORD_LIFECYCLE_REPORT: reportPath,
      CONCORD_FIXTURE_JOURNAL: journalPath,
    },
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  })
  const [stdout, stderr, exited] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  const exitCode = exited ?? (child.signalCode ? 128 + (SIGNAL_NUMBERS[child.signalCode] ?? 0) : -1)
  let report: CaseReport | undefined
  let journal: FixtureJournalEntry[] = []
  if (await pathExists(reportPath)) {
    report = JSON.parse(await readFile(reportPath, "utf8")) as CaseReport
    journal = report.journal
  } else if (await pathExists(journalPath)) {
    journal = (JSON.parse(await readFile(journalPath, "utf8")) as { journal: FixtureJournalEntry[] }).journal
  }
  return { scenario, exitCode, stdout, stderr, report, journal }
}

// The unregistered same-prefix sibling the case creates: found by its marker
// content, never by trusting a path shape.
async function findCaseSibling(scenario: string): Promise<string> {
  const names = await readdir(tmpdir())
  for (const name of names) {
    if (!name.startsWith(`${OWNED_PREFIX}${scenario}-`)) continue
    const candidate = join(tmpdir(), name)
    if (existsSync(join(candidate, "unregistered.txt"))) return candidate
  }
  throw new Error(`no unregistered sibling found for ${scenario}`)
}

async function expectProcessGone(pid: number | undefined, label: string): Promise<void> {
  if (!pid) return
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      process.kill(pid, 0)
    } catch {
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  throw new Error(`process ${pid} (${label}) survived the cleanup`)
}

interface Heartbeat {
  lines: number
  lastPid: number | undefined
}

async function readHeartbeat(sibling: string): Promise<Heartbeat> {
  const path = join(sibling, "heartbeat.txt")
  if (!existsSync(path)) return { lines: 0, lastPid: undefined }
  const text = await readFile(path, "utf8")
  const lines = text.trim().split("\n").filter(Boolean)
  const lastPid = lines.length > 0 ? Number(lines[lines.length - 1].split(" ")[0]) : undefined
  return { lines: lines.length, lastPid }
}

// A drained writer stops writing: the heartbeat must be stable across a
// settle window after the case process is gone. settleMs lets the sigkill
// case wait out the supervisor's asynchronous post-mortem drain first.
async function expectHeartbeatStable(sibling: string, label: string, settleMs = 0): Promise<Heartbeat> {
  if (settleMs > 0) await new Promise((resolve) => setTimeout(resolve, settleMs))
  const first = await readHeartbeat(sibling)
  await new Promise((resolve) => setTimeout(resolve, 350))
  const second = await readHeartbeat(sibling)
  expect(second.lines, `${label}: heartbeat kept growing after cleanup (${first.lines} -> ${second.lines})`).toBe(first.lines)
  return second
}

async function digestTree(root: string): Promise<string> {
  const names = await readdir(root)
  const parts: string[] = []
  for (const name of names.sort()) parts.push(`${name}:${await readFile(join(root, name), "utf8")}`)
  return parts.join("|")
}

async function mkdtempForeign(): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), FOREIGN_PREFIX))
  await Bun.write(join(root, "active-run.txt"), "another active run owns this root\n")
  return root
}

function journalIndexOf(journal: FixtureJournalEntry[], kind: FixtureJournalEntry["kind"], root?: string): number {
  return journal.findIndex((entry) => entry.kind === kind && (root === undefined || entry.root === root))
}

async function assertScenarioSpecific(result: CaseResult, sibling: string, label: string): Promise<void> {
  const findings = result.report?.findings ?? {}
  const finding = (name: string): unknown => {
    expect(findings[name], `${label}: finding ${name} missing (findings: ${JSON.stringify(findings)})`).toBeDefined()
    return findings[name]
  }
  switch (result.scenario) {
    case "success": {
      expect(result.journal.some((entry) => entry.kind === "removal_error")).toBe(false)
      break
    }
    case "assertion-failure": {
      expect(result.stderr).toContain("synthetic assertion failure")
      break
    }
    case "timeout-resumption": {
      // The no_ship counterexample, promoted: the body really resumed after a
      // real timeout — the root was still there to write into, new spawns
      // were refused, and removal happened at settlement.
      expect(finding("resumedAfterExpiry")).toBe(true)
      expect(finding("rootPresentAtResumption")).toBe(true)
      expect(finding("wroteAfterExpiry")).toBe(true)
      expect(finding("spawnRefused")).toBe(true)
      expect(finding("expiryJournaledBeforeResumption")).toBe(true)
      expect(finding("rootRemovedAtSettlement")).toBe(true)
      const root = result.report?.synopsis.root as string
      expect(journalIndexOf(result.journal, "spawn_refused", root)).toBeGreaterThanOrEqual(0)
      const completion = result.journal.find((entry) => entry.kind === "completion" && entry.root === root)
      expect(completion?.detail).toBe("body_settled")
      break
    }
    case "timeout-nosettle": {
      expect(finding("allocated")).toBe(true)
      const completion = result.journal.find((entry) => entry.kind === "completion")
      expect(completion?.detail).toBe("run_end")
      break
    }
    case "teardown-exception": {
      expect(result.stderr).toContain("synthetic teardown exception")
      const completion = result.journal.find((entry) => entry.kind === "completion")
      expect(completion?.detail).toBe("run_end")
      break
    }
    case "cooperative-abort": {
      expect(Number(finding("abortedExitCode"))).not.toBe(0)
      const root = result.report?.synopsis.root as string
      expect(journalIndexOf(result.journal, "child_cancel", root)).toBeGreaterThanOrEqual(0)
      expect(journalIndexOf(result.journal, "child_drained", root)).toBeGreaterThanOrEqual(0)
      break
    }
    case "cleanup-failure-passing": {
      expect(finding("removalFailed")).toBe(true)
      expect(findings.ensureReleasedThrew).toBeUndefined()
      expect(result.stderr).toContain("removal_error")
      break
    }
    case "cleanup-failure-failing": {
      expect(result.stderr).toContain("original body failure")
      expect(result.stderr).toContain("removal_error")
      break
    }
    case "overlapping-owners": {
      expect(finding("childAliveAfterBRelease")).toBe(true)
      expect(finding("rootBRemoved")).toBe(true)
      expect(finding("journalChildrenOnlyUnderA")).toBe(true)
      expect(finding("childDeadAfterARelease")).toBe(true)
      expect(finding("rootARemoved")).toBe(true)
      const rootB = result.report?.synopsis.rootB as string
      expect(journalIndexOf(result.journal, "child_cancel", rootB)).toBe(-1)
      break
    }
    case "unknown-ownership": {
      expect(finding("unknownRefused")).toBe(true)
      expect(finding("siblingUntouched")).toBe(true)
      break
    }
    case "grouped-writer": {
      // Descendant containment: the separately grouped, SIGTERM-ignoring
      // writer was adopted, killed, and reaped by the subreaper supervisor
      // before the root was removed.
      expect(Number(finding("writerExitCode"))).toBe(0)
      const drained = result.journal.find((entry) => entry.kind === "descendants_drained")
      expect(drained).toBeDefined()
      const report = JSON.parse(drained?.detail ?? "{}") as { subreaper?: boolean; killed?: number[]; reaped?: number[] }
      expect(report.subreaper, "supervisor did not install the subreaper").toBe(true)
      expect((report.killed ?? []).length).toBeGreaterThan(0)
      expect((report.reaped ?? []).length).toBeGreaterThan(0)
      const heartbeat = await expectHeartbeatStable(sibling, label)
      expect(heartbeat.lastPid, `${label}: no writer heartbeat to inspect`).toBeDefined()
      expect(report.killed).toContain(heartbeat.lastPid)
      expect(report.reaped).toContain(heartbeat.lastPid)
      await expectProcessGone(heartbeat.lastPid, `${label} grouped writer`)
      const root = result.report?.synopsis.root as string
      expect(journalIndexOf(result.journal, "descendants_drained", root)).toBeLessThan(journalIndexOf(result.journal, "completion", root))
      break
    }
    case "sigint":
    case "sigterm": {
      // Drain order: children cancelled and drained BEFORE the synchronous
      // removal-and-exit block removed the root.
      const root = result.journal.find((entry) => entry.kind === "removal_entry")?.root as string
      expect(root).toBeDefined()
      expect(journalIndexOf(result.journal, "child_cancel", root)).toBeLessThan(journalIndexOf(result.journal, "removal_entry", root))
      expect(journalIndexOf(result.journal, "child_drained", root)).toBeLessThan(journalIndexOf(result.journal, "removal_entry", root))
      expect(journalIndexOf(result.journal, "completion", root)).toBeGreaterThanOrEqual(0)
      const heartbeat = await expectHeartbeatStable(sibling, label)
      await expectProcessGone(heartbeat.lastPid, `${label} signal-drained writer`)
      await expectProcessGone(result.report?.synopsis.childPid, `${label} signal-drained child`)
      break
    }
  }
}

test("every lifecycle case holds its ownership across three consecutive real bun-test runs", async () => {
  const sentinel = await mkdtempForeign()
  const sentinelBefore = await digestTree(sentinel)
  const workDir = await mkdtemp(join(tmpdir(), `${OWNED_PREFIX}work-`))
  try {
    for (let run = 1; run <= 3; run++) {
      for (const scenario of Object.keys(EXPECTED_EXIT)) {
        const result = await runCase(scenario, workDir)
        const label = `${scenario} run ${run}`
        expect(result.exitCode, `${label} stderr: ${result.stderr}`).toBe(EXPECTED_EXIT[scenario])
        // The unregistered same-prefix sibling proves removal stays scoped to
        // the registered root, never a wildcard prefix match.
        const sibling = await findCaseSibling(scenario)
        expect(await pathExists(join(sibling, "unregistered.txt")), `${label}: sibling was wildcard-deleted`).toBe(true)
        // Zero remaining owned fixture roots on every other path. The signal
        // cases exit inside the handler (no afterAll report), so their root
        // comes from the journal the synchronous block flushed.
        const root = (result.report?.synopsis.root ?? result.journal.find((entry) => entry.kind === "removal_entry")?.root) as string | undefined
        if (RETAINING_SCENARIOS.has(scenario)) {
          // A failed removal retains the root and its collected evidence and
          // is visible; the parent then confines the known leftover.
          expect(root, `${label}: retained root missing from report`).toBeDefined()
          expect(await pathExists(join(root as string, "evidence.txt")), `${label}: retained evidence vanished`).toBe(true)
          await chmod(root as string, 0o700)
          await rm(root as string, { recursive: true, force: true })
        } else {
          // Zero remaining owned fixture roots on every other path.
          expect(root, `${label}: root missing from report`).toBeDefined()
          expect(await pathExists(root as string), `${label}: owned root survived`).toBe(false)
          await expectProcessGone(result.report?.synopsis.childPid, label)
        }
        await assertScenarioSpecific(result, sibling, label)
        expect(await digestTree(sentinel), `${label}: foreign sentinel changed`).toBe(sentinelBefore)
        await rm(sibling, { recursive: true, force: true })
      }
    }
  } finally {
    await rm(workDir, { recursive: true, force: true })
    expect(await digestTree(sentinel), "foreign sentinel changed at teardown").toBe(sentinelBefore)
    await rm(sentinel, { recursive: true, force: true })
  }
}, 300_000)

test("SIGKILL cannot run in-process cleanup and leaves an identifiable owned leftover", async () => {
  const workDir = await mkdtemp(join(tmpdir(), `${OWNED_PREFIX}kill-`))
  try {
    const reportPath = join(workDir, "report.json")
    const journalPath = join(workDir, "journal.json")
    const child = Bun.spawn([process.execPath, "test", CASE_FILE], {
      cwd: import.meta.dir,
      env: {
        ...process.env,
        CONCORD_LIFECYCLE_CASE: "sigkill",
        CONCORD_LIFECYCLE_TOKEN: TOKEN,
        CONCORD_LIFECYCLE_REPORT: reportPath,
        CONCORD_FIXTURE_JOURNAL: journalPath,
      },
      stdin: "ignore",
      stdout: "pipe",
      stderr: "pipe",
    })
    // Wait for the case's owned root (with its marker) to exist, give the
    // supervised writer a moment to spawn, then kill the process outright.
    let root = ""
    for (let attempt = 0; attempt < 100 && !root; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 100))
      const names = await readdir(tmpdir())
      for (const name of names) {
        if (!name.startsWith(`${OWNED_PREFIX}sigkill-`)) continue
        if (existsSync(join(tmpdir(), name, OWNED_ROOT_MARKER))) root = join(tmpdir(), name)
      }
    }
    expect(root, "sigkill case never allocated its root").not.toBe("")
    await new Promise((resolve) => setTimeout(resolve, 300))
    child.kill("SIGKILL")
    const [stderr, exited] = await Promise.all([new Response(child.stderr).text(), child.exited])
    // 137 is the conventional status of a SIGKILL death; no handler ran.
    const exitCode = exited ?? 128 + (SIGNAL_NUMBERS[child.signalCode ?? "SIGKILL"] ?? 9)
    expect(exitCode).toBe(137)
    // No in-process cleanup claimed anything: neither the case report nor the
    // signal journal exists.
    expect(await pathExists(reportPath)).toBe(false)
    expect(await pathExists(journalPath)).toBe(false)
    // The leftover survives and is identifiable by its ownership marker.
    expect(await pathExists(root)).toBe(true)
    const marker = JSON.parse(await readFile(join(root, OWNED_ROOT_MARKER), "utf8")) as { pid?: number }
    expect(marker.pid).toBe(child.pid)
    // The supervised writer tree did not leak: its supervisor watched the
    // killed parent die and drained its own command tree. That drain is
    // asynchronous (parent poll plus SIGTERM grace before SIGKILL), so wait
    // it out before asserting the heartbeat went stable.
    const sibling = await findCaseSibling("sigkill")
    const heartbeat = await expectHeartbeatStable(sibling, "sigkill", 2_000)
    await expectProcessGone(heartbeat.lastPid, "sigkill supervised writer")
    // Confine the identified leftover manually; the module claimed nothing.
    await rm(root, { recursive: true, force: true })
    await rm(sibling, { recursive: true, force: true })
    expect(stderr).not.toContain("removal_error")
  } finally {
    await rm(workDir, { recursive: true, force: true })
  }
}, 60_000)

test("no owned fixture root or sibling remains under this run's token after the full run", async () => {
  const names = await readdir(tmpdir())
  const remaining = names.filter((name) => name.startsWith(OWNED_PREFIX))
  expect(remaining).toEqual([])
})
