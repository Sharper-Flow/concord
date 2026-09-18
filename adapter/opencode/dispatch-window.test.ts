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
  inputs: { task: "do the bounded thing", context: "", constraints: [] },
}

const here = async () => process.cwd()

describe("dispatch authorization window", () => {
  test("refuses a task call when no window is open for the session", async () => {
    const windows = new DispatchWindows()
    const args = { subagent_type: "general", prompt: "whatever I like", description: "x" }
    await expect(windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here)).rejects.toThrow(/no authorized dispatch/i)
    expect(args.subagent_type).toBe("general")
  })

  // An unauthorized call is refused on the window alone, so the host is never
  // asked where the session runs.
  test("refuses an unauthorized call without reading the session directory", async () => {
    const windows = new DispatchWindows()
    let reads = 0
    const counted = async () => { reads++; return process.cwd() }
    await expect(windows.bind(TASK_TOOL_ID, "session-a", {}, undefined, counted)).rejects.toThrow(/no authorized dispatch/i)
    expect(reads).toBe(0)
  })

  test("replaces caller arguments with the recorded packet", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "general", prompt: "whatever I like", description: "x" }
    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here)
    expect(args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(args.prompt).attempt_id).toBe("attempt-1")
  })

  test("consumes the window exactly once", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" }, undefined, here)
    await expect(
      windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" }, undefined, here),
    ).rejects.toThrow(/no authorized dispatch/i)
  })

  test("scopes a window to the session that requested it", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    await expect(
      windows.bind(TASK_TOOL_ID, "session-b", { subagent_type: "x", prompt: "y", description: "z" }, undefined, here),
    ).rejects.toThrow(/no authorized dispatch/i)
    const args = { subagent_type: "x", prompt: "y", description: "z" }
    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here)
    expect(args.subagent_type).toBe("concord-implement")
  })

  test("ignores tools other than the task tool", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "general", prompt: "untouched" }
    await windows.bind("bash", "session-a", args, undefined, here)
    expect(args.prompt).toBe("untouched")
    const taskArgs = { subagent_type: "x", prompt: "y", description: "z" }
    await windows.bind(TASK_TOOL_ID, "session-a", taskArgs, undefined, here)
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
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" }, "call-1", here)
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
    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, here)
    expect(args.task_id).toBeUndefined()
  })

  // The host process keeps the directory it launched in, while a session moves
  // to its claimed worktree. Comparing the claim against the process directory
  // refused every dispatch from a host launched anywhere else, although Task
  // creates the worker session in the session's directory and never in the
  // process directory.
  test("binds when the session directory differs from the host process directory", async () => {
    const windows = new DispatchWindows()
    const claimed = realpathSync(tmpdir())
    expect(claimed).not.toBe(process.cwd())
    windows.open("session-a", packet, "", claimed)
    const args = { subagent_type: "general", prompt: "model input", description: "model task" }

    await windows.bind(TASK_TOOL_ID, "session-a", args, undefined, async () => claimed)
    expect(args.subagent_type).toBe("concord-implement")
  })

  // A session that moved between authorization and the Task call would start
  // the worker outside the worktree the core authorized.
  test("refuses a worker when the session moved away from the claimed worktree", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const args = { subagent_type: "general", prompt: "model input", description: "model task" }

    await expect(
      windows.bind(TASK_TOOL_ID, "session-a", args, undefined, async () => realpathSync(tmpdir())),
    ).rejects.toThrow(/does not match the active claimed worktree/i)
    expect(args.subagent_type).toBe("general")
    expect(windows.has("session-a")).toBe(false)
  })

  test("does not expose paths in a directory mismatch", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "", process.cwd())
    const result = await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, async () => realpathSync(tmpdir())).catch(error => String(error))

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
      }),
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
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, here)

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
        windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "model input" }, undefined, async () => alias),
      ).rejects.toThrow(/does not match the active claimed worktree/i)
    } finally {
      fs.rmSync(root, { recursive: true, force: true })
    }
  })
})

describe("in-flight retention across the host task call", () => {
  test("bind moves the record to in flight and completion takes it once", async () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "sha256:" + "c".repeat(64), process.cwd())
    await windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "x" }, undefined, here)
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
  const bindInFlight = async (windows: DispatchWindows, session = "session-a") => {
    windows.open(session, packet, "sha256:" + "c".repeat(64), process.cwd())
    await windows.bind(TASK_TOOL_ID, session, { subagent_type: "x", prompt: "y", description: "z" }, "call-cancel", here)
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

  test("reports nothing to release for a session without a retained record", () => {
    expect(new DispatchWindows().releaseRetained("session-none", "attempt-1", "implement")).toBe(false)
  })
})
