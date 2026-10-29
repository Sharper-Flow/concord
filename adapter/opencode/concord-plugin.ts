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
  work_transition,
  work_relate,
  work_compact,
  work_start,
  publishWorkStartDefinition,
  publishRequestDefinition,
  takeWorkNotices,
} from "./concord"
import type { PluginInput, PluginOptions } from "@opencode-ai/plugin"
import { createContinuityTransform } from "./continuity-hook"
import { createAgentSwitchNotice } from "./agent-switch-hook"
import { bindCiWatchClient, ciWatchTool, drainQueuedCiReportsForMessage, isCiWatchNoticeMessage, publishCiWatchDefinition, redeliverQueuedCiReports, registerCiWatchPeer } from "./ci-watch"
import { hostControlPlane } from "./move-session"
import { claimHostLease } from "./host-lease"
import { guardPrimaryRoles } from "./primary-role"
import { clearTurnMoveBoundary, questionRequiresNormalChat, TURN_MOVE_QUESTION_REFUSAL } from "./turn-move-boundary"
import { continueWorkStartOnEvent, observeWorkStartMessage } from "./work-start-continuation"

// The plugin factory is the only place the host hands over its own client, and
// CD-0098 D2 makes the move-session route a requirement of work start. Binding
// the client here is what lets the tool path reach the route. The host type is
// imported rather than restated, so a member the adapter needs cannot go
// missing from a local description of it. The import is type-only and erases,
// which keeps the adapter free of a runtime dependency on a host package.
// The session title is the single source of the goal text, and compaction
// never reads it: the summary runs over conversation history alone. The host
// joins the strings this hook pushes onto the context into the compaction
// prompt, so a title that names the goal anchors the summary. A session
// started by work_start carries no exported Product and work selection, so
// the continuity block alone cannot carry the goal there.
const GOAL_TITLE_PREFIX = "Goal: "

async function pushSessionGoalTitle(input: unknown, output: { context: string[] }): Promise<void> {
  try {
    const sessionID = (input as { sessionID?: unknown } | null | undefined)?.sessionID
    if (typeof sessionID !== "string" || sessionID.length === 0 || !Array.isArray(output.context)) return
    const title = await hostControlPlane().sessionTitle(sessionID)
    if (title === null || !title.startsWith(GOAL_TITLE_PREFIX)) return
    output.context.push(title)
  } catch {
    return
  }
}

export default async function ConcordAdapterPlugin(input?: Partial<PluginInput>, _options?: PluginOptions) {
  hostControlPlane().bind(input)
  // The CI watcher delivers through the same client, so the plugin factory is
  // the one place the watcher transport is bound.
  bindCiWatchClient(input)
  // Bind before publication so a failed peer registration can log.
  const ciPeer = registerCiWatchPeer(input)
  // CD-0111 D1/D2: claim the session's release lease before any tool can
  // run. The claim fails closed: a session that cannot claim a lease keeps
  // the release it runs visible to the installer, so the tools refuse rather
  // than run unprotected. The lease names the session's directory and
  // worktree so a breaking-migration refusal points at the exact terminal.
  await claimHostLease(process.pid, { directory: input?.directory, worktree: input?.worktree })
  const continuityTransform = createContinuityTransform()
  const agentSwitch = createAgentSwitchNotice()
  const tools = guardPrimaryRoles({
    concord_product_view: product_view,
    concord_work_browse: work_browse,
    concord_work_trace: work_trace,
    concord_knowledge: knowledge,
    concord_work_define: work_define,
    concord_domain: domain,
    concord_work_transition: work_transition,
    concord_work_relate: work_relate,
    concord_work_compact: work_compact,
    concord_work_start: work_start,
    concord_ci_watch: ciWatchTool(ciPeer.id),
  })
  return {
    tool: tools,
    "chat.message": async (
      input: { sessionID: string; messageID?: string; agent?: string },
      output?: { message?: { id?: unknown }; parts?: unknown[] },
    ) => {
      // The ci-watch start notice is a noReply user message, and the host
      // still reports it through this hook. It must not consume the wake
      // protocol: a drained report would land in a message that never wakes
      // the model, and the boundary clear and agent-switch read below would
      // treat the notice as an operator turn. Skip those when every part
      // carries the notice marker.
      if (isCiWatchNoticeMessage(output?.parts)) return
      observeWorkStartMessage(input.sessionID, output?.parts)
      clearTurnMoveBoundary(input.sessionID)
      await agentSwitch.chatMessage(input)
      // The CI watcher queues a terminal report it could not confirm and
      // injects it here, so a lost wake reaches the model on the next turn.
      // The host validates drained parts against the stored part schema, so
      // the drain needs the message the parts will belong to: the input
      // messageID when the host supplies one, else the host-generated id on
      // the output message record.
      if (output !== undefined && Array.isArray(output.parts)) {
        drainQueuedCiReportsForMessage(input.sessionID, input.messageID, output)
      }
    },
    "tool.definition": async (
      definitionInput: { toolID: string },
      output: { description: string; parameters: unknown; jsonSchema?: unknown },
    ) => {
      await publishRequestDefinition(definitionInput, output)
      await publishWorkStartDefinition(definitionInput, output)
      await publishCiWatchDefinition(definitionInput, output)
    },
    event: async ({ event }: { event: unknown }) => {
      redeliverQueuedCiReports(event)
      continueWorkStartOnEvent(event, message => {
        if (input?.client?.app?.log) void input.client.app.log({ body: { service: "concord", level: "warn", message } }).catch(() => { console.warn(message) })
        else console.warn(message)
      })
    },
    "tool.execute.before": async (
      input: { tool: string; sessionID: string; callID: string },
      _output: { args: Record<string, unknown> },
    ) => {
      if (input.tool === "question" && questionRequiresNormalChat(input.sessionID)) {
        throw new Error(TURN_MOVE_QUESTION_REFUSAL)
      }
    },
    // The text-part channel. The work-state reporter queues operator-facing
    // blocks per session — the closure banner, the gate brief, the
    // worktree-removal notice — and this hook drains them into the
    // assistant's own message as the text part completes, so the transcript
    // holds them and the agent spends no tokens forming them. The hook
    // swallows every throw: a display can never damage an assistant message.
    "experimental.text.complete": async (
      input: { sessionID: string; messageID: string; partID: string },
      output: { text: string },
    ) => {
      try {
        const blocks = takeWorkNotices(input.sessionID)
        if (blocks.length > 0) output.text = `${output.text}${output.text ? "\n\n" : ""}${blocks.join("\n\n")}`
      } catch {
        // A display can never damage an assistant message.
      }
    },
    // The transform input is the host's typed `{ sessionID?, model }`; only
    // the session identity matters here, and its absence (the
    // Agent.generate path) renders no continuity block.
    "experimental.chat.system.transform": async (input: { sessionID?: string }, output: { system: string[] }) => {
      await continuityTransform(input, output)
      await agentSwitch.transform(input, output)
    },
    "experimental.session.compacting": pushSessionGoalTitle,
    dispose: async () => ciPeer.dispose(),
  }
}
