import { randomUUID } from "node:crypto"
import fs, { realpathSync } from "node:fs"
import { tmpdir } from "node:os"
import path from "node:path"
import { describe, expect, test } from "bun:test"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"

const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-1",
  lane_id: "implement",
  lane_version: 1,
  lane_digest: "sha256:" + "a".repeat(64),
  work_id: "work-1",
  step_id: "step-1",
  inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, context: "", constraints: [] },
}

const here = async () => process.cwd()

function pendingDirectory() {
  let resolve!: (directory: string) => void
  let reject!: (error: Error) => void
  const promise = new Promise<string>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

describe("dispatch authorization window", () => {
  test("refuses a task call when no window is open for the session", async () => {
    const windows = new DispatchWindows()
    const args = { subagent_type: "general", prompt: "whatever I like", description: "x" }
    await expect(windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here, process.cwd())).rejects.toThrow(/no authorized dispatch/i)
    expect(args.subagent_type).toBe("general")
  })

  // An unauthorized call is refused on the window alone, so the host is never
  // asked where the session runs.
  test("refuses an unauthorized call without reading the session directory", async () => {
    const windows = new DispatchWindows()
    let reads = 0
    const counted = async () => { reads++; return process.cwd() }
    await expect(windows.bind(TASK_TOOL_ID, "session-a", {}, undefined, counted, process.cwd())).rejects.toThrow(/no authorized dispatch/i)
    expect(reads).toBe(0)
  })

  test("replaces caller arguments with the recorded packet", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "general", prompt: "whatever I like", description: "x" }
    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here, process.cwd())
    expect(args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(args.prompt).attempt_id).toBe("attempt-1")
  })

  test("consumes the window exactly once", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" }, undefined, here, process.cwd())
    await expect(
      windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" }, undefined, here, process.cwd()),
    ).rejects.toThrow(/no authorized dispatch/i)
  })

  test("scopes a window to the session that requested it", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    await expect(
      windows.bind(TASK_TOOL_ID, "session-b", { subagent_type: "x", prompt: "y", description: "z" }, undefined, here, process.cwd()),
    ).rejects.toThrow(/no authorized dispatch/i)
    const args = { subagent_type: "x", prompt: "y", description: "z" }
    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here, process.cwd())
    expect(args.subagent_type).toBe("concord-implement")
  })

  test("ignores tools other than the task tool", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "general", prompt: "untouched" }
    await windows.bind("bash", "session-a", args, undefined, here, process.cwd())
    expect(args.prompt).toBe("untouched")
    const taskArgs = { subagent_type: "x", prompt: "y", description: "z" }
    await windows.bind(TASK_TOOL_ID, "session-a", taskArgs, undefined, here, process.cwd())
    expect(taskArgs.subagent_type).toBe("concord-implement")
  })

  test("refuses a second open window for one session", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    expect(() => windows.open("session-a", packet, "", process.cwd())).toThrow(/already holds an open dispatch/i)
  })

  // The in-flight refusal is the wedge an agent cannot cross: the record has
  // no settle left to wait for, so the refusal must carry the route that
  // closes the attempt and releases the retained record.
  test("the in-flight refusal names the worker_abandon route and the retained identity", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" }, "call-1", here, process.cwd())
    const refusal = (() => {
      try {
        windows.open("session-a", packet, "", process.cwd())
        return ""
      } catch (error) {
        return error instanceof Error ? error.message : String(error)
      }
    })()
    expect(refusal).toContain("worker_abandon")
    expect(refusal).toContain("attempt-1")
    expect(refusal).toContain("implement")
    expect(refusal).toContain("work-1")
    expect(refusal).toContain("session-a")
  })

  test("refuses to resume a prior worker session", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "x", prompt: "y", description: "z", task_id: "session-prior" }
    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here, process.cwd())
    expect(args.task_id).toBeUndefined()
  })

  // The session readback and the execution instance report the claimed
  // worktree while the host process keeps its launch directory: the process
  // directory is not execution evidence, so the bind proceeds.
  test("binds when the session directory differs from the host process directory", async () => {
    const windows = new DispatchWindows()
    const claimed = realpathSync(tmpdir())
    expect(claimed).not.toBe(process.cwd())
    windows.open("session-a", packet, "", claimed)
    const args = { subagent_type: "general", prompt: "model input", description: "model task" }

    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, async () => claimed, claimed)
    expect(args.subagent_type).toBe("concord-implement")
  })

  // A session that moved between authorization and the Task call would start
  // the worker outside the worktree the core authorized.
  test("refuses a worker when the session moved away from the claimed worktree", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "general", prompt: "model input", description: "model task" }

    await expect(
      windows.bind(TASK_TOOL_ID, "session-a", args, undefined, async () => realpathSync(tmpdir()), process.cwd()),
    ).rejects.toThrow(/does not match the active claimed worktree/i)
    expect(args.subagent_type).toBe("general")
    expect(windows.has("session-a")).toBe(false)
  })

  test("does not expose paths in a directory mismatch", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const result = await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, async () => realpathSync(tmpdir()), process.cwd()).catch(error => String(error))

    expect(result).not.toContain(process.cwd())
    expect(result).not.toContain(realpathSync(tmpdir()))
  })

  test("closes the window when the bind-time directory read fails", async () => {
    const windows = new DispatchWindows()
    const secretPath = path.join(process.cwd(), "private-session-path")
    windows.open("session-a", packet, "", process.cwd())

    await expect(
      windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, async () => {
        throw new Error(`cannot read ${secretPath}`)
      }, process.cwd()),
    ).rejects.toThrow(/could not resolve the host session directory/i)
    expect(windows.has("session-a")).toBe(false)
  })

  test("refuses a window without a resolvable worker directory", () => {
    for (const workerDirectory of [undefined, `${process.cwd()}/concord-dispatch-nonexistent-${randomUUID()}`]) {
      const windows = new DispatchWindows()
      expect(() => windows.open("session-a", packet, "", workerDirectory)).toThrow(/resolvable worker directory/)
      expect(windows.has("session-a")).toBe(false)
    }
  })

  // Binding must never relocate the host process. A dispatch that changed the
  // process directory would move every other session sharing this process.
  test("keeps the host process directory unchanged when binding a worker", async () => {
    const directory = process.cwd()
    const before = process.cwd()
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", directory)
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, here, process.cwd())

    expect(process.cwd()).toBe(before)
  })

  test("pins the resolved claimed directory before the host task call", async () => {
    const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
    const claimed = path.join(root, "claimed")
    const other = path.join(root, "other")
    const alias = path.join(root, "alias")
    for (const directory of [claimed, other]) fs.mkdirSync(directory)
    fs.symlinkSync(claimed, alias)
    try {
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", alias)
      fs.unlinkSync(alias)
      fs.symlinkSync(other, alias)

      await expect(
        windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, async () => alias, claimed),
      ).rejects.toThrow(/does not match the active claimed worktree/i)
    } finally {
      fs.rmSync(root, { recursive: true, force: true })
    }
  })

  // The execution directory is a required bind input: the host runs the Task
  // child in the plugin instance directory, so a missing answer or a value
  // that resolves to no directory on disk proves nothing about where the
  // worker would run. The refusal discards the window and records no attempt.
  test("refuses and discards when the execution directory is missing or unresolvable", async () => {
    for (const executionDirectory of [undefined, "", "relative/claim", `${process.cwd()}/concord-execution-nonexistent-${randomUUID()}`, 42]) {
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", process.cwd())
      const args = { subagent_type: "general", prompt: "model input", description: "model task" }
      await expect(
        windows.bind(TASK_TOOL_ID, "session-a", args, "call-unresolved", here, executionDirectory),
      ).rejects.toThrow(/execution directory/i)
      expect(args.subagent_type).toBe("general")
      expect(windows.has("session-a")).toBe(false)
      expect(windows.inFlightAttempt("session-a")).toBeNull()
    }
  })

  // A symlink alias to the claimed worktree resolves to the same canonical
  // identity, so binding through it proves the same execution placement. Once
  // the alias is retargeted it resolves elsewhere and the bind refuses.
  test("binds through a symlink alias to the claimed worktree and refuses a retargeted alias", async () => {
    const root = fs.mkdtempSync(path.join(tmpdir(), "concord-execution-"))
    const claimed = path.join(root, "claimed")
    const other = path.join(root, "other")
    const alias = path.join(root, "alias")
    for (const directory of [claimed, other]) fs.mkdirSync(directory)
    fs.symlinkSync(claimed, alias)
    try {
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", claimed)
      const args = { subagent_type: "general", prompt: "model input", description: "model task" }
      await windows.bind(TASK_TOOL_ID, "session-a", args, "call-alias", async () => claimed, alias)
      expect(args.subagent_type).toBe("concord-implement")

      windows.open("session-b", packet, "", claimed)
      fs.unlinkSync(alias)
      fs.symlinkSync(other, alias)
      const refused = { subagent_type: "general", prompt: "model input", description: "model task" }
      await expect(
        windows.bind(TASK_TOOL_ID, "session-b", refused, "call-retargeted", async () => claimed, alias),
      ).rejects.toThrow(/does not match the active claimed worktree/i)
      expect(refused.subagent_type).toBe("general")
      expect(windows.has("session-b")).toBe(false)
      expect(windows.inFlightAttempt("session-b")).toBeNull()
      windows.open("session-c", packet, "", claimed)
      const message = await windows.bind(TASK_TOOL_ID, "session-c", refused, "call-private-path", async () => claimed, alias).catch(error => String(error))
      expect(message).toMatch(/execution identity sha256:/)
      expect(message).not.toContain(root)
    } finally {
      fs.rmSync(root, { recursive: true, force: true })
    }
  })

  // The session-directory await can retarget a symlinked execution directory
  // after the pre-await answers passed, so only a check that reads the
  // execution directory after the await is authoritative.
  test("refuses when the execution alias retargets during the session-directory await", async () => {
    const root = fs.mkdtempSync(path.join(tmpdir(), "concord-retarget-await-"))
    const claimed = path.join(root, "claimed")
    const other = path.join(root, "other")
    const alias = path.join(root, "alias")
    for (const directory of [claimed, other]) fs.mkdirSync(directory)
    fs.symlinkSync(claimed, alias)
    try {
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", claimed)
      const args = { subagent_type: "general", prompt: "model input", description: "model task" }
      await expect(
        windows.bind(TASK_TOOL_ID, "session-a", args, "call-retarget-during-await", async () => {
          fs.unlinkSync(alias)
          fs.symlinkSync(other, alias)
          return claimed
        }, alias),
      ).rejects.toThrow(/does not match the active claimed worktree/i)
      expect(args.subagent_type).toBe("general")
      expect(windows.has("session-a")).toBe(false)
      expect(windows.inFlightAttempt("session-a")).toBeNull()
    } finally {
      fs.rmSync(root, { recursive: true, force: true })
    }
  })

  // The process directory is not the instance directory: a host process that
  // stays in the trunk still executes its Task children in the instance
  // directory, so a claimed-instance bind proceeds without relocating it.
  test("binds the claimed execution instance while the host process runs in the trunk", async () => {
    const trunk = fs.mkdtempSync(path.join(tmpdir(), "concord-process-trunk-"))
    const claimed = fs.mkdtempSync(path.join(tmpdir(), "concord-process-claim-"))
    const previousDirectory = process.cwd()
    try {
      process.chdir(trunk)
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", claimed)
      const args = { subagent_type: "general", prompt: "model input", description: "model task" }
      await windows.bind(TASK_TOOL_ID, "session-a", args, "call-trunk-process", async () => claimed, claimed)
      expect(args.subagent_type).toBe("concord-implement")
      expect(process.cwd()).toBe(trunk)
    } finally {
      process.chdir(previousDirectory)
      fs.rmSync(trunk, { recursive: true, force: true })
      fs.rmSync(claimed, { recursive: true, force: true })
    }
  })
})

describe("in-flight retention across the host task call", () => {
  test("bind moves the record to in flight and completion takes it once", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "sha256:" + "c".repeat(64), process.cwd())
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "x" }, undefined, here, process.cwd())
    expect(windows.has("session-a")).toBe(false)

    const record = windows.takeInFlight("session-a")
    expect(record?.packet.attempt_id).toBe("attempt-1")
    expect(record?.packetDigest).toBe("sha256:" + "c".repeat(64))
    // One authorization admits one result.
    expect(windows.takeInFlight("session-a")).toBeNull()
  })

  test("a session with no dispatch has nothing in flight", () => {
    expect(new DispatchWindows().takeInFlight("session-none")).toBeNull()
  })
})

describe("retained attempt release", () => {
  test("releases an abandoned open window before any Task can consume it", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "sha256:" + "c".repeat(64), process.cwd())
    expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(true)
    expect(windows.has("session-a")).toBe(false)
    await expect(windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "concord-explore", prompt: "utility" }, "call-after-abandon", here, process.cwd())).rejects.toThrow(/no authorized dispatch window/i)
    expect(windows.inFlightAttempt("session-a")).toBeNull()
    expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(false)
  })

  test("does not release another session, attempt, or lane's open window", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    expect(windows.releaseRetained("session-b", "attempt-1", "implement")).toBe(false)
    expect(windows.releaseRetained("session-a", "attempt-other", "implement")).toBe(false)
    expect(windows.releaseRetained("session-a", "attempt-1", "research")).toBe(false)
    expect(windows.has("session-a")).toBe(true)
  })

  const bindInFlight = async (windows: DispatchWindows, session = "session-a") => {
    windows.open(session, packet, "sha256:" + "c".repeat(64), process.cwd())
    await windows.bind(TASK_TOOL_ID, session, { subagent_type: "x", prompt: "y", description: "z" }, "call-cancel", here, process.cwd())
  }

  test("drops the retained record when the caller names its attempt identity", async () => {
    const windows = new DispatchWindows()
    await bindInFlight(windows)
    expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(true)
    expect(windows.inFlight("session-a", "call-cancel")).toBeNull()
    // The recovered session can dispatch again.
    expect(() => windows.open("session-a", packet, "", process.cwd())).not.toThrow()
  })

  test("refuses a foreign attempt identity and leaves the guard intact", async () => {
    const windows = new DispatchWindows()
    await bindInFlight(windows)
    expect(windows.releaseRetained("session-a", "attempt-2", "implement")).toBe(false)
    expect(windows.releaseRetained("session-a", "attempt-1", "research")).toBe(false)
    expect(windows.inFlight("session-a", "call-cancel")).not.toBeNull()
    expect(() => windows.open("session-a", packet, "", process.cwd())).toThrow()
  })

  test("refuses while a settlement is in progress", async () => {
    const windows = new DispatchWindows()
    await bindInFlight(windows)
    expect(windows.claimSettlement("session-a", "call-cancel")).not.toBeNull()
    expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(false)
    expect(windows.inFlight("session-a", "call-cancel")).not.toBeNull()
  })

  // A refused write leaves the record retained with its claim converted to the
  // refused state: no settle or completion route may re-attempt, and only the
  // worker_abandon release clears the retention.
  test("a refused settlement blocks re-settle and completion but not the release", async () => {
    const windows = new DispatchWindows()
    await bindInFlight(windows)
    expect(windows.claimSettlement("session-a", "call-cancel")).not.toBeNull()
    windows.refuseSettlement("session-a")
    expect(windows.claimSettlement("session-a", "call-cancel")).toBeNull()
    expect(windows.takeInFlight("session-a")).toBeNull()
    expect(windows.inFlight("session-a", "call-cancel")).not.toBeNull()
    expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(true)
    expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(false)
    // The recovered session can dispatch again.
    expect(() => windows.open("session-a", packet, "", process.cwd())).not.toThrow()
  })

  test("reports nothing to release for a session without a retained record", () => {
    expect(new DispatchWindows().releaseRetained("session-none", "attempt-1", "implement")).toBe(false)
  })

  for (const revocation of ["release", "close"] as const) {
    test(`${revocation} during an awaited bind does not resurrect the window`, async () => {
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", process.cwd())
      const directory = pendingDirectory()
      const args = { subagent_type: "general", prompt: "unchanged", description: "unchanged", task_id: "unchanged" }
      const binding = windows.bind(TASK_TOOL_ID, "session-a", args, "call-after-abandon", () => directory.promise, process.cwd())
      if (revocation === "release") {
        expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(true)
      } else {
        windows.close("session-a")
      }
      expect(windows.has("session-a")).toBe(false)
      directory.resolve(process.cwd())
      await expect(binding).rejects.toThrow(/no authorized dispatch window/i)
      expect(args).toEqual({ subagent_type: "general", prompt: "unchanged", description: "unchanged", task_id: "unchanged" })
      expect(windows.inFlightAttempt("session-a")).toBeNull()
    })
  }

  for (const resolution of ["matching", "mismatch", "failure"] as const) {
    test(`an old bind preserves a replacement window after ${resolution} directory resolution`, async () => {
      const windows = new DispatchWindows()
      windows.open("session-a", packet, "", process.cwd())
      const directory = pendingDirectory()
      const args: Record<string, unknown> = {}
      const binding = windows.bind(TASK_TOOL_ID, "session-a", args, "old-call", () => directory.promise, process.cwd())
      expect(windows.releaseRetained("session-a", "attempt-1", "implement")).toBe(true)
      // Even equal packet identities name distinct single-use authorizations.
      const replacement = { ...packet, inputs: { ...packet.inputs, task: "replacement authorization" } }
      windows.open("session-a", replacement, "", process.cwd())
      if (resolution === "failure") {
        directory.reject(new Error("synthetic resolver failure"))
      } else {
        directory.resolve(resolution === "matching" ? process.cwd() : realpathSync(tmpdir()))
      }
      await expect(binding).rejects.toThrow(/no authorized dispatch window|could not resolve/i)
      expect(args).toEqual({})
      expect(windows.has("session-a")).toBe(true)
      expect(windows.inFlightAttempt("session-a")).toBeNull()
      const replacementArgs: Record<string, unknown> = {}
      await windows.bind(TASK_TOOL_ID, "session-a", replacementArgs, "replacement-call", here, process.cwd())
      expect(JSON.parse(replacementArgs.prompt as string).inputs.task).toBe("replacement authorization")
      expect(windows.inFlight("session-a", "replacement-call")?.packet).toBe(replacement)
    })
  }

  test("concurrent binds consume one window once and preserve the winning in-flight record", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const directory = pendingDirectory()
    const args: Record<string, unknown> = {}
    const binding = windows.bind(TASK_TOOL_ID, "session-a", args, "losing-call", () => directory.promise, process.cwd())
    const winnerArgs: Record<string, unknown> = {}
    await windows.bind(TASK_TOOL_ID, "session-a", winnerArgs, "winning-call", here, process.cwd())
    expect(windows.inFlight("session-a", "winning-call")?.packet).toBe(packet)
    directory.resolve(process.cwd())
    await expect(binding).rejects.toThrow(/no authorized dispatch window/i)
    expect(args).toEqual({})
    expect(windows.inFlight("session-a", "winning-call")?.packet).toBe(packet)
    expect(windows.inFlight("session-a", "losing-call")).toBeNull()
    expect(windows.has("session-a")).toBe(false)
  })
})
