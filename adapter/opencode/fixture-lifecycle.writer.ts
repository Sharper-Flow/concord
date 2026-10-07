// Regression helper for descendant containment: a fixture command that moves
// itself into its own process group, spawns a descendant that does the same,
// ignores cooperative termination (SIGTERM), and keeps writing heartbeats —
// the shape a real command tree presents when a separately grouped git
// descendant outlives the process that spawned it. The supervisor must adopt,
// signal, kill, and reap this descendant before the fixture root goes away.
import { dlopen } from "bun:ffi"
import { appendFileSync } from "node:fs"

const libc = dlopen("libc.so.6", {
  setpgid: { args: ["i32", "i32"], returns: "i32" },
})

function ownProcessGroup(): void {
  try {
    ;(libc.symbols.setpgid as (pid: number, pgid: number) => number)(0, 0)
  } catch {
    // Staying in the parent's group still exercises the drain path.
  }
}

const mode = process.argv[2] ?? ""
const heartbeatPath = process.argv[3] ?? ""

if (mode === "grouped-child") {
  ownProcessGroup()
  // Ignore cooperative termination: only SIGKILL can stop this writer, which
  // is exactly the escalation the supervisor's drain must prove.
  process.on("SIGTERM", () => {})
  process.on("SIGINT", () => {})
  let beats = 0
  const timer = setInterval(() => {
    beats++
    try {
      appendFileSync(heartbeatPath, `${process.pid} ${Date.now()} ${beats}\n`)
    } catch {
      // The heartbeat file went away; keep running so the drain still has a
      // live descendant to contain.
    }
  }, 25)
  timer.unref()
  setInterval(() => {}, 60_000)
} else if (mode === "grouped-parent") {
  ownProcessGroup()
  const child = Bun.spawn([process.execPath, import.meta.path, "grouped-child", heartbeatPath], {
    stdin: "ignore",
    stdout: "ignore",
    stderr: "ignore",
  })
  child.unref()
  // Exit while the separately grouped descendant keeps running and writing:
  // the supervisor must adopt it as a kernel orphan and drain it.
  setTimeout(() => process.exit(0), 150)
} else {
  process.stderr.write("fixture-lifecycle.writer: unknown mode\n")
  process.exit(64)
}
