// CD-0102 D1/D2: the single-use authorization window that binds one typed
// dispatch to one native Task call.
//
// The model issues the Task call, not Concord, so the arguments it composes
// carry no provenance the core can trust. A window is opened only by an
// authorized `dispatch_worker` action. The next Task call from the same session
// has its agent selection and prompt overwritten by the recorded packet, and the
// window closes. A Task call with no open window fails.
import type { AgentLanePacket } from "./dispatch"

// The host renders the worker card only for the tool with this id, so the lane
// runs under it or it runs without operator progress, navigation, and cancel.
export const TASK_TOOL_ID = "task"

// The lane agent names installed under .opencode/agents/ carry this prefix.
const LANE_AGENT_PREFIX = "concord-"

export class DispatchWindowError extends Error {}

// An authorized attempt in flight: the packet the core authorized and the
// packet digest it recorded. Completion needs both, and the host runs the
// worker between dispatch and completion, so neither can be held on a stack.
export interface DispatchRecord {
  packet: AgentLanePacket
  packetDigest: string
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

  open(sessionID: string, packet: AgentLanePacket, packetDigest = ""): void {
    if (this.#open.has(sessionID)) {
      throw new DispatchWindowError(`session ${sessionID} already holds an open dispatch window`)
    }
    this.#open.set(sessionID, { packet, packetDigest })
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
  takeInFlight(sessionID: string): DispatchRecord | null {
    const record = this.#inFlight.get(sessionID)
    if (!record) return null
    this.#inFlight.delete(sessionID)
    return record
  }

  // bind is the `tool.execute.before` body. It mutates the caller's arguments in
  // place, which is the only channel the host hook contract offers.
  bind(tool: string, sessionID: string, args: MutableToolArgs): void {
    if (tool !== TASK_TOOL_ID) return
    const record = this.#open.get(sessionID)
    if (!record) {
      throw new DispatchWindowError(
        `no authorized dispatch window is open for session ${sessionID}; start a worker through dispatch_worker`,
      )
    }
    this.#open.delete(sessionID)
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

const shared = new DispatchWindows()
