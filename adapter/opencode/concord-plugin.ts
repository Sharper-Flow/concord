import type { Plugin } from "@opencode/plugin"
import { product_view, work_browse, work_trace, work_define, domain, work_transition, work_relate } from "./concord"
import { createAgentSwitchNotice } from "./agent-switch-hook"
import { claimHostLease } from "./host-lease"
import { guardPrimaryRole } from "./primary-role"

const definitions = [product_view, work_browse, work_trace, work_define, domain, work_transition, work_relate]

export default {
  id: "concord",
  async setup(ctx) {
    const agentSwitch = createAgentSwitchNotice()
    await claimHostLease(process.pid, { directory: ctx.location.directory, worktree: ctx.location.directory })
    await ctx.tool.transform((editor) => {
      for (const definition of definitions) {
        const guarded = guardPrimaryRole(definition.name, definition)
        editor.add({
          ...definition,
          input: structuredClone(definition.input),
          execute: (args, context) => guarded.execute(args, {
            sessionID: context.sessionID,
            agent: context.agent,
            messageID: context.messageID,
            signal: context.signal,
            sessionDirectory: async () => (await ctx.session.get({ sessionID: context.sessionID })).location.directory,
          }),
        })
      }
    })
    await ctx.session.hook("context", agentSwitch.context)
  },
} satisfies Plugin.Plugin
