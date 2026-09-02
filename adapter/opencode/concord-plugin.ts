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
} from "./concord"
import { createContinuityTransform } from "./continuity-hook"
import { createAgentSwitchNotice } from "./agent-switch-hook"
import { dispatchWindows } from "./dispatch-window"

export default async function ConcordAdapterPlugin() {
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
    "chat.message": agentSwitch.chatMessage,
    // CD-0102 D2. The model composes the Task call, so its arguments carry no
    // provenance. This hook overwrites them with the packet an authorized
    // dispatch recorded, and throws when no dispatch authorized the call —
    // which fails that one tool call rather than the session.
    "tool.execute.before": async (
      input: { tool: string; sessionID: string; callID: string },
      output: { args: Record<string, unknown> },
    ) => {
      dispatchWindows().bind(input.tool, input.sessionID, output.args)
    },
    "experimental.chat.system.transform": async (input: unknown, output: { system: string[] }) => {
      await continuityTransform(input, output)
      await agentSwitch.transform(input, output)
    },
  }
}
