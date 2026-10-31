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
  work_define,
  domain,
  work_transition,
  work_relate,
  work_compact,
  publishRequestDefinition,
} from "./concord"
import type { PluginInput, PluginOptions } from "@opencode-ai/plugin"
import { createAgentSwitchNotice } from "./agent-switch-hook"
import { sessionDirectoryReader } from "./session-directory"
import { claimHostLease } from "./host-lease"
import { guardPrimaryRoles } from "./primary-role"

// The plugin factory is the only place the host hands over its own client.
// Binding the client here lets tools reach the host session routes. The host type is
// imported rather than restated, so a member the adapter needs cannot go
// missing from a local description of it. The import is type-only and erases,
// which keeps the adapter free of a runtime dependency on a host package.
export default async function ConcordAdapterPlugin(input?: Partial<PluginInput>, _options?: PluginOptions) {
  sessionDirectoryReader().bind(input)
  // CD-0111 D1/D2: claim the session's release lease before any tool can
  // run. The claim fails closed: a session that cannot claim a lease keeps
  // the release it runs visible to the installer, so the tools refuse rather
  // than run unprotected. The lease names the session's directory and
  // worktree so a breaking-migration refusal points at the exact terminal.
  await claimHostLease(process.pid, { directory: input?.directory, worktree: input?.worktree })
  const agentSwitch = createAgentSwitchNotice()
  const tools = guardPrimaryRoles({
    concord_product_view: product_view,
    concord_work_browse: work_browse,
    concord_work_trace: work_trace,
    concord_work_define: work_define,
    concord_domain: domain,
    concord_work_transition: work_transition,
    concord_work_relate: work_relate,
    concord_work_compact: work_compact,
  })
  return {
    tool: tools,
    "chat.message": async (
      input: { sessionID: string; messageID?: string; agent?: string },
    ) => {
      await agentSwitch.chatMessage(input)
    },
    "tool.definition": async (
      definitionInput: { toolID: string },
      output: { description: string; parameters: unknown; jsonSchema?: unknown },
    ) => {
      await publishRequestDefinition(definitionInput, output)
    },
    "experimental.chat.system.transform": async (input: { sessionID?: string }, output: { system: string[] }) => {
      await agentSwitch.transform(input, output)
    },
  }
}
