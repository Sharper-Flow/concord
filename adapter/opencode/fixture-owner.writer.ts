// Regression helper for owner-level descendant containment. The shapes here
// are the ones an in-process or bounded-round design cannot contain:
//
//   chain N  — a separately SESSIONED (setsid) SIGTERM-ignoring link that
//              spawns the next link and keeps heartbeating. Only SIGKILL
//              stops a link, and each link becomes visible to the owner only
//              after its parent dies, so draining the chain needs as many
//              kill/reap rounds as there are links — no bounded round count
//              can promise it.
//   daemon   — a double-forked, sessioned SIGTERM-ignoring writer that is
//              orphaned immediately, while the inner suite still runs, and
//              must be adopted by the owner mid-run and killed before
//              removal.
import { dlopen } from "bun:ffi"
import { appendFileSync } from "node:fs"

const libc = dlopen("libc.so.6", {
  setsid: { args: [], returns: "i32" },
})

function ownSession(): void {
  try {
    ;(libc.symbols.setsid as () => number)()
  } catch {
    // Remaining in the parent's session still exercises the drain path.
  }
}

const mode = process.argv[2] ?? ""
const heartbeatPath = process.argv[3] ?? ""
const remaining = Number(process.argv[4] ?? "0")

function beat(): void {
  const timer = setInterval(() => {
    try {
      appendFileSync(heartbeatPath, `${process.pid} ${Date.now()}\n`)
    } catch {
      // The heartbeat file went away; keep running so the drain still has a
      // live descendant to contain.
    }
  }, 25)
  timer.unref()
  setInterval(() => {}, 60_000)
}

function ignoreSignals(): void {
  process.on("SIGTERM", () => {})
  process.on("SIGINT", () => {})
}

if (mode === "chain") {
  ownSession()
  ignoreSignals()
  beat()
  if (remaining > 0) {
    const child = Bun.spawn([process.execPath, import.meta.path, "chain", heartbeatPath, String(remaining - 1)], {
      stdin: "ignore",
      stdout: "ignore",
      stderr: "ignore",
    })
    child.unref()
  }
} else if (mode === "daemon") {
  // Double fork: the middle process exits at once, so the writer is orphaned
  // while the inner suite still runs and the owner must adopt it mid-run.
  const child = Bun.spawn([process.execPath, import.meta.path, "daemon-middle", heartbeatPath, "0"], {
    stdin: "ignore",
    stdout: "ignore",
    stderr: "ignore",
  })
  child.unref()
} else if (mode === "daemon-middle") {
  ownSession()
  const child = Bun.spawn([process.execPath, import.meta.path, "daemon-writer", heartbeatPath, "0"], {
    stdin: "ignore",
    stdout: "ignore",
    stderr: "ignore",
  })
  child.unref()
  setTimeout(() => process.exit(0), 50)
} else if (mode === "daemon-writer") {
  ownSession()
  ignoreSignals()
  beat()
} else {
  process.stderr.write("fixture-owner.writer: unknown mode\n")
  process.exit(64)
}
