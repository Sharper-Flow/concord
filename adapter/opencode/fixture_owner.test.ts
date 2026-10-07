// Owner-level regression matrix for the external fixture-run owner
// (fixture-root-owner.py). Every case runs the real owner as a child of this
// test, and the owner runs the real case file (fixture-owner.case.ts) as a
// child `bun test` process, so bun:test's own timeouts, hooks, and exit
// statuses are the exercised ones. Three regression shapes are permanent
// residents here: (1) a real 50ms-timeout body that resumes at 200ms during
// another awaited hook and writes into the root, (2) a separately sessioned
// SIGTERM-ignoring six-process writer chain that only unbounded kill/reap
// rounds — bounded by the kernel's ECHILD, never by a round count — can
// drain before removal, and (3) a cleanup failure after a passing run, which
// must exit nonzero with a visible removal_error. The owner's whole startup,
// cancellation, and pipe-failure surface is covered by the focused
// regressions below this matrix.
//
// Every lifecycle case runs three consecutive times and asserts: the exact
// owned run root (from the owner's own allocated_root journal, never a
// global-prefix inventory) is gone and stays gone, all owned descendants are
// reaped before the removal entry, original failures and signal statuses
// 130/143 are preserved, removal errors are visible with evidence retained,
// a same-prefix sibling and a concurrent foreign-run sentinel stay
// untouched, and an owner killed by SIGKILL — which can run no cleanup at
// all — leaves an identifiable leftover that no shipped mode reclaims: only
// the test-owned adoption probe (fixture-owner-adopt-probe.py) confines it,
// through kernel adoption of exactly that run's descendants. The same-prefix
// sibling and the foreign sentinel are created and hashed BEFORE each owner
// launch and stay present through drainage and removal, so their unchanged
// bytes afterwards are real isolation evidence, not an artifact of arriving
// after the cleanup finished. Faults that cannot be timed against a real
// run — marker persistence, the post-spawn journal, and a signal racing a
// failing resolution, drain, or removal — are covered deterministically by
// fixture-owner-fault-probe.py below the matrix. No model call happens
// anywhere in this file.
import { expect, test } from "bun:test"
import { chmod, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises"
import { existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { OWNED_ROOT_MARKER } from "./fixture-temp-root"

const OWNER = join(import.meta.dir, "fixture-root-owner.py")
const PROBE = join(import.meta.dir, "fixture-owner-adopt-probe.py")
const FAULT_PROBE = join(import.meta.dir, "fixture-owner-fault-probe.py")
const CASE_FILE = join(import.meta.dir, "fixture-owner.case.ts")
// Resolved once here, against a stable PATH: a pyenv-style shim would break
// when a case deliberately strips PATH inside the child environment, and the
// owner process itself must still be spawnable in every case.
const PYTHON = (Bun.which("python3", { PATH: "/usr/bin:/bin" }) ?? Bun.which("python3")) as string
const SIGNAL_NUMBERS: Record<string, number> = { SIGINT: 2, SIGTERM: 15, SIGKILL: 9 }

const EXPECTED_EXIT: Record<string, number> = {
  success: 0,
  "assertion-failure": 1,
  "timeout-resumption": 1,
  "timeout-nosettle": 1,
  "teardown-exception": 1,
  "cooperative-abort": 0,
  "cleanup-failure-passing": 91,
  "cleanup-failure-failing": 1,
  "chain-writer": 0,
  "daemon-writer": 0,
  "owner-eof": 125,
  sigint: 130,
  sigterm: 143,
  // A signal landing in a live run whose removal will also fail: the
  // captured signal keeps 130/143 instead of degrading to the cleanup
  // failure's 91.
  "sigint-removal-failure": 130,
  "sigterm-removal-failure": 143,
}
const RETAINING = new Set([
  "cleanup-failure-passing",
  "cleanup-failure-failing",
  "sigint-removal-failure",
  "sigterm-removal-failure",
])
// The sleepy cases have no single expected status: they end by the matrix's
// own action (SIGINT, SIGTERM, EOF, SIGKILL), never by themselves.
const ACTION_CASES: Record<string, "eof" | "SIGINT" | "SIGTERM" | "SIGKILL"> = {
  "owner-eof": "eof",
  sigint: "SIGINT",
  sigterm: "SIGTERM",
  "sigint-removal-failure": "SIGINT",
  "sigterm-removal-failure": "SIGTERM",
}
// Which live case each action drives: the removal-failure actions need the
// sleepy variant whose run root is already read-only.
const ACTION_CASE_SCENARIO: Record<string, string> = {
  "owner-eof": "sleepy",
  sigint: "sleepy",
  sigterm: "sleepy",
  "sigint-removal-failure": "sleepy-cleanup-failure",
  "sigterm-removal-failure": "sleepy-cleanup-failure",
}

interface OwnerEvent {
  kind: string
  root?: string
  nonce?: string
  inner_pid?: number
  status?: number
  via?: string
  detail?: string
  signalled?: number[]
  killed?: number[]
  reaped?: number[]
  processes?: number[]
  owner_status?: number
  marker_present?: boolean
  owner_journalled_removal_error?: boolean
}

interface CaseFindings {
  scenario: string
  findings: Record<string, unknown>
}

interface OwnerRun {
  exitCode: number
  stdout: string
  stderr: string
  events: OwnerEvent[]
  findings: CaseFindings | undefined
}

async function pathExists(path: string): Promise<boolean> {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}

function parseEvents(stderr: string): OwnerEvent[] {
  const events: OwnerEvent[] = []
  for (const line of stderr.split("\n")) {
    if (!line.startsWith("@@concord-owner ")) continue
    try {
      events.push(JSON.parse(line.slice("@@concord-owner ".length)) as OwnerEvent)
    } catch {
      // A malformed line never breaks a case.
    }
  }
  return events
}

// Reads the owner's stderr line by line while the owner runs, so the matrix
// can act (signal, EOF) the moment the owner journals a fact.
class LineReader {
  private buffer = ""
  private text = ""
  private lines: string[] = []
  private done = false
  private readonly waiters: Array<{ predicate: (line: string) => boolean; resolve: (line: string) => void }> = []

  constructor(stream: ReadableStream<Uint8Array>) {
    void this.pump(stream)
  }

  private async pump(stream: ReadableStream<Uint8Array>): Promise<void> {
    const reader = stream.getReader()
    const decoder = new TextDecoder()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      const chunk = decoder.decode(value, { stream: true })
      this.text += chunk
      this.buffer += chunk
      const parts = this.buffer.split("\n")
      this.buffer = parts.pop() ?? ""
      for (const line of parts) {
        this.lines.push(line)
        for (let i = this.waiters.length - 1; i >= 0; i--) {
          if (this.waiters[i].predicate(line)) this.waiters.splice(i, 1)[0].resolve(line)
        }
      }
    }
    this.done = true
  }

  waitFor(predicate: (line: string) => boolean, timeoutMs: number): Promise<string> {
    const existing = this.lines.find(predicate)
    if (existing) return Promise.resolve(existing)
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("timed out waiting for an owner journal line")), timeoutMs)
      const waiter = { predicate, resolve: (line: string) => { clearTimeout(timer); resolve(line) } }
      this.waiters.push(waiter)
      void this.done
    })
  }

  async textAfterExit(boundMs = 5_000): Promise<string> {
    // The inner run inherits the owner's pipes, so the stream can stay open
    // briefly past the owner's own exit (and past an owner SIGKILL, until
    // recovery contains the survivors). Bounded, never unbounded.
    const deadline = Date.now() + boundMs
    while (!this.done && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 20))
    return this.text
  }
}

async function expectProcessGone(pid: number | undefined, label: string): Promise<void> {
  if (!pid) return
  for (let attempt = 0; attempt < 100; attempt++) {
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
  pids: number[]
  lines: number
}

async function readHeartbeat(path: string): Promise<Heartbeat> {
  if (!existsSync(path)) return { pids: [], lines: 0 }
  const text = await readFile(path, "utf8")
  const lines = text.trim().split("\n").filter(Boolean)
  return { pids: [...new Set(lines.map((line) => Number(line.split(" ")[0])))], lines: lines.length }
}

async function expectHeartbeatStable(path: string, label: string, settleMs = 0): Promise<Heartbeat> {
  if (settleMs > 0) await new Promise((resolve) => setTimeout(resolve, settleMs))
  const first = await readHeartbeat(path)
  await new Promise((resolve) => setTimeout(resolve, 350))
  const second = await readHeartbeat(path)
  expect(second.lines, `${label}: heartbeat kept growing after cleanup (${first.lines} -> ${second.lines})`).toBe(first.lines)
  return second
}

async function digestTree(root: string): Promise<string> {
  const names = await readdir(root)
  const parts: string[] = []
  for (const name of names.sort()) parts.push(`${name}:${await readFile(join(root, name), "utf8")}`)
  return parts.join("|")
}

function eventAt(events: OwnerEvent[], kind: string): OwnerEvent | undefined {
  return events.find((event) => event.kind === kind)
}

async function runOwnerCase(scenario: string, workDir: string, action?: "eof" | "SIGINT" | "SIGTERM" | "SIGKILL"): Promise<OwnerRun> {
  const reportPath = join(workDir, `${scenario}.report.json`)
  const pulsePath = join(workDir, `${scenario}.pulse.txt`)
  // The action cases all drive one live inner run from outside; the case
  // scenario selects it while the label keeps the action.
  const caseScenario = action ? (ACTION_CASE_SCENARIO[scenario] ?? "sleepy") : scenario
  // A stale heartbeat AND a stale report from an earlier run would satisfy
  // the readiness wait with the previous run's facts; each run observes only
  // its own run's publications.
  await rm(pulsePath, { force: true })
  await rm(reportPath, { force: true })
  const child = Bun.spawn([PYTHON, OWNER, CASE_FILE], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      CONCORD_OWNER_CASE: caseScenario,
      CONCORD_OWNER_REPORT: reportPath,
      CONCORD_OWNER_PULSE: pulsePath,
      CONCORD_OWNER_BUN: process.execPath,
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const reader = new LineReader(child.stderr)
  // Readiness for an externally driven case, measured only from THIS run's
  // own publications: the cleanup-failure variants republish their findings
  // report after writing evidence and before going quiet, and every variant
  // raises its six-link chain. A readiness wait is scheduling, never an
  // assertion: it waits on what the case itself published, so the driving
  // action can never land before the facts the assertions rely on exist.
  // The cleanup-failure report is published after the permission changes,
  // so its readiness fact, not a grace timer, admits the driving signal.
  const waitForReport = caseScenario === "sleepy-cleanup-failure"
  const awaitReady = async (): Promise<void> => {
    for (let attempt = 0; attempt < 600; attempt++) {
      const chainUp = (await readHeartbeat(pulsePath)).pids.length >= 6
      const published = !waitForReport || ((await pathExists(reportPath)) &&
        (JSON.parse(await readFile(reportPath, "utf8")) as CaseFindings).findings.ready === true)
      if (chainUp && published) return
      await new Promise((resolve) => setTimeout(resolve, 100))
    }
    throw new Error(`${scenario}: this run did not publish readiness before its driving action`)
  }
  if (action === "eof") {
    await reader.waitFor((line) => line.includes('"allocated_root"'), 15_000)
    // Give the sleepy case time to spawn its writer chain, then die the way
    // an abnormal launcher does: the stdin pipe closes, which requests
    // cancellation by EOF alone.
    await awaitReady()
    await child.stdin.end()
  } else if (action === "SIGINT" || action === "SIGTERM" || action === "SIGKILL") {
    await reader.waitFor((line) => line.includes('"allocated_root"'), 15_000)
    await awaitReady()
    child.kill(action)
  }
  const stdout = await new Response(child.stdout).text()
  const stderr = await reader.textAfterExit()
  const exited = await child.exited
  const exitCode = exited ?? (child.signalCode ? 128 + (SIGNAL_NUMBERS[child.signalCode] ?? 0) : -1)
  let findings: CaseFindings | undefined
  if (await pathExists(reportPath)) findings = JSON.parse(await readFile(reportPath, "utf8")) as CaseFindings
  return { exitCode, stdout, stderr, events: parseEvents(stderr), findings }
}

async function assertScenario(result: OwnerRun, scenario: string, workDir: string, label: string): Promise<void> {
  const root = eventAt(result.events, "allocated_root")?.root
  expect(root, `${label}: owner never journalled its run root`).toBeDefined()
  const rootPath = root as string
  const drain = eventAt(result.events, "drain_complete")
  expect(drain, `${label}: owner never reached the drain boundary`).toBeDefined()
  const drainIndex = result.events.indexOf(drain as OwnerEvent)
  const completion = eventAt(result.events, "completion")
  const removalError = eventAt(result.events, "removal_error")
  const findings = result.findings?.findings ?? {}
  const pulsePath = join(workDir, `${scenario}.pulse.txt`)

  if (RETAINING.has(scenario)) {
    // The removal failed: the error is visible in the owner's own output,
    // the status is nonzero (or the preserved original failure), and the
    // root plus its collected evidence stay in place.
    expect(removalError, `${label}: failed removal journalled no removal_error`).toBeDefined()
    expect(result.stderr).toContain("removal_error")
    expect(await pathExists(rootPath), `${label}: retained root vanished`).toBe(true)
    const retainedRoot = String(findings.root ?? rootPath)
    expect(await pathExists(join(retainedRoot, "evidence.txt")), `${label}: retained evidence vanished`).toBe(true)
    expect(await readFile(join(retainedRoot, "evidence.txt"), "utf8"), `${label}: retained evidence changed`).toBe("collected evidence\n")
    return
  }
  expect(removalError, `${label}: unexpected removal_error`).toBeUndefined()
  expect(completion, `${label}: no completion event`).toBeDefined()
  // Reaped BEFORE removal: the drain boundary precedes the cleanup entry.
  const cleanupIndex = result.events.findIndex((event, index) => index > drainIndex && event.kind === "cleanup_entry")
  expect(cleanupIndex, `${label}: removal began before the drain boundary`).toBeGreaterThan(drainIndex)
  expect(await pathExists(rootPath), `${label}: owned run root survived`).toBe(false)
  // The root cannot be recreated: nothing owned remains to write it.
  await new Promise((resolve) => setTimeout(resolve, 300))
  expect(await pathExists(rootPath), `${label}: run root was recreated after removal`).toBe(false)

  const drained = (drain?.reaped ?? []).length
  switch (scenario) {
    case "success": {
      expect(findings.underOwnedRoot, `${label}: allocation escaped the owned run root`).toBe(true)
      expect(drained).toBeGreaterThanOrEqual(0)
      break
    }
    case "assertion-failure": {
      expect(result.stderr).toContain("synthetic assertion failure")
      break
    }
    case "timeout-resumption": {
      // The regression shape: the body really resumed after a real timeout
      // and wrote into the root, the root was still there, and removal
      // happened only after the process could not write again.
      expect(findings.resumedAfterTimeout).toBe(true)
      expect(findings.rootPresentAtResumption).toBe(true)
      expect(findings.wroteAfterTimeout).toBe(true)
      expect(findings.stillAliveAtRunEnd).toBe(true)
      break
    }
    case "timeout-nosettle": {
      expect(findings.allocated).toBe(true)
      break
    }
    case "teardown-exception": {
      expect(result.stderr).toContain("synthetic teardown exception")
      break
    }
    case "cooperative-abort": {
      // Late AbortSignal linkage: the aborted command died nonzero, and the
      // SIGTERM-ignoring child the abort cannot reach was drained by the
      // owner before removal.
      expect(Number(findings.abortedExitCode)).not.toBe(0)
      expect(drained, `${label}: ignoring child was not drained`).toBeGreaterThan(0)
      await expectProcessGone(Number(findings.ignoringPid), `${label} ignoring child`)
      break
    }
    case "chain-writer":
    case "sigint":
    case "sigterm":
    case "owner-eof": {
      // Six separately sessioned, SIGTERM-ignoring links, each visible to
      // the owner only after its parent died: every one killed and reaped
      // before the removal entry.
      expect(drained, `${label}: the six-link chain was not fully reaped`).toBeGreaterThanOrEqual(6)
      expect((drain?.killed ?? []).length, `${label}: chain links died without SIGKILL escalation`).toBeGreaterThanOrEqual(6)
      const heartbeat = await expectHeartbeatStable(pulsePath, label)
      expect(heartbeat.pids.length, `${label}: chain never reached six links`).toBeGreaterThanOrEqual(6)
      for (const pid of heartbeat.pids) await expectProcessGone(pid, `${label} chain link`)
      break
    }
    case "daemon-writer": {
      // The daemonized writer was adopted by the owner mid-run and killed
      // before removal.
      expect(drained, `${label}: the daemon was not drained`).toBeGreaterThanOrEqual(1)
      const heartbeat = await expectHeartbeatStable(pulsePath, label)
      expect(heartbeat.pids.length, `${label}: daemon never wrote`).toBeGreaterThanOrEqual(1)
      for (const pid of heartbeat.pids) await expectProcessGone(pid, `${label} daemon writer`)
      break
    }
  }
}

test("every owner lifecycle case cleans its exact root across three consecutive runs", async () => {
  // A concurrent foreign run: another active run's root that must remain
  // unchanged while every case here cleans up.
  const sentinel = await mkdtemp(join(tmpdir(), "concord-owner-foreign-"))
  await Bun.write(join(sentinel, "active-run.txt"), "another active run owns this root\n")
  const sentinelBefore = await digestTree(sentinel)
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-matrix-"))
  try {
    for (let run = 1; run <= 3; run++) {
      for (const scenario of Object.keys(EXPECTED_EXIT)) {
        const action = ACTION_CASES[scenario]
        // A same-prefix sibling of the owner's run roots, created and hashed
        // BEFORE the owner launches and present through drainage and removal:
        // removal must stay scoped to the exact owned root, never a prefix
        // match, and the unchanged bytes afterwards prove it.
        const sibling = await mkdtemp(join(tmpdir(), "cfx-"))
        await Bun.write(join(sibling, "unregistered.txt"), "not owned by this run\n")
        const siblingBefore = await digestTree(sibling)
        const result = await runOwnerCase(scenario, workDir, action)
        const root = eventAt(result.events, "allocated_root")?.root
        try {
          const label = `${scenario} run ${run}`
          expect(result.exitCode, `${label} stderr: ${result.stderr.slice(-2000)}`).toBe(EXPECTED_EXIT[scenario])
          await assertScenario(result, scenario, workDir, label)
          expect(await digestTree(sibling), `${label}: same-prefix sibling changed during cleanup`).toBe(siblingBefore)
          expect(await digestTree(sentinel), `${label}: foreign sentinel changed`).toBe(sentinelBefore)
          // The cancelled cases preserve the conventional signal/EOF statuses.
          if (scenario === "sigint" || scenario === "sigterm" || scenario === "sigint-removal-failure" || scenario === "sigterm-removal-failure") {
            expect(eventAt(result.events, "cancelled")?.via).toBe(scenario.startsWith("sigint") ? "SIGINT" : "SIGTERM")
          }
        } finally {
          // Restore only this run's known failure fixture after its owner
          // proves drainage. A missing proof keeps the root, even at teardown.
          if (root && RETAINING.has(scenario) && eventAt(result.events, "drain_complete") && await pathExists(root)) {
            await chmod(root, 0o755)
            const fixture = result.findings?.findings.root
            if (typeof fixture === "string" && fixture.startsWith(`${root}/`) && await pathExists(fixture)) {
              await chmod(fixture, 0o755)
            }
            await rm(root, { recursive: true, force: true })
          }
          await rm(sibling, { recursive: true, force: true })
        }
      }
    }
  } finally {
    await rm(workDir, { recursive: true, force: true })
    expect(await digestTree(sentinel), "foreign sentinel changed at teardown").toBe(sentinelBefore)
    await rm(sentinel, { recursive: true, force: true })
  }
}, 480_000)

test("owner SIGKILL leaves an identifiable leftover that only the test-owned adoption probe confines", async () => {
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-kill-"))
  const pulsePath = join(workDir, "kill.pulse.txt")
  // A same-prefix foreign sibling, created and hashed BEFORE the probe
  // launches and present through the whole adoption containment: its
  // unchanged bytes afterwards prove the probe removed only its registered
  // root, never a prefix match.
  const sibling = await mkdtemp(join(tmpdir(), "cfx-"))
  await Bun.write(join(sibling, "unregistered.txt"), "not owned by this run\n")
  const siblingBefore = await digestTree(sibling)
  // The probe is the owner's parent and a subreaper BEFORE the owner
  // launches, so the killed owner's whole process tree moves onto it by
  // kernel adoption. No shipped mode reclaims the leftover: this is test
  // machinery, and it claims containment of exactly this run, nothing else.
  const probe = Bun.spawn([PYTHON, PROBE, CASE_FILE, pulsePath], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      CONCORD_OWNER_CASE: "sleepy",
      CONCORD_OWNER_REPORT: join(workDir, "report.json"),
      CONCORD_OWNER_PULSE: pulsePath,
      CONCORD_OWNER_BUN: process.execPath,
    },
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  })
  const [stdout, stderr, exited] = await Promise.all([new Response(probe.stdout).text(), new Response(probe.stderr).text(), probe.exited])
  const events = parseEvents(stderr)
  const root = eventAt(events, "probe_registered_root")?.root
  expect(exited, `adoption probe failed: ${stdout}${stderr}`).toBe(0)
  expect(root, "probe never registered the owner's allocated root").toBeDefined()

  // The killed owner ran no cleanup at all: its status is 137 and the
  // leftover — root plus intact ownership marker — was observed before any
  // containment started. No automatic reclamation is claimed or performed.
  expect(eventAt(events, "probe_kill")?.owner_status).toBe(137)
  const leftover = eventAt(events, "probe_leftover")
  expect(leftover, "probe never observed the SIGKILL leftover").toBeDefined()
  expect(leftover?.marker_present, "leftover root lost its ownership marker").toBe(true)
  expect(leftover?.owner_journalled_removal_error).toBe(false)

  // Containment order is the owner's own: every adopted descendant dead and
  // reaped to the kernel's ECHILD boundary BEFORE the exact root is removed.
  const drain = eventAt(events, "probe_drain_complete")
  expect(drain, "probe never reached the drain boundary").toBeDefined()
  expect(events.indexOf(drain as OwnerEvent), "leftover observed after containment").toBeGreaterThan(events.indexOf(leftover as OwnerEvent))
  expect((drain?.reaped ?? []).length, "the adopted tree was not fully reaped").toBeGreaterThanOrEqual(6)
  const cleanup = eventAt(events, "cleanup_entry")
  expect(cleanup, "probe removed no root").toBeDefined()
  expect(events.indexOf(cleanup as OwnerEvent), "removal began before the drain boundary").toBeGreaterThan(events.indexOf(drain as OwnerEvent))
  expect(eventAt(events, "removal_error")).toBeUndefined()
  expect(eventAt(events, "completion")).toBeDefined()

  const rootPath = root as string
  expect(await pathExists(rootPath), "confined root survived").toBe(false)
  await new Promise((resolve) => setTimeout(resolve, 300))
  expect(await pathExists(rootPath), "confined root was recreated after removal").toBe(false)
  const heartbeat = await expectHeartbeatStable(pulsePath, "owner-sigkill after containment")
  expect(heartbeat.pids.length, "chain never reached six links").toBeGreaterThanOrEqual(6)
  for (const pid of heartbeat.pids) await expectProcessGone(pid, "contained chain link")
  expect(await digestTree(sibling), "same-prefix sibling changed during containment").toBe(siblingBefore)
  try {
    await rm(workDir, { recursive: true, force: true })
  } finally {
    if (await pathExists(rootPath)) await rm(rootPath, { recursive: true, force: true })
    await rm(sibling, { recursive: true, force: true })
  }
}, 120_000)

test("the removed recovery route stays removed: usage only, nothing scanned, nothing touched", async () => {
  // A decoy root with a valid-looking marker: a usage error must leave it
  // exactly as it was — no process-table scan, no signalling, no removal.
  const decoy = await mkdtemp(join(tmpdir(), "cfx-"))
  const markerText = `${JSON.stringify({ nonce: "synthetic", owner: "decoy" })}\n`
  await Bun.write(join(decoy, OWNED_ROOT_MARKER), markerText)
  const run = Bun.spawnSync([PYTHON, OWNER, "--recover", decoy, "synthetic"], {
    cwd: import.meta.dir,
    env: { ...process.env },
    stdout: "pipe",
    stderr: "pipe",
  })
  expect(run.exitCode, `removed route was not a usage error: ${run.stdout.toString()}${run.stderr.toString()}`).toBe(2)
  expect(run.stderr.toString()).toContain("usage: fixture-root-owner.py CASE_FILE")
  expect(run.stderr.toString()).not.toContain("recovery_")
  expect(run.stdout.toString()).toBe("")
  expect(await pathExists(decoy), "usage error touched a root").toBe(true)
  expect(await readFile(join(decoy, OWNED_ROOT_MARKER), "utf8")).toBe(markerText)
  await rm(decoy, { recursive: true, force: true })
}, 30_000)

test("startup failure — unresolvable or unspawnable bun — cleans the exact root and fails visibly", async () => {
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-startfail-"))
  const unspawnable = join(workDir, "not-a-bun")
  await writeFile(unspawnable, "#!/bin/sh\nsleep 30\n")
  await chmod(unspawnable, 0o644) // exists, but will not exec
  const cases: Array<{ label: string; bun: string; path?: string }> = [
    { label: "no-executable", bun: "", path: "/nonexistent" },
    { label: "unresolved-path", bun: join(workDir, "missing-bun") },
    { label: "unspawnable", bun: unspawnable },
  ]
  for (const { label, bun, path } of cases) {
    const child = Bun.spawn([PYTHON, OWNER, CASE_FILE], {
      cwd: import.meta.dir,
      env: {
        ...process.env,
        ...(path ? { PATH: path } : {}),
        CONCORD_OWNER_CASE: "success",
        CONCORD_OWNER_REPORT: join(workDir, `${label}.report.json`),
        CONCORD_OWNER_PULSE: join(workDir, `${label}.pulse.txt`),
        CONCORD_OWNER_BUN: bun,
      },
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
    })
    const [stdout, stderr, exited] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
    const events = parseEvents(stderr)
    const root = eventAt(events, "allocated_root")?.root
    expect(root, `${label}: owner never journalled its run root`).toBeDefined()
    expect(exited, `${label} startup failure stderr: ${stdout}${stderr}`).toBe(92)
    expect(eventAt(events, "startup_error"), `${label}: no startup_error journalled`).toBeDefined()
    expect(eventAt(events, "drain_complete"), `${label}: no drain at startup failure`).toBeDefined()
    expect(eventAt(events, "completion"), `${label}: exact root not removed at startup failure`).toBeDefined()
    expect(await pathExists(root as string), `${label}: root survived startup failure`).toBe(false)
    await rm(root as string, { recursive: true, force: true })
  }
  await rm(workDir, { recursive: true, force: true })
}, 30_000)

test("startup cancellation — SIGTERM before the inner run starts — still drains and removes the root", async () => {
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-startcancel-"))
  const stub = join(workDir, "slow-bun")
  await writeFile(stub, "#!/bin/sh\nsleep 30\n")
  await chmod(stub, 0o755)
  const child = Bun.spawn([PYTHON, OWNER, CASE_FILE], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      CONCORD_OWNER_CASE: "sleepy",
      CONCORD_OWNER_REPORT: join(workDir, "report.json"),
      CONCORD_OWNER_PULSE: join(workDir, "sc.pulse.txt"),
      CONCORD_OWNER_BUN: stub,
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const reader = new LineReader(child.stderr)
  // allocated_root is journalled BEFORE the inner spawn: the moment it
  // appears, the owner is mid-startup and signal handling is already
  // installed, so this cancellation lands in the startup window.
  await reader.waitFor((line) => line.includes('"allocated_root"'), 15_000)
  child.kill("SIGTERM")
  const stderr = await reader.textAfterExit()
  const exited = await child.exited
  const events = parseEvents(stderr)
  const root = eventAt(events, "allocated_root")?.root
  expect(root, "owner never journalled its run root").toBeDefined()
  expect(exited, `startup-cancellation stderr: ${stderr.slice(-2000)}`).toBe(143)
  expect(eventAt(events, "cancelled")?.via).toBe("SIGTERM")
  expect(eventAt(events, "drain_complete"), "no drain at startup cancellation").toBeDefined()
  expect(eventAt(events, "completion"), "root not removed at startup cancellation").toBeDefined()
  expect(await pathExists(root as string), "root survived startup cancellation").toBe(false)
  await rm(workDir, { recursive: true, force: true })
}, 60_000)

test("a SIGTERM arriving during drain/removal preserves its conventional 143 status", async () => {
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-late-"))
  const pulsePath = join(workDir, "late.pulse.txt")
  const child = Bun.spawn([PYTHON, OWNER, CASE_FILE], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      CONCORD_OWNER_CASE: "chain-writer",
      CONCORD_OWNER_REPORT: join(workDir, "report.json"),
      CONCORD_OWNER_PULSE: pulsePath,
      CONCORD_OWNER_BUN: process.execPath,
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const reader = new LineReader(child.stderr)
  // The inner chain-writer run passes and exits on its own; the drain of its
  // six-link chain is the cleanup window this signal lands in.
  await reader.waitFor((line) => line.includes('"child_exit"'), 30_000)
  child.kill("SIGTERM")
  const stderr = await reader.textAfterExit()
  const exited = await child.exited
  const events = parseEvents(stderr)
  const root = eventAt(events, "allocated_root")?.root
  expect(root, "owner never journalled its run root").toBeDefined()
  expect(exited, `late-signal stderr: ${stderr.slice(-2000)}`).toBe(143)
  expect(eventAt(events, "completion"), "cleanup did not complete after the late signal").toBeDefined()
  expect(await pathExists(root as string), "root survived the late signal").toBe(false)
  await new Promise((resolve) => setTimeout(resolve, 300))
  expect(await pathExists(root as string), "run root was recreated after removal").toBe(false)
  const heartbeat = await expectHeartbeatStable(pulsePath, "late-signal")
  for (const pid of heartbeat.pids) await expectProcessGone(pid, "late-signal chain link")
  await rm(workDir, { recursive: true, force: true })
}, 90_000)

test("launcher death with closed output pipes cannot abort the owner's cleanup", async () => {
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-pipes-"))
  const pulsePath = join(workDir, "pipes.pulse.txt")
  const child = Bun.spawn([PYTHON, OWNER, CASE_FILE], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      CONCORD_OWNER_CASE: "sleepy",
      CONCORD_OWNER_REPORT: join(workDir, "report.json"),
      CONCORD_OWNER_PULSE: pulsePath,
      CONCORD_OWNER_BUN: process.execPath,
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  // Read just enough journal to learn the exact root, then give the pipe
  // away: this test deliberately keeps no copy of the owner's output.
  const streamReader = child.stderr.getReader()
  const decoder = new TextDecoder()
  let buffer = ""
  let root: string | undefined
  const deadline = Date.now() + 15_000
  while (root === undefined && Date.now() < deadline) {
    const { done, value } = await streamReader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split("\n")
    buffer = parts.pop() ?? ""
    for (const line of parts) {
      if (line.startsWith("@@concord-owner ") && line.includes('"allocated_root"')) {
        root = (JSON.parse(line.slice("@@concord-owner ".length)) as OwnerEvent).root
      }
    }
  }
  expect(root, "owner never journalled its run root").toBeDefined()
  for (let attempt = 0; attempt < 100; attempt++) {
    if ((await readHeartbeat(pulsePath)).pids.length >= 6) break
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  expect((await readHeartbeat(pulsePath)).pids.length, "chain never reached six links").toBeGreaterThanOrEqual(6)
  // The launcher side dies completely: both output pipes close first (every
  // later owner write must fail), then the stdin pipe closes (EOF). Cleanup
  // has to run through those failures and still remove the exact root.
  streamReader.releaseLock()
  await child.stderr.cancel()
  await child.stdout.cancel()
  await child.stdin.end()
  const exited = await child.exited
  expect(exited).toBe(125) // EOF cancellation with cleanup completed
  const rootPath = root as string
  for (let attempt = 0; attempt < 200 && (await pathExists(rootPath)); attempt++) {
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  expect(await pathExists(rootPath), "cleanup aborted when the output pipes closed").toBe(false)
  await new Promise((resolve) => setTimeout(resolve, 300))
  expect(await pathExists(rootPath), "run root was recreated after removal").toBe(false)
  for (const pid of (await readHeartbeat(pulsePath)).pids) await expectProcessGone(pid, "closed-pipes chain link")
  await rm(workDir, { recursive: true, force: true })
}, 90_000)

interface FaultProbeResult {
  probe?: string
  exit?: number
  escaped?: string
  root_removed?: boolean
  events?: string[]
  details?: Record<string, string>
  drain_reasons?: string[]
  stderr?: string
  fault_details?: string[]
  journal_failures?: string[]
  failed_fault_details?: string[]
}

test("deterministic fault probes hold every post-allocation failure inside the one lifecycle", async () => {
  // Real-run timing cannot place a SIGTERM inside a failing resolution,
  // drain, or removal, and cannot produce ENOSPC on demand, so these faults
  // drive the owner's own run_owned() deterministically inside the probe
  // process: no real child is spawned, no real signal is sent, and every
  // probe root is confined to a probe-owned temporary parent. The assertions
  // here are the permanent form of the boundary findings: a marker failure
  // must not leak an unregistered root, a post-spawn journal failure must
  // still drain and remove, a captured signal keeps 130/143 over every
  // startup, drain, and removal failure, persistent journal ENOSPC stays
  // finite (reporting may never grow what it iterates) and still allows the
  // authorized rmtree, a nonce failure after allocation still cleans the
  // owned root, a termination failure never assumes the child dead, and
  // journal failures inside cleanup itself never skip the deletion.
  // Every failure case runs three consecutive times.
  const attempt = async (): Promise<void> => {
  const probe = Bun.spawn([PYTHON, FAULT_PROBE], {
    cwd: import.meta.dir,
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  })
  const [stdout, stderr, exited] = await Promise.all([
    new Response(probe.stdout).text(),
    new Response(probe.stderr).text(),
    probe.exited,
  ])
  expect(exited, `fault probe crashed: ${stderr}`).toBe(0)
  const lines = stdout
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line) as FaultProbeResult & { probes?: number; real_processes_spawned?: number })
  const summary = lines[lines.length - 1]
  const expectedProbeCount = 49
  expect(summary.probes, `fault probe did not report its run: ${stdout}${stderr}`).toBe(expectedProbeCount)
  expect(summary.real_processes_spawned).toBe(0)
  const byLabel = new Map(lines.slice(0, -1).map((line) => [String(line.probe), line]))
  expect(byLabel.size, "fault probe labels must identify distinct scenarios").toBe(expectedProbeCount)
  const row = (label: string): FaultProbeResult => {
    const found = byLabel.get(label)
    expect(found, `probe ${label} never ran`).toBeDefined()
    return found as FaultProbeResult
  }
  const expectNoEscape = (label: string, found: FaultProbeResult): void => {
    expect(found.escaped, `${label}: the fault escaped the lifecycle (${found.escaped})`).toBeUndefined()
  }

  {
    // Marker persistence fails after registration: the exact root still
    // drains and is removed, the error stays visible, and the run fails.
    const found = row("marker-fault")
    expectNoEscape("marker-fault", found)
    expect(found.exit).toBe(92)
    expect(found.events?.[0], "the root was not registered before the marker write").toBe("allocated_root")
    expect(found.events).toContain("startup_error")
    expect(found.details?.startup_error).toContain("marker persistence failed")
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("completion")
    expect(found.root_removed, "marker failure leaked its root").toBe(true)
  }
  {
    // A SIGTERM racing the marker write keeps 143; cleanup still happens.
    const found = row("marker-fault-sigterm")
    expectNoEscape("marker-fault-sigterm", found)
    expect(found.exit).toBe(143)
    expect(found.details?.cancelled).toBe("SIGTERM")
    expect(found.events).toContain("completion")
    expect(found.root_removed).toBe(true)
  }
  {
    // The post-spawn journal failure: the started child is drained (never
    // abandoned), the exact root is removed, the fault stays visible.
    const found = row("postspawn-journal-fault")
    expectNoEscape("postspawn-journal-fault", found)
    expect(found.exit).toBe(91)
    expect(found.events).toContain("owner_fault")
    expect(found.details?.owner_fault).toContain("journal inner_started failed")
    expect((found.drain_reasons ?? []).length, "the started child was not drained").toBeGreaterThan(0)
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("completion")
    expect(found.root_removed, "journal failure leaked its root").toBe(true)
  }
  {
    // A SIGTERM captured during failed executable resolution: 143, not 92,
    // with the startup failure still journalled and the root still removed.
    const found = row("startup-signal-sigterm")
    expectNoEscape("startup-signal-sigterm", found)
    expect(found.exit).toBe(143)
    expect(found.details?.cancelled).toBe("SIGTERM")
    expect(found.events).toContain("startup_error")
    expect(found.details?.startup_error).toContain("no bun executable")
    expect(found.events).toContain("completion")
    expect(found.root_removed).toBe(true)
  }
  for (const [label, code] of [["signal-failed-removal-sigterm", 143], ["signal-failed-removal-sigint", 130]] as const) {
    // A captured signal during a failing removal keeps its conventional
    // status; the failed removal keeps the root and stays visible.
    const found = row(label)
    expectNoEscape(label, found)
    expect(found.exit, `${label}: the failed removal overrode the captured signal`).toBe(code)
    expect(found.root_removed).toBe(false)
    expect(found.events).toContain("removal_error")
    expect(found.events).toContain("drain_complete")
    const events = found.events ?? []
    expect(events.indexOf("cleanup_entry")).toBeGreaterThan(events.indexOf("drain_complete"))
  }
  for (const [label, code] of [["signal-failed-drain-sigterm", 143], ["signal-failed-drain-sigint", 130]] as const) {
    // A captured signal during a failed drainage proof keeps its
    // conventional status; without drainage proof nothing is removed.
    const found = row(label)
    expectNoEscape(label, found)
    expect(found.exit, `${label}: the failed drain overrode the captured signal`).toBe(code)
    expect(found.root_removed, `${label}: removed without the kernel's drainage proof`).toBe(false)
    expect(found.events).toContain("removal_error")
    expect(found.events).not.toContain("drain_complete")
    expect(found.events).not.toContain("cleanup_entry")
    expect(found.events).not.toContain("completion")
  }
  {
    // Without any signal: a failed removal after a passing run exits 91 and
    // keeps the root (the matrix covers the same decision as a real run).
    const found = row("plain-failed-removal")
    expectNoEscape("plain-failed-removal", found)
    expect(found.exit).toBe(91)
    expect(found.root_removed).toBe(false)
    expect(found.events).toContain("removal_error")
  }
  {
    // Without any signal: a plain startup failure cleans the root, 92.
    const found = row("plain-startup-failure")
    expectNoEscape("plain-startup-failure", found)
    expect(found.exit).toBe(92)
    expect(found.events).toContain("startup_error")
    expect(found.events).toContain("completion")
    expect(found.root_removed).toBe(true)
  }
  {
    // Nonce generation fails after the root exists: the root was owned state
    // before any fallible operation, so it still drains and is removed.
    const found = row("nonce-fault")
    expectNoEscape("nonce-fault", found)
    expect(found.exit).toBe(91)
    expect(found.events).toContain("owner_fault")
    expect(found.details?.owner_fault).toContain("entropy exhaustion")
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("completion")
    expect(found.root_removed, "nonce failure leaked its root").toBe(true)
  }
  for (const [label, code] of [["persistent-journal-fault", 91], ["persistent-journal-fault-sigterm", 143]] as const) {
    // Persistent journal ENOSPC: the probe returned at all (reporting never
    // grows the fault list it iterates), the authorized rmtree still ran —
    // a failed report never skips the deletion — and the run fails (or keeps
    // the captured signal) with nothing after allocated_root journalled.
    const found = row(label)
    expectNoEscape(label, found)
    expect(found.exit).toBe(code)
    expect(found.events).toEqual(["allocated_root"])
    expect(found.root_removed, `${label}: persistent journal failure leaked its root`).toBe(true)
    expect((found.drain_reasons ?? []).length, `${label}: no drainage ran`).toBeGreaterThan(0)
  }
  // Journal failures inside cleanup itself: each entry is attempted
  // independently of the deletion, the rmtree the drainage proof authorized
  // still runs, no removal_error is claimed, and the unreportable step fails
  // the run instead of pretending it was journalled — the failed kind is
  // exactly the one absent from the journalled events.
  const CLEANUP_JOURNAL_FAULTS: Record<string, string> = {
    "cleanup-entry-journal-fault": "cleanup_entry",
    "removal-entry-journal-fault": "removal_entry",
    "completion-journal-fault": "completion",
  }
  for (const [label, absent] of Object.entries(CLEANUP_JOURNAL_FAULTS)) {
    const found = row(label)
    expectNoEscape(label, found)
    expect(found.exit).toBe(91)
    expect(found.root_removed, `${label}: journal failure skipped the authorized rmtree`).toBe(true)
    expect(found.events).toContain("drain_complete")
    expect(found.events).not.toContain("removal_error")
    expect(found.events, `${label}: the failed step was journalled anyway`).not.toContain(absent)
    expect(found.fault_details, `${label}: the late journal fault was not reported`).toEqual([
      expect.stringContaining(`journal ${absent} failed`),
    ])
    const events = found.events ?? []
    const present = absent === "cleanup_entry" ? "removal_entry" : "cleanup_entry"
    expect(events.indexOf(present), `${label}: no removal entry was attempted`).toBeGreaterThan(events.indexOf("drain_complete"))
  }
  {
    // A stop failure under EOF cancellation: the termination error never
    // assumes the child dead — the kernel-only drain still runs — and the
    // EOF status 125 survives the fault.
    const found = row("stop-failure-eof")
    expectNoEscape("stop-failure-eof", found)
    expect(found.exit).toBe(125)
    expect(found.details?.owner_fault).toContain("inner termination failed")
    expect(found.drain_reasons).toEqual(["cancel_eof"])
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("completion")
    expect(found.root_removed).toBe(true)
  }
  {
    // A SIGTERM captured inside the failing stop keeps 143 with the drain
    // and removal still completed.
    const found = row("stop-failure-sigterm")
    expectNoEscape("stop-failure-sigterm", found)
    expect(found.exit).toBe(143)
    expect(found.details?.cancelled).toBe("SIGTERM")
    expect(found.events).toContain("drain_complete")
    expect(found.root_removed).toBe(true)
  }
  for (const [label, code] of [["poll-failure", 91], ["poll-failure-sigint", 130]] as const) {
    // Poll failures reach the lifecycle — no escape, drain, removal — and a
    // captured SIGINT keeps 130 over the fault.
    const found = row(label)
    expectNoEscape(label, found)
    expect(found.exit).toBe(code)
    expect(found.events).toContain("owner_fault")
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("completion")
    expect(found.root_removed, `${label}: poll failure leaked its root`).toBe(true)
  }
  {
    // Combination: a failing stop, a failing removal, and a captured SIGTERM
    // together — 143 wins, the drain still ran, the root is retained with a
    // visible removal error.
    const found = row("stop-removal-failure-sigterm")
    expectNoEscape("stop-removal-failure-sigterm", found)
    expect(found.exit).toBe(143)
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("removal_error")
    expect(found.root_removed).toBe(false)
  }
  {
    // Combination: persistent journal ENOSPC beside a failed drainage proof
    // — the root is kept (no removal without kernel proof) and the run fails
    // visibly despite the dead journal.
    const found = row("persistent-journal-and-drain-fault")
    expectNoEscape("persistent-journal-and-drain-fault", found)
    expect(found.journal_failures, "combined probe never injected its journal failure").toEqual(["removal_error", "owner_fault"])
    expect(found.failed_fault_details, "the terminal report lost the late journal fault").toEqual([
      expect.stringContaining("journal removal_error failed"),
    ])
    expect(found.events).toEqual(["allocated_root", "inner_started", "child_exit"])
    expect(found.stderr).toContain("removal_error")
    expect(found.stderr).toContain("drain failed closed")
    expect(found.exit).toBe(91)
    expect(found.events).not.toContain("drain_complete")
    expect(found.events).not.toContain("completion")
    expect(found.root_removed, "removed without the kernel's drainage proof").toBe(false)
  }
  for (const boundary of ["poll", "stop", "wait"] as const) {
    for (const [name, code] of [["sigint", 130], ["sigterm", 143]] as const) {
      for (const combined of [false, true]) {
        const label = `${boundary}-${combined ? "journal-removal-failure" : "failure"}-${name}`
        const found = row(label)
        expectNoEscape(label, found)
        expect(found.exit, `${label}: an owner fault overrode the captured signal`).toBe(code)
        expect(found.details?.cancelled).toBe(name === "sigint" ? "SIGINT" : "SIGTERM")
        expect(found.events).toContain("owner_fault")
        expect(found.events).toContain("drain_complete")
        expect(found.root_removed).toBe(!combined)
        if (combined) {
          expect(found.events).toContain("removal_error")
          expect(found.events).not.toContain("cleanup_entry")
          expect(found.fault_details).toContainEqual(expect.stringContaining("journal cleanup_entry failed"))
        } else {
          expect(found.events).toContain("completion")
        }
      }
    }
  }
  {
    const found = row("wait-failure-eof")
    expectNoEscape("wait-failure-eof", found)
    expect(found.exit).toBe(125)
    expect(found.fault_details).toContainEqual(expect.stringContaining("synthetic wait failure"))
    expect(found.events).toContain("drain_complete")
    expect(found.events).toContain("completion")
    expect(found.root_removed).toBe(true)
  }
  for (const status of [0, 1, 7, -9]) {
    for (const fault of ["journal", "owner", "drain", "removal"]) {
      const label = `inner-${status}-${fault}-fault`
      const found = row(label)
      expectNoEscape(label, found)
      const expected = status < 0 ? 128 - status : status || 91
      expect(found.exit, `${label}: a late fault replaced the recorded inner failure`).toBe(expected)
      expect(found.root_removed).toBe(fault !== "drain" && fault !== "removal")
      if (fault === "drain") {
        expect(found.events).not.toContain("drain_complete")
        expect(found.events).not.toContain("cleanup_entry")
      } else {
        expect(found.events).toContain("drain_complete")
      }
      if (fault === "journal" || fault === "owner") {
        expect(found.events).toContain("owner_fault")
        expect((found.fault_details ?? []).length).toBeGreaterThan(0)
      } else {
        expect(found.events).toContain("removal_error")
        expect(found.stderr).toContain("removal_error")
      }
    }
  }
  }
  for (let run = 1; run <= 3; run++) await attempt()
}, 120_000)
