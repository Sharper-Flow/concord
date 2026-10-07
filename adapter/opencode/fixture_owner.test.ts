// Owner-level regression matrix for the external fixture-run owner
// (fixture-root-owner.py). Every case runs the real owner as a child of this
// test, and the owner runs the real case file (fixture-owner.case.ts) as a
// child `bun test` process, so bun:test's own timeouts, hooks, and exit
// statuses are the exercised ones. The three no_ship counterexamples are
// permanent residents here: (1) a real 50ms-timeout body that resumes at
// 200ms during another awaited hook and writes into the root, (2) a
// separately sessioned SIGTERM-ignoring six-process writer chain that only
// unbounded kill/reap rounds — bounded by the kernel's ECHILD, never by a
// round count — can drain before removal, and (3) a cleanup failure after a
// passing run, which must exit nonzero with a visible removal_error.
//
// Every lifecycle case runs three consecutive times and asserts: the exact
// owned run root (from the owner's own allocated_root journal, never a
// global-prefix inventory) is gone and stays gone, all owned descendants are
// reaped before the removal entry, original failures and signal statuses
// 130/143 are preserved, removal errors are visible with evidence retained,
// a same-prefix sibling and a concurrent foreign-run sentinel stay
// untouched, and an owner killed by SIGKILL — which can run no cleanup at
// all — leaves an identifiable leftover confined only by the probe's own
// recovery owner. No model call happens anywhere in this file.
import { expect, test } from "bun:test"
import { chmod, mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises"
import { existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const OWNER = join(import.meta.dir, "fixture-root-owner.py")
const CASE_FILE = join(import.meta.dir, "fixture-owner.case.ts")
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
}
const RETAINING = new Set(["cleanup-failure-passing", "cleanup-failure-failing"])
// The sleepy case has no single expected status: it ends by the matrix's
// own action (SIGINT, SIGTERM, EOF, SIGKILL), never by itself.
const ACTION_CASES: Record<string, "eof" | "SIGINT" | "SIGTERM" | "SIGKILL"> = {
  "owner-eof": "eof",
  sigint: "SIGINT",
  sigterm: "SIGTERM",
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
  // The action cases all drive one live inner run (the sleepy case) from
  // outside; the case scenario selects it while the label keeps the action.
  const caseScenario = action ? "sleepy" : scenario
  // A stale heartbeat from an earlier run would satisfy the live-chain wait
  // with dead PIDs; each run observes only its own chain.
  await rm(pulsePath, { force: true })
  const child = Bun.spawn(["python3", OWNER, CASE_FILE], {
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
  if (action === "eof") {
    await reader.waitFor((line) => line.includes('"allocated_root"'), 15_000)
    // Give the sleepy case time to spawn its writer chain, then die the way
    // an abnormal launcher does: the stdin pipe closes, which requests
    // cancellation by EOF alone.
    for (let attempt = 0; attempt < 100; attempt++) {
      if ((await readHeartbeat(pulsePath)).pids.length >= 6) break
      await new Promise((resolve) => setTimeout(resolve, 100))
    }
    await child.stdin.end()
  } else if (action === "SIGINT" || action === "SIGTERM" || action === "SIGKILL") {
    await reader.waitFor((line) => line.includes('"allocated_root"'), 15_000)
    for (let attempt = 0; attempt < 100; attempt++) {
      if ((await readHeartbeat(pulsePath)).pids.length >= 6) break
      await new Promise((resolve) => setTimeout(resolve, 100))
    }
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
      // The no_ship counterexample, promoted: the body really resumed after
      // a real timeout and wrote into the root, the root was still there,
      // and removal happened only after the process could not write again.
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
        const result = await runOwnerCase(scenario, workDir, action)
        const label = `${scenario} run ${run}`
        expect(result.exitCode, `${label} stderr: ${result.stderr.slice(-2000)}`).toBe(EXPECTED_EXIT[scenario])
        // A same-prefix sibling of the owner's run roots: removal must stay
        // scoped to the exact owned root, never a prefix match.
        const sibling = await mkdtemp(join(tmpdir(), "cfx-"))
        await Bun.write(join(sibling, "unregistered.txt"), "not owned by this run\n")
        await assertScenario(result, scenario, workDir, label)
        expect(await pathExists(join(sibling, "unregistered.txt")), `${label}: same-prefix sibling was wildcard-deleted`).toBe(true)
        await rm(sibling, { recursive: true, force: true })
        expect(await digestTree(sentinel), `${label}: foreign sentinel changed`).toBe(sentinelBefore)
        // Confine an intentionally retained leftover (failed removal).
        const root = eventAt(result.events, "allocated_root")?.root
        if (root && RETAINING.has(scenario)) {
          await chmod(root, 0o755)
          await rm(root, { recursive: true, force: true })
        }
        // The cancelled cases preserve the conventional signal/EOF statuses.
        if (scenario === "sigint" || scenario === "sigterm") {
          expect(eventAt(result.events, "cancelled")?.via).toBe(scenario === "sigint" ? "SIGINT" : "SIGTERM")
        }
      }
    }
  } finally {
    await rm(workDir, { recursive: true, force: true })
    expect(await digestTree(sentinel), "foreign sentinel changed at teardown").toBe(sentinelBefore)
    await rm(sentinel, { recursive: true, force: true })
  }
}, 480_000)

test("owner SIGKILL leaves an identifiable leftover that only the probe's recovery owner confines", async () => {
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-kill-"))
  const pulsePath = join(workDir, "kill.pulse.txt")
  const child = Bun.spawn(["python3", OWNER, CASE_FILE], {
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
  const reader = new LineReader(child.stderr)
  const allocatedLine = await reader.waitFor((line) => line.includes('"allocated_root"'), 15_000)
  const allocated = JSON.parse(allocatedLine.slice("@@concord-owner ".length)) as OwnerEvent
  for (let attempt = 0; attempt < 100; attempt++) {
    if ((await readHeartbeat(pulsePath)).pids.length >= 6) break
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  // SIGKILL runs no cleanup at all: the owner dies, the leftover stays.
  child.kill("SIGKILL")
  const stderr = await reader.textAfterExit()
  const exited = await child.exited
  const exitCode = exited ?? 128 + (SIGNAL_NUMBERS[child.signalCode ?? "SIGKILL"] ?? 9)
  expect(exitCode).toBe(137)
  expect(allocated.root).toBeDefined()
  const root = allocated.root as string
  const nonce = allocated.nonce as string
  expect(await pathExists(root), "owner SIGKILL should leave the run root in place").toBe(true)
  expect(stderr).not.toContain("removal_error")
  // The inner bun process and the chain survive the owner's death — no
  // cleanup claim is made here — so the heartbeat may still grow until the
  // recovery owner signals exactly them, by nonce, never a reused PID.
  const beforeRecovery = await readHeartbeat(pulsePath)
  expect(beforeRecovery.pids.length, "chain never reached six links").toBeGreaterThanOrEqual(6)
  const recovery = Bun.spawn(["python3", OWNER, "--recover", root, nonce], { stdin: "ignore", stdout: "pipe", stderr: "pipe" })
  const [recoveryStdout, recoveryErr, recoveryExit] = await Promise.all([new Response(recovery.stdout).text(), new Response(recovery.stderr).text(), recovery.exited])
  expect(recoveryExit, `recovery owner failed: ${recoveryStdout}${recoveryErr}`).toBe(0)
  expect(await pathExists(root), "recovery owner left the root").toBe(false)
  await expectProcessGone(allocated.inner_pid, "recovered inner bun process")
  for (const pid of beforeRecovery.pids) await expectProcessGone(pid, "recovered chain link")
  await expectHeartbeatStable(pulsePath, "owner-sigkill after recovery")
  try {
    await rm(workDir, { recursive: true, force: true })
  } finally {
    if (await pathExists(root)) await rm(root, { recursive: true, force: true })
  }
}, 120_000)
