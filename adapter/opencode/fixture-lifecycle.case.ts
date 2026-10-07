// Child-process driver for the fixture-lifecycle regression matrix. The
// parent test spawns this script once per lifecycle case; each run allocates a
// real fixture root, spawns a real fixture-owned child, and then exits exactly
// the way that lifecycle case ends — including signal deaths — so the parent
// observes both the removal outcome and the preserved exit status. No model
// call happens anywhere in this matrix.
//
// Scenarios and their exit statuses:
//   success             explicit release, exit 0
//   assertion-failure   release through the failure path, exit 1
//   timeout             no release; the armed deadline removes the root, exit 1
//   teardown-exception  no release; the exit sweep removes the root, exit 1
//   cooperative-abort   AbortSignal cancels the child, release, exit 0
//   sigint              signal handler sweeps, exit 130
//   sigterm             signal handler sweeps, exit 143
//   sigkill             no in-process cleanup is possible, exit 137
//   removal-error       removal fails visibly, root retained, exit 1
import { chmod, mkdtemp } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import {
  allocateFixtureRoot,
  fixtureJournal,
  releaseFixtureRoot,
  runFixtureProcess,
  trackFixtureChild,
} from "./fixture-lifecycle"

const TIMEOUT_CASE_DEADLINE_MS = 300
const ORDINARY_CASE_DEADLINE_MS = 60_000

function emit(value: Record<string, unknown>): void {
  process.stdout.write(`@@${JSON.stringify(value)}\n`)
}

async function main(): Promise<number> {
  const scenario = process.argv[2] ?? ""
  const prefix = `concord-lifecycle-${scenario}-`
  const root = await allocateFixtureRoot(prefix, {
    timeoutMs: scenario === "timeout" ? TIMEOUT_CASE_DEADLINE_MS : ORDINARY_CASE_DEADLINE_MS,
  })
  // A fixture-owned child that outlives the case body: every cleanup path must
  // cancel and drain it before (or while) removing the root.
  const child = Bun.spawn([process.execPath, "-e", "setTimeout(() => {}, 30_000)"], {
    stdin: "ignore",
    stdout: "ignore",
    stderr: "ignore",
  })
  trackFixtureChild(child)
  // An unregistered same-prefix sibling: the sweep must remove the registered
  // root only, never a wildcard prefix match.
  const sibling = await mkdtemp(join(tmpdir(), prefix))
  await Bun.write(join(sibling, "unregistered.txt"), "not owned by this run\n")
  emit({ scenario, root, sibling, pid: process.pid, childPid: child.pid })

  switch (scenario) {
    case "success": {
      const quick = await runFixtureProcess([process.execPath, "-e", "process.stdout.write('ok')"])
      if (quick.exitCode !== 0 || quick.stdout !== "ok") throw new Error(`quick child failed: ${quick.exitCode} ${quick.stderr}`)
      await releaseFixtureRoot(root)
      return 0
    }
    case "assertion-failure": {
      try {
        throw new Error("synthetic assertion failure")
      } finally {
        // The finally still runs on this path, as it does under bun:test —
        // but through the lifecycle release, not a bare rm.
        await releaseFixtureRoot(root, "assertion_failure")
      }
    }
    case "timeout": {
      // The test hangs past its declared timeout: no release call ever runs.
      // Stay alive past the armed deadline so the deadline path itself — the
      // one a real timed-out test depends on — performs the removal, then exit
      // failing the way a timed-out run does.
      await new Promise((resolve) => setTimeout(resolve, TIMEOUT_CASE_DEADLINE_MS + 400))
      return 1
    }
    case "teardown-exception": {
      try {
        await runFixtureProcess([process.execPath, "-e", "process.exit(0)"])
      } finally {
        // The teardown itself throws before any release: the exit sweep owns
        // the removal from here.
        throw new Error("synthetic teardown exception before release")
      }
    }
    case "cooperative-abort": {
      const controller = new AbortController()
      const late = runFixtureProcess([process.execPath, "-e", "setTimeout(() => {}, 30_000)"], "", { signal: controller.signal })
      await new Promise((resolve) => setTimeout(resolve, 100))
      controller.abort()
      const aborted = await late
      if (aborted.exitCode === 0) throw new Error("aborted child must not exit cleanly")
      // The long-lived tracked child from the top of the case is cancelled and
      // drained by the release below, not by this abort.
      await releaseFixtureRoot(root, "cooperative_abort")
      return 0
    }
    case "sigint": {
      process.kill(process.pid, "SIGINT")
      await new Promise(() => {})
      return 0
    }
    case "sigterm": {
      process.kill(process.pid, "SIGTERM")
      await new Promise(() => {})
      return 0
    }
    case "sigkill": {
      // SIGKILL runs no handler: this leftover is exactly the case the module
      // does not claim to reclaim, and the parent test identifies it by the
      // ownership marker and confines it.
      process.kill(process.pid, "SIGKILL")
      await new Promise(() => {})
      return 0
    }
    case "removal-error": {
      await Bun.write(join(root, "evidence.txt"), "collected evidence\n")
      await chmod(root, 0o500)
      const release = await releaseFixtureRoot(root, "removal_error_case")
      if (release.removed) throw new Error("removal was expected to fail against a read-only root")
      return 1
    }
    default:
      throw new Error(`unknown scenario: ${scenario || "(none)"}`)
  }
}

main().then((code) => {
  emit({ journal: fixtureJournal() })
  process.exit(code)
}).catch((error) => {
  process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`)
  emit({ journal: fixtureJournal() })
  process.exit(1)
})
