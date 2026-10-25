// CD-0102 D1/D2: the single-use authorization window that binds one typed
// dispatch to one native Task call.
//
// The coordinator issues the native Task with its exact attempt reference.
// The hook validates that selector before binding the recorded packet once.
import { createHash } from "node:crypto"
import fs from "node:fs"
import path from "node:path"
import type { AgentLanePacket, HostProvenance } from "./dispatch"
import type { ReleaseStaleness } from "./host-lease"
import { dispatchRequiresNextTurn, TURN_MOVE_DISPATCH_REFUSAL } from "./turn-move-boundary"

// serializeLanePacket is the one byte form of a lane packet: the Task prompt
// the dispatch window writes and the opening text the readback compares. It
// escapes every '@' as \u0040, which JSON decodes back to '@', so quoted
// correction context such as a prior session title cannot form a host @agent
// mention in the worker's opening message.
export function serializeLanePacket(packet: AgentLanePacket): string {
  return JSON.stringify(packet).replace(/@/g, "\\u0040")
}

// The host renders the worker card only for the tool with this id, so the lane
// runs under it or it runs without operator progress, navigation, and cancel.
export const TASK_TOOL_ID = "task"

// The lane agent names installed under .opencode/agents/ carry this prefix.
const LANE_AGENT_PREFIX = "concord-"

export function laneTaskArguments(packet: AgentLanePacket): { subagent_type: string; prompt: string; description: string } {
  return {
    subagent_type: LANE_AGENT_PREFIX + packet.lane_id,
    prompt: serializeLanePacket(packet),
    description: `${packet.lane_id} lane, attempt ${packet.attempt_id}`,
  }
}

export class DispatchWindowError extends Error {}

// CD-0191: the stale-release dispatch gate. OpenCode loads lane agent
// definitions once at process start and never hot-reloads, while the
// installer rewrites them in place on every install, so a session whose
// pinned release differs from the installed release would dispatch workers
// against lane text the install replaced. The gate owns that refusal text
// and is checked at the dispatch_worker action boundary, before any core
// call: refusing after core authorization (the window bind, for example)
// would strand a recorded attempt behind exactly the terminal-attempt harm
// this gate prevents. The refusal rides the dispatch surface's own
// adapter-gate vocabulary — unauthorized_dispatch with a boundary
// discriminator, the turn-move gate's shape — because TS7 pins
// adapter-origin tool-envelope error kinds to the transport set, and
// details.refusal_kind carries the stale_context classification.
export function staleReleaseDispatchRefusal(staleness: ReleaseStaleness): string {
  return (
    `this session pinned release ${staleness.pinnedRelease} but the host installed ${staleness.installedRelease}; ` +
    `the lane definitions this process holds were replaced on disk, so dispatch_worker refuses and no worker attempt opens; ` +
    `restart this session to load the installed release, then dispatch again`
  )
}

// An authorized attempt in flight: the packet and digest captured before the
// host starts the worker. Completion needs these values after the host runs
// the worker, so none can be held on a stack.
export interface DispatchRecord {
  packet: AgentLanePacket
  packetDigest: string
  workerDirectory: string
  workerDirectoryIdentity: string
  callID?: string
  // The provenance manifest captured at authorization. Completion records it
  // instead of recomputing after the worker ran; a window opened without one
  // carries none and completion falls back to computing it.
  provenance?: HostProvenance
}

interface MutableToolArgs {
  subagent_type?: unknown
  prompt?: unknown
  description?: unknown
  task_id?: unknown
  [key: string]: unknown
}

interface AuthorizationRecord extends DispatchRecord {
  readonly generation: number
  readonly priorCorrelatedGeneration: number
}

interface AuthorizationGenerations {
  latest: number
  correlatedThrough: number
  pending: Map<number, { callID?: string; attemptID: string; promptDigest: string; settled: boolean; terminal: boolean }>
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
  readonly #open = new Map<string, AuthorizationRecord>()
  // An attempt the host is running now. The Task call consumed its window, and
  // completion still needs the packet and the digest the core recorded.
  readonly #inFlight = new Map<string, AuthorizationRecord>()
  readonly #settling = new Set<DispatchRecord>()
  // A refused after-hook retains its record. Only an explicit authenticated
  // recovery or a confirmed no-report abandonment releases it; repeated host
  // notifications cannot silently re-attempt settlement.
  readonly #refused = new Set<DispatchRecord>()
  // A core completion can precede the host terminal part. Only both facts
  // retire a generation as a possible source of a later session-only error.
  readonly #generations = new Map<string, AuthorizationGenerations>()

  open(sessionID: string, packet: AgentLanePacket, packetDigest = "", workerDirectory?: string, pinnedWorkerDirectory?: string, provenance?: HostProvenance): void {
    const running = this.#inFlight.get(sessionID)
    if (running) {
      // The in-flight refusal names its recovery routes. A record that stays
      // in-flight has no settle left to wait for, and without the named
      // routes an agent that hits this refusal restarts the host instead of
      // recovering the retained completed Task through worker_reconcile, or
      // closing an attempt with no report through worker_abandon, whose
      // accepted receipt releases the retained record.
      throw new DispatchWindowError(
        `session ${sessionID} already holds an in-flight dispatch attempt (${running.packet.lane_id} lane, attempt ${running.packet.attempt_id}, work ${running.packet.work_id}) that never settled; recover its retained completed Task with concord_work_transition operation worker_reconcile, or use worker_abandon naming that work_id, attempt_id, and lane_id only when no worker report exists, then dispatch again`,
      )
    }
    if (this.#open.has(sessionID)) {
      throw new DispatchWindowError(`session ${sessionID} already holds an open dispatch window; the next Task call consumes it`)
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
    const workerDirectoryIdentity = canonicalDirectoryIdentity(canonicalWorkerDirectory)!
    let generations = this.#generations.get(sessionID)
    if (!generations) {
      generations = { latest: 0, correlatedThrough: 0, pending: new Map() }
      this.#generations.set(sessionID, generations)
    }
    const generation = ++generations.latest
    generations.pending.set(generation, { attemptID: packet.attempt_id, promptDigest: createHash("sha256").update(serializeLanePacket(packet)).digest("hex"), settled: false, terminal: false })
    this.#open.set(sessionID, { generation, priorCorrelatedGeneration: generations.correlatedThrough, packet, packetDigest, workerDirectory: canonicalWorkerDirectory, workerDirectoryIdentity, provenance })
  }

  // close discards a window whose dispatch failed before the worker started, so
  // a refused attempt does not leave an authorization the next call can consume.
  close(sessionID: string): void {
    const record = this.#open.get(sessionID)
    if (record) this.#settled(sessionID, record)
    this.#open.delete(sessionID)
  }

  has(sessionID: string): boolean {
    return this.#open.has(sessionID)
  }

  // takeInFlight hands the running attempt to the completion path once. A second
  // completion for one dispatch finds nothing, so a result cannot be admitted
  // twice against a single authorization.
  takeInFlight(sessionID: string, callID?: string): DispatchRecord | null {
    const record = this.#inFlight.get(sessionID)
    if (!record || this.#settling.has(record) || this.#refused.has(record)) return null
    if (callID !== undefined && record.callID !== undefined && record.callID !== callID) return null
    this.finishSettlement(sessionID, callID, record)
    return record
  }

  inFlight(sessionID: string, callID: string): DispatchRecord | null {
    const record = this.#inFlight.get(sessionID)
    return record && (record.callID === undefined || record.callID === callID) ? record : null
  }

  inFlightAttempt(sessionID: string): DispatchRecord | null {
    return this.#inFlight.get(sessionID) ?? null
  }

  activeAttempt(sessionID: string): DispatchRecord | null {
    const record = this.inFlightAttempt(sessionID)
    return record && !this.#settling.has(record) && !this.#refused.has(record) ? record : null
  }

  hasActiveAttempts(): boolean {
    return [...this.#inFlight.keys()].some((sessionID) => this.activeAttempt(sessionID) !== null)
  }

  claimSettlement(sessionID: string, callID?: string, expected?: DispatchRecord): DispatchRecord | null {
    const record = callID === undefined ? this.#inFlight.get(sessionID) ?? null : this.inFlight(sessionID, callID)
    if (!record || expected !== undefined && record !== expected || this.#settling.has(record) || this.#refused.has(record)) return null
    this.#settling.add(record)
    return record
  }

  finishSettlement(sessionID: string, callID: string | undefined, expected: DispatchRecord): void {
    const record = this.#inFlight.get(sessionID)
    if (!record || record !== expected || callID !== undefined && record.callID !== undefined && record.callID !== callID) return
    this.#inFlight.delete(sessionID)
    this.#settling.delete(record)
    this.#refused.delete(record)
    this.#settled(sessionID, record)
  }

  // unclaimSettlement releases a settlement claim without dropping the
  // retained record, so a refused settlement keeps the record and leaves the
  // worker_abandon release route live for the coordinator. A later settle
  // attempt may run again; the spawn route relies on that for its idempotent
  // abandonment replay.
  unclaimSettlement(sessionID: string, expected: DispatchRecord): void {
    if (this.#inFlight.get(sessionID) === expected) this.#settling.delete(expected)
  }

  // refuseSettlement ends a settle attempt whose terminal write was refused.
  // The retained record stays and a repeat host notification cannot settle it.
  // The explicit recovery receipt releases only its matching attempt.
  refuseSettlement(sessionID: string, expected: DispatchRecord): void {
    if (this.#inFlight.get(sessionID) !== expected) return
    this.#settling.delete(expected)
    this.#refused.add(expected)
  }

  // A session-only error carries no call identity. Prior session-only closes
  // cannot certify this fence: prompt_async may publish their error again.
  canSettleSessionError(sessionID: string, expected: DispatchRecord): boolean {
    const record = this.#inFlight.get(sessionID)
    const generations = this.#generations.get(sessionID)
    return record === expected && this.activeAttempt(sessionID) === record && generations?.latest === record.generation
      && record.priorCorrelatedGeneration === record.generation - 1
      && [...generations.pending.values()].filter(entry => !entry.settled).length === 1
  }

  observeTerminalPart(sessionID: string, callID: string, partID: string | undefined, prompt: unknown): boolean {
    const generations = this.#generations.get(sessionID)
    if (!generations || typeof prompt !== "string") return false
    const promptDigest = createHash("sha256").update(prompt).digest("hex")
    // The host can retain the original selector or the rewritten packet.
    // Either must identify this attempt even when a call selector is reused.
    const matches = [...generations.pending.values()].filter(entry => entry.callID !== undefined
      && (entry.callID === callID || entry.callID === partID)
      && (entry.attemptID === prompt || entry.promptDigest === promptDigest))
    if (matches.length !== 1) return false
    const current = this.#inFlight.get(sessionID)
    const matchesCurrent = current !== undefined && generations.pending.get(current.generation) === matches[0]
    matches[0].terminal = true
    this.#advanceCorrelated(generations)
    return matchesCurrent
  }

  #settled(sessionID: string, record: AuthorizationRecord): void {
    const generations = this.#generations.get(sessionID)
    const entry = generations?.pending.get(record.generation)
    if (!generations || !entry) return
    entry.settled = true
    this.#advanceCorrelated(generations)
  }

  #advanceCorrelated(generations: AuthorizationGenerations): void {
    while (true) {
      const next = generations.correlatedThrough + 1
      const entry = generations.pending.get(next)
      if (!entry?.settled || !entry.terminal) return
      generations.pending.delete(next)
      generations.correlatedThrough = next
    }
  }

  // An accepted abandonment releases either an unused authorization window or
  // a retained in-flight record. Neither may authorize another Task afterward.
  // The drop requires the caller to name the exact attempt identity the
  // record holds, and requires no settlement to be in progress, so a foreign
  // identity or a settling attempt leaves the retention guard exactly as it
  // was.
  releaseRetained(sessionID: string, attemptID: string, laneID: string): boolean {
    const record = this.#inFlight.get(sessionID) ?? this.#open.get(sessionID)
    if (!record || this.#settling.has(record)) return false
    if (record.packet.attempt_id !== attemptID || record.packet.lane_id !== laneID) return false
    this.#open.delete(sessionID)
    this.#inFlight.delete(sessionID)
    this.#refused.delete(record)
    this.#settled(sessionID, record)
    return true
  }

  // retirementBlocker answers this session's dispatch-window quiescence for
  // the Project-session retirement read (CD-0182 amendment): the retiring
  // session's own open, in-flight, settling, or refused window is execution
  // that must stop before the session is ready to retire, and the core cannot
  // see these windows, so the adapter supplies the fact. A null answer means
  // this session holds no retained dispatch record. Another session's
  // positively identified window is not this session's worker, so the reader
  // is per-session.
  retirementBlocker(sessionID: string): string | null {
    const open = this.#open.get(sessionID)
    if (open) {
      return `the session holds an open dispatch window (${open.packet.lane_id} lane, attempt ${open.packet.attempt_id}); complete the authorized dispatch or close the window before retirement`
    }
    const record = this.#inFlight.get(sessionID)
    if (record && this.#settling.has(record)) {
      return "the session's worker attempt is still settling; the terminal record must land before retirement"
    }
    if (record && this.#refused.has(record)) {
      return "the session's last settle was refused; the retained attempt needs the worker_abandon release before retirement"
    }
    const inFlight = this.#inFlight.get(sessionID)
    if (inFlight) {
      return `the session holds an in-flight worker attempt (${inFlight.packet.lane_id} lane, attempt ${inFlight.packet.attempt_id}); close it with worker_abandon before retirement`
    }
    return null
  }

  // bind is the `tool.execute.before` body. It mutates the caller's arguments in
  // place, which is the only channel the host hook contract offers.
  //
  // executionDirectory is where the host instance runs the Task worker — the
  // plugin factory directory. It is required with no fallback: the session
  // record and the process directory do not prove execution placement.
  //
  // resolveSessionDirectory reads where the host reports this session now, per
  // call from the host session route rather than from storage (CD-0104 D1). It
  // is a resolver rather than a value because an unauthorized call must be
  // refused on the window alone, without spending a host round-trip on it.
  //
  // Both directory answers are checked after the await: the await admits
  // retargets and revocations, so only the still-open record plus the
  // post-await answers authorize the call, and the execution-directory check
  // is the final gate before the caller's arguments are touched.
  async bind(tool: string, sessionID: string, args: MutableToolArgs, callID: string | undefined, resolveSessionDirectory: () => Promise<string>, executionDirectory: unknown): Promise<void> {
    if (tool !== TASK_TOOL_ID) return
    const record = this.#open.get(sessionID)
    if (!record) {
      throw new DispatchWindowError(
        `no authorized dispatch window is open for session ${sessionID}; start a worker through dispatch_worker`,
      )
    }
    if (Object.keys(args).some(key => !["subagent_type", "prompt", "description"].includes(key)) ||
        args.subagent_type !== LANE_AGENT_PREFIX + record.packet.lane_id ||
        args.prompt !== record.packet.attempt_id || typeof args.description !== "string") {
      throw new DispatchWindowError("native lane Task requires the exact authorized lane and attempt reference with only subagent_type, prompt, and description; use the dispatch response, without resume, background, or command fields")
    }
    if (dispatchRequiresNextTurn(sessionID)) {
      this.#open.delete(sessionID)
      throw new DispatchWindowError(TURN_MOVE_DISPATCH_REFUSAL)
    }
    let sessionDirectory: string
    try {
      sessionDirectory = await resolveSessionDirectory()
    } catch (error) {
      if (this.#open.get(sessionID) === record) this.#open.delete(sessionID)
      if (error instanceof DispatchWindowError) throw error
      throw new DispatchWindowError("worker dispatch could not resolve the host session directory")
    }
    // The await permits revocation, replacement, or another bind's consumption.
    // Only the exact still-open record can authorize this Task call.
    if (this.#open.get(sessionID) !== record) {
      throw new DispatchWindowError(
        `no authorized dispatch window is open for session ${sessionID}; start a worker through dispatch_worker`,
      )
    }
    const mismatch = dispatchDirectoryIdentityMismatch(record.workerDirectoryIdentity, sessionDirectory)
    if (mismatch) {
      this.#open.delete(sessionID)
      throw new DispatchWindowError(mismatch)
    }
    const executionInstanceDirectory = canonicalDirectory(executionDirectory)
    if (executionInstanceDirectory === null) {
      this.#open.delete(sessionID)
      throw new DispatchWindowError("worker dispatch requires the execution directory of the host instance that would run the Task worker; it was not supplied or does not resolve to a directory")
    }
    const executionIdentity = canonicalDirectoryIdentity(executionInstanceDirectory)
    if (executionIdentity !== record.workerDirectoryIdentity) {
      this.#open.delete(sessionID)
      throw new DispatchWindowError(
        `worker dispatch directory does not match the active claimed worktree (expected identity ${record.workerDirectoryIdentity}, execution identity ${executionIdentity ?? "unresolved"}); the host instance would run the Task worker outside the claimed worktree the dispatch authorized; replay work_start or worktree_claim so the execution instance runs in the claimed worktree, then dispatch again after the next operator turn`,
      )
    }
    this.#open.delete(sessionID)
    record.callID = callID
    this.#generations.get(sessionID)!.pending.get(record.generation)!.callID = callID
    this.#inFlight.set(sessionID, record)
    Object.assign(args, laneTaskArguments(record.packet))
  }
}

// A native Task child runs in the directory the turn resolved at its start.
// Across turns, that directory equals the host-reported session directory. A
// moved turn still resolves its pre-move directory, so dispatch stays closed
// until the next operator turn. Resolve the host directory before comparison
// after that boundary so a symlink cannot move the worker outside the claim.
export function canonicalDirectory(value: unknown): string | null {
  if (typeof value !== "string" || value.length === 0 || !path.isAbsolute(value)) return null
  try {
    const resolved = fs.realpathSync(value)
    return fs.statSync(resolved).isDirectory() ? resolved : null
  } catch {
    return null
  }
}

function canonicalDirectoryIdentity(value: string): string | null {
  if (!path.isAbsolute(value)) return null
  return "sha256:" + createHash("sha256").update(value, "utf8").digest("hex")
}

export function directoryIdentity(value: unknown): string | null {
  const canonical = canonicalDirectory(value)
  return canonical === null ? null : canonicalDirectoryIdentity(canonical)
}

export function dispatchDirectoryIdentityMismatch(expectedIdentity: string | null, sessionDirectory: unknown): string | null {
  const actualIdentity = directoryIdentity(sessionDirectory)
  return expectedIdentity !== null && expectedIdentity === actualIdentity ? null :
    `worker dispatch directory does not match the active claimed worktree (expected identity ${expectedIdentity ?? "unresolved"}, session identity ${actualIdentity ?? "unresolved"})`
}

export function dispatchDirectoryMismatch(expected: unknown, sessionDirectory: unknown): string | null {
  return dispatchDirectoryIdentityMismatch(directoryIdentity(expected), sessionDirectory)
}

const shared = new DispatchWindows()
