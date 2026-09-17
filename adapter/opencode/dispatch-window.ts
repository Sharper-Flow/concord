// CD-0102 D1/D2: the single-use authorization window that binds one typed
// dispatch to one native Task call.
//
// The model issues the Task call, not Concord, so the arguments it composes
// carry no provenance the core can trust. A window is opened only by an
// authorized `dispatch_worker` action. The next Task call from the same session
// has its agent selection and prompt overwritten by the recorded packet, and the
// window closes. A Task call with no open window fails.
import { createHash } from "node:crypto"
import fs from "node:fs"
import path from "node:path"
import type { AgentLanePacket } from "./dispatch"

// The host renders the worker card only for the tool with this id, so the lane
// runs under it or it runs without operator progress, navigation, and cancel.
export const TASK_TOOL_ID = "task"

// The lane agent names installed under .opencode/agents/ carry this prefix.
const LANE_AGENT_PREFIX = "concord-"

export class DispatchWindowError extends Error {}

// An authorized attempt in flight: the packet and digest captured before the
// host starts the worker. Completion needs these values after the host runs
// the worker, so none can be held on a stack.
export interface DispatchRecord {
  packet: AgentLanePacket
  packetDigest: string
  workerDirectory: string
  workerDirectoryIdentity: string
  callID?: string
}

interface MutableToolArgs {
  subagent_type?: unknown
  prompt?: unknown
  description?: unknown
  task_id?: unknown
  [key: string]: unknown
}

// The authorized `dispatch_worker` path opens a window and the plugin's
// `tool.execute.before` hook consumes it, so both reach the same instance
// through this module. The plugin module graph is per OpenCode instance, which
// scopes the windows to one running host.
export const dispatchWindows = (): DispatchWindows => shared

export class DispatchWindows {
  // One window per session. A session that already holds one cannot open a
  // second, because two open windows would let one authorized dispatch start
  // whichever worker the next call happens to name.
  readonly #open = new Map<string, DispatchRecord>()
  // An attempt the host is running now. The Task call consumed its window, and
  // completion still needs the packet and the digest the core recorded.
  readonly #inFlight = new Map<string, DispatchRecord>()
  readonly #settling = new Set<string>()

  open(sessionID: string, packet: AgentLanePacket, packetDigest = "", workerDirectory?: string, pinnedWorkerDirectory?: string): void {
    if (this.#open.has(sessionID) || this.#inFlight.has(sessionID)) {
      throw new DispatchWindowError(`session ${sessionID} already holds an open dispatch window or an in-flight attempt`)
    }
    // The dispatch path resolves the host-reported directory before
    // authorization. Verify that the same caller path still resolves to that
    // pinned identity after authorization, before the window can open.
    const canonicalWorkerDirectory = pinnedWorkerDirectory ?? canonicalDirectory(workerDirectory)
    if (canonicalWorkerDirectory === null) {
      throw new DispatchWindowError("worker dispatch requires a non-empty, resolvable worker directory")
    }
    if (pinnedWorkerDirectory !== undefined) {
      const mismatch = dispatchDirectoryMismatch(canonicalWorkerDirectory, workerDirectory)
      if (mismatch) throw new DispatchWindowError(mismatch)
    }
    const workerDirectoryIdentity = canonicalDirectoryIdentity(canonicalWorkerDirectory)
    if (workerDirectoryIdentity === null) {
      throw new DispatchWindowError("worker dispatch requires a resolvable worker directory identity")
    }
    this.#open.set(sessionID, { packet, packetDigest, workerDirectory: canonicalWorkerDirectory, workerDirectoryIdentity })
  }

  // close discards a window whose dispatch failed before the worker started, so
  // a refused attempt does not leave an authorization the next call can consume.
  close(sessionID: string): void {
    this.#open.delete(sessionID)
  }

  has(sessionID: string): boolean {
    return this.#open.has(sessionID)
  }

  // takeInFlight hands the running attempt to the completion path once. A second
  // completion for one dispatch finds nothing, so a result cannot be admitted
  // twice against a single authorization.
  takeInFlight(sessionID: string, callID?: string): DispatchRecord | null {
    if (this.#settling.has(sessionID)) return null
    const record = this.#inFlight.get(sessionID)
    if (!record) return null
    if (callID !== undefined && record.callID !== undefined && record.callID !== callID) return null
    this.#inFlight.delete(sessionID)
    return record
  }

  inFlight(sessionID: string, callID: string): DispatchRecord | null {
    const record = this.#inFlight.get(sessionID)
    return record?.callID === callID ? record : null
  }

  claimSettlement(sessionID: string, callID: string): DispatchRecord | null {
    if (this.#settling.has(sessionID)) return null
    const record = this.inFlight(sessionID, callID)
    if (!record) return null
    this.#settling.add(sessionID)
    return record
  }

  finishSettlement(sessionID: string, callID: string): void {
    if (!this.inFlight(sessionID, callID)) return
    this.#inFlight.delete(sessionID)
    this.#settling.delete(sessionID)
  }

  // bind is the `tool.execute.before` body. It mutates the caller's arguments in
  // place, which is the only channel the host hook contract offers.
  //
  // resolveSessionDirectory reads where the host runs this session now, per call
  // from the host session route rather than from storage (CD-0104 D1). The
  // window recorded that same value when the core authorized the dispatch, so a
  // difference means the session moved in between and the worker would no longer
  // start in the worktree the core authorized.
  //
  // It is a resolver rather than a value because an unauthorized call must be
  // refused on the window alone. Resolving first would spend a host round-trip
  // on a call that is already refused, and would report the host's answer in
  // place of the authorization failure that actually stopped it.
  async bind(tool: string, sessionID: string, args: MutableToolArgs, callID: string | undefined, resolveSessionDirectory: () => Promise<string>): Promise<void> {
    if (tool !== TASK_TOOL_ID) return
    const record = this.#open.get(sessionID)
    if (!record) {
      throw new DispatchWindowError(
        `no authorized dispatch window is open for session ${sessionID}; start a worker through dispatch_worker`,
      )
    }
    let sessionDirectory: string
    try {
      sessionDirectory = await resolveSessionDirectory()
    } catch {
      this.#open.delete(sessionID)
      throw new DispatchWindowError("worker dispatch could not resolve the host session directory")
    }
    const mismatch = dispatchDirectoryIdentityMismatch(record.workerDirectoryIdentity, sessionDirectory)
    if (mismatch) {
      this.#open.delete(sessionID)
      throw new DispatchWindowError(mismatch)
    }
    this.#open.delete(sessionID)
    record.callID = callID
    this.#inFlight.set(sessionID, record)
    const packet = record.packet
    args.subagent_type = LANE_AGENT_PREFIX + packet.lane_id
    args.prompt = JSON.stringify(packet)
    args.description = `${packet.lane_id} lane, attempt ${packet.attempt_id}`
    // A resumed worker session would carry a prior attempt's history into this
    // attempt. Lane restart is not reachable, so the resume field never survives.
    delete args.task_id
  }
}

// The native Task creates the worker session in the directory the host reports
// for the calling session, not in the host process directory. Resolve both sides
// before comparison so a symlink cannot make the host run a worker outside the
// claimed worktree.
export function canonicalDirectory(value: unknown): string | null {
  if (typeof value !== "string" || value.length === 0 || !path.isAbsolute(value)) return null
  try {
    const resolved = fs.realpathSync(value)
    return fs.statSync(resolved).isDirectory() ? resolved : null
  } catch {
    return null
  }
}

export function isResolvableDirectory(value: unknown): value is string {
  return canonicalDirectory(value) !== null
}

function canonicalDirectoryIdentity(value: string): string | null {
  if (!path.isAbsolute(value)) return null
  return "sha256:" + createHash("sha256").update(value, "utf8").digest("hex")
}

export function directoryIdentity(value: unknown): string | null {
  const canonical = canonicalDirectory(value)
  return canonical === null ? null : canonicalDirectoryIdentity(canonical)
}

export function dispatchDirectoryIdentityMismatch(expectedIdentity: string, sessionDirectory: unknown): string | null {
  const actualIdentity = directoryIdentity(sessionDirectory)
  return actualIdentity !== expectedIdentity
    ? `worker dispatch directory does not match the active claimed worktree (expected identity ${expectedIdentity}, session identity ${actualIdentity ?? "unresolved"})`
    : null
}

export function dispatchDirectoryMismatch(expected: unknown, sessionDirectory: unknown): string | null {
  const expectedIdentity = directoryIdentity(expected)
  const actualIdentity = directoryIdentity(sessionDirectory)
  return expectedIdentity !== null && expectedIdentity === actualIdentity ? null :
    `worker dispatch directory does not match the active claimed worktree (expected identity ${expectedIdentity ?? "unresolved"}, session identity ${actualIdentity ?? "unresolved"})`
}

const shared = new DispatchWindows()
