// Linux test-command supervisor for fixture-owned processes. The adapter's
// e2e fixtures reach real command trees — the concord core spawns git through
// internal/store runBoundedGitOutput, which runs git in its own process group
// — so killing the directly spawned process alone never contains the actual
// command descendants, and killing the outer test process group misses them
// too, because those descendants are separately grouped.
//
// Every fixture command therefore runs under this supervisor, which installs
// PR_SET_CHILD_SUBREAPER (kernel semantics: an orphaned descendant is adopted
// by its nearest living subreaper ancestor — see
// https://man7.org/linux/man-pages/man2/PR_SET_CHILD_SUBREAPER.2const.html).
// The supervisor owns exactly its kernel children: the command it spawned plus
// any orphaned descendants the kernel adopted into it. Drain signals each
// owned PID and its process group, waits out a grace interval, escalates to
// SIGKILL, and reaps every owned PID with waitpid(2) before exiting. Ownership
// comes from /proc/self/task/<tid>/children — the kernel's own child list —
// and from waitpid itself, never from process names, path prefixes, PID age,
// or a scan of unrelated processes. No third-party dependency: bun:ffi reaches
// the system libc (prctl, waitpid) and nothing else.
import { dlopen } from "bun:ffi"
import { readdirSync, readFileSync } from "node:fs"

const PR_SET_CHILD_SUBREAPER = 36
const WNOHANG = 1
const GRACE_MS = 800
const POLL_MS = 40

const SIGNAL_NUMBERS: Record<string, number> = {
  SIGHUP: 1, SIGINT: 2, SIGQUIT: 3, SIGILL: 4, SIGTRAP: 5, SIGABRT: 6, SIGBUS: 7,
  SIGFPE: 8, SIGKILL: 9, SIGUSR1: 10, SIGSEGV: 11, SIGUSR2: 12, SIGPIPE: 13,
  SIGALRM: 14, SIGTERM: 15, SIGCHLD: 17, SIGCONT: 18, SIGSTOP: 19, SIGTSTP: 20,
}

const libc = dlopen("libc.so.6", {
  // prctl is variadic in glibc; on linux-amd64 (the only release platform)
  // integer varargs travel in the same registers as fixed arguments, so a
  // fixed five-arg declaration calls it correctly.
  prctl: { args: ["i32", "i64", "i64", "i64", "i64"], returns: "i32" },
  waitpid: { args: ["i32", "ptr", "i32"], returns: "i32" },
})

function becomeSubreaper(): boolean {
  try {
    const prctl = libc.symbols.prctl as (option: number, ...args: number[]) => number
    return prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) === 0
  } catch {
    return false
  }
}

// waitpid on one exact PID. "pending" means still running under WNOHANG;
// "gone" means no such child remains to reap for this process.
type ReapOutcome = "reaped" | "pending" | "gone"
function reapPid(pid: number, block: boolean): ReapOutcome {
  const status = new Int32Array(1)
  const waitpid = libc.symbols.waitpid as (pid: number, status: unknown, flags: number) => number
  const rc = waitpid(pid, status, block ? 0 : WNOHANG)
  if (rc === pid) return "reaped"
  if (rc === 0) return "pending"
  return "gone"
}

// The kernel's list of this process's direct children (which, for a subreaper,
// includes kernel-adopted orphans). Reading only this supervisor's own child
// list keeps the inventory narrowly owned: unrelated processes are never
// enumerated.
function ownChildren(): number[] {
  const pids = new Set<number>()
  let tasks: string[] = []
  try {
    tasks = readdirSync("/proc/self/task")
  } catch {
    return [...pids]
  }
  for (const tid of tasks) {
    try {
      const text = readFileSync(`/proc/self/task/${tid}/children`, "utf8")
      for (const token of text.split(/\s+/)) {
        const pid = Number(token)
        if (Number.isInteger(pid) && pid > 0) pids.add(pid)
      }
    } catch {
      // Task exited mid-read; other task entries still cover the children.
    }
  }
  return [...pids]
}

function signalPid(pid: number, signal: NodeJS.Signals): void {
  try {
    process.kill(pid, signal)
  } catch {
    // Already gone; the reap pass still settles it.
  }
  try {
    process.kill(-pid, signal)
  } catch {
    // No separate process group (or already gone).
  }
}

interface DrainReport {
  argv0: string
  subreaper: boolean
  reason: string
  signalled: number[]
  killed: number[]
  reaped: number[]
}

const subreaperInstalled = becomeSubreaper()
const command = process.argv.slice(2)
if (command.length === 0) {
  process.stderr.write("fixture-supervisor: no command given\n")
  process.exit(64)
}

let commandPid = 0
let drainPromise: Promise<void> | undefined
let commandPromise: Promise<{ exited: number; signalCode: string | null }> = Promise.resolve({ exited: 65, signalCode: null })

async function drainOwnedChildren(report: DrainReport): Promise<void> {
  for (let round = 0; round < 8; round++) {
    const live = ownChildren().filter((pid) => pid !== commandPid)
    for (const pid of live) report.signalled.push(pid)
    for (const pid of live) signalPid(pid, "SIGTERM")
    const deadline = Date.now() + GRACE_MS
    let pending = live
    while (Date.now() < deadline && pending.length > 0) {
      await new Promise((resolve) => setTimeout(resolve, POLL_MS))
      pending = pending.filter((pid) => {
        if (reapPid(pid, false) === "pending") return true
        report.reaped.push(pid)
        return false
      })
    }
    for (const pid of pending) {
      signalPid(pid, "SIGKILL")
      report.killed.push(pid)
    }
    for (const pid of pending) {
      if (reapPid(pid, true) === "reaped") report.reaped.push(pid)
    }
    if (ownChildren().length === 0) return
  }
}

// Every termination path (command exit, supervisor signal, dead parent)
// shares one drain. A later caller must never cut an in-flight drain short:
// it awaits the same completion, then exits with its own status.
async function drainAndExit(reason: string, exitCode: number): Promise<never> {
  if (!drainPromise) {
    drainPromise = (async () => {
      const report: DrainReport = { argv0: command[0], subreaper: subreaperInstalled, reason, signalled: [], killed: [], reaped: [] }
      if (commandPid !== 0) {
        report.signalled.push(commandPid)
        signalPid(commandPid, "SIGTERM")
        const killTimer = setTimeout(() => signalPid(commandPid, "SIGKILL"), GRACE_MS)
        killTimer.unref?.()
        try {
          await commandPromise
        } catch {
          // The command died by signal; Bun observed the exit already.
        }
        clearTimeout(killTimer)
      }
      await drainOwnedChildren(report)
      process.stderr.write(`@@concord-supervisor ${JSON.stringify(report)}\n`)
    })()
  }
  await drainPromise
  process.exit(exitCode)
}

commandPromise = (async () => {
  const child = Bun.spawn(command, {
    cwd: process.env.CONCORD_FIXTURE_CWD || undefined,
    env: process.env,
    stdin: "inherit",
    stdout: "inherit",
    stderr: "inherit",
  })
  commandPid = child.pid
  const exited = (await child.exited) ?? 0
  return { exited, signalCode: child.signalCode ?? null }
})()

// If the owning test process dies by any path — including SIGKILL, where it
// can run nothing at all — this supervisor notices the parent is gone, drains
// the whole command tree, and exits. It never removes fixture roots; root
// ownership stays with the test process's lifecycle registry.
const parentPid = process.ppid
const parentWatch = setInterval(() => {
  try {
    process.kill(parentPid, 0)
  } catch {
    parentWatch.unref()
    void drainAndExit("parent_gone", 143)
  }
}, 250)
parentWatch.unref()

process.on("SIGTERM", () => void drainAndExit("supervisor_sigterm", 143))
process.on("SIGINT", () => void drainAndExit("supervisor_sigint", 130))

try {
  const { exited, signalCode } = await commandPromise
  const status = signalCode ? 128 + (SIGNAL_NUMBERS[signalCode] ?? 0) : exited
  await drainAndExit("command_exited", status)
} catch (error) {
  process.stderr.write(`fixture-supervisor: command failed to start: ${error instanceof Error ? error.message : String(error)}\n`)
  process.exit(65)
}
