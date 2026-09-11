// concord-plugin — OpenCode plugin ENTRY module for the Concord adapter.
//
// OpenCode invokes every function-valued export of a plugin entry module as a
// plugin factory: its loader iterates the entry module's values and calls each
// with the host PluginInput. The adapter tool modules (concord.ts and its
// transitive imports) export helper functions alongside the tool definitions,
// so none of them can be the entry module — the host would call those helpers
// as bogus factories.
//
// This module therefore exports exactly one function-valued binding: default.
// The tool definitions live in concord.ts and arrive here as a normal
// transitive import; OpenCode inspects only this entry module's exports.
//
// The tool hook keys are the names agents invoke. The adapter's execute path
// hardcodes the `concord_*` names for contract routing, so the keys here must
// carry the `concord_` prefix.
import {
  product_view,
  work_browse,
  work_trace,
  knowledge,
  work_define,
  domain,
  work_initiative,
  work_transition,
  work_relate,
  work_compact,
  work_start,
  publishWorkStartDefinition,
} from "./concord"
import type { PluginInput } from "@opencode-ai/plugin"
import { createContinuityTransform } from "./continuity-hook"
import { createAgentSwitchNotice } from "./agent-switch-hook"
import { dispatchWindows, DispatchWindowError, TASK_TOOL_ID } from "./dispatch-window"
import { agentLanes, agentUtilities } from "./generated-agent-lanes"
import { completeDispatchedWorker } from "./lane_completion"
import { hostControlPlane, SessionScopeUnavailable } from "./move-session"
import { claimHostLease } from "./host-lease"
import { clearTurnMoveBoundary, questionRequiresNormalChat, TURN_MOVE_QUESTION_REFUSAL } from "./turn-move-boundary"
import { appendPendingWorkStateLines } from "./workflow-status"

// The plugin factory is the only place the host hands over its own client, and
// CD-0098 D2 makes the move-session route a requirement of work start. Binding
// the client here is what lets the tool path reach the route. The host type is
// imported rather than restated, so a member the adapter needs cannot go
// missing from a local description of it. The import is type-only and erases,
// which keeps the adapter free of a runtime dependency on a host package.
export default async function ConcordAdapterPlugin(input?: Partial<PluginInput>) {
  hostControlPlane().bind(input)
  // CD-0111 D1/D2: claim the session's release lease before any tool can
  // run. The claim fails closed: a session that cannot claim a lease keeps
  // the release it runs visible to the installer, so the tools refuse rather
  // than run unprotected.
  await claimHostLease(process.pid)
  const continuityTransform = createContinuityTransform()
  const agentSwitch = createAgentSwitchNotice()
  return {
    tool: {
      concord_product_view: product_view,
      concord_work_browse: work_browse,
      concord_work_trace: work_trace,
      concord_knowledge: knowledge,
      concord_work_define: work_define,
      concord_domain: domain,
      concord_work_initiative: work_initiative,
      concord_work_transition: work_transition,
      concord_work_relate: work_relate,
      concord_work_compact: work_compact,
      concord_work_start: work_start,
    },
    "chat.message": async (input: { sessionID: string }) => {
      clearTurnMoveBoundary(input.sessionID)
      await agentSwitch.chatMessage(input)
    },
    "tool.definition": publishWorkStartDefinition,
    // Managed sessions and Concord lanes require one authorized packet.
    // Ordinary unmanaged Tasks remain entirely subject to host permissions.
    "tool.execute.before": async (
      input: { tool: string; sessionID: string; callID: string },
      output: { args: Record<string, unknown> },
    ) => {
      if (input.tool === "question" && questionRequiresNormalChat(input.sessionID)) {
        throw new Error(TURN_MOVE_QUESTION_REFUSAL)
      }
      if (input.tool !== TASK_TOOL_ID) return
      const windows = dispatchWindows()
      const concordLane = agentLanes.some((lane) => output.args.subagent_type === `concord-${lane.id}`)
      if (windows.has(input.sessionID) || concordLane) {
        windows.bind(input.tool, input.sessionID, output.args)
        return
      }
      const concordUtility = agentUtilities.some((utility) => output.args.subagent_type === `concord-${utility.id}`)
      if (concordUtility) {
        if (await hostControlPlane().hasManagedParent(input.sessionID)) {
          throw new DispatchWindowError("a Concord utility cannot run from a session with a managed parent")
        }
        return
      }
      const scope = await hostControlPlane().taskScope(input.sessionID)
      if (scope === null) throw new SessionScopeUnavailable("cannot resolve managed Task scope: the calling host session does not exist")
      if (scope === "managed") {
        windows.bind(input.tool, input.sessionID, output.args)
        return
      }
      // A native Task may resume a session that belongs to another parent.
      // Caller participation alone cannot authorize a managed resume target.
      const target = output.args.task_id
      if (typeof target === "string" && target.length > 0 && await hostControlPlane().taskScope(target) === "managed") {
        throw new DispatchWindowError("an unmanaged Task cannot resume a managed Concord session; use a fresh authorized dispatch")
      }
    },
    // CD-0102 D5. The host ran the worker between the two hooks. This one
    // drains the in-flight attempt and admits the result: session export,
    // readback, report resolution, signing, and the worker-dispatch and
    // terminal evidence records. It never throws; a refusal is appended to
    // the tool output so the worker's result and the refusal both reach the
    // coordinator.
    "tool.execute.after": async (
      input: { tool: string; sessionID: string; callID: string; args: unknown },
      output: { title: string; output: string; metadata: unknown },
    ) => {
      await completeDispatchedWorker(input, output)
    },
    "experimental.chat.system.transform": async (input: unknown, output: { system: string[] }) => {
      await continuityTransform(input, output)
      await agentSwitch.transform(input, output)
    },
    "experimental.text.complete": async (
      input: { sessionID: string; messageID: string; partID: string },
      output: { text: string },
    ) => {
      output.text = appendPendingWorkStateLines(input.sessionID, output.text)
    },
  }
}
