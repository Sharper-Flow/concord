// primary-role — refuses Concord tools a registered role may not call.
//
// Frontmatter denials are host permissions, and OpenCode merges session
// permissions over agent permissions, so an "always allow" granted under
// another agent survives a switch to a restricted role. The host supplies
// the responding agent with every tool call, so the guard reads that
// identity per call and never trusts the advisory switch notice. The denied
// set is generated from both role registries: the primary profiles in
// contracts/agent-primary-profiles.v1.json and the utilities in
// contracts/agent-utilities.v1.json, so host visibility and execution denial
// derive from one registry population.
import { roleDeniedConcordTools } from "./generated-primary-profiles"

export class PrimaryRoleRefusal extends Error {}

const ALL_DENIED = new Set(Object.values(roleDeniedConcordTools).flatMap((role) => [...role.tools]))

// A step with no agent identity cannot be proven to be some other agent, so a
// tool any registered role denies refuses closed.
function refusalReason(toolID: string, agent: unknown): string | null {
  if (!ALL_DENIED.has(toolID)) return null
  if (typeof agent !== "string" || agent.length === 0) {
    return `${toolID} is refused: the host supplied no agent identity for this step, so the caller cannot be proven to hold Concord execution authority; the call records nothing`
  }
  const role = roleDeniedConcordTools[agent]
  if (role === undefined || !role.tools.includes(toolID)) return null
  return `${toolID} is refused for ${agent}: ${role.boundary}; the call records nothing`
}

// The wrapper runs the original tool unchanged and forwards its arguments and
// result, so the guarded type keeps the wrapped execute contract and adds the
// one field the guard reads: the host execution context agent. The host may
// omit agent, and the runtime already refuses a step with no identity, so the
// guarded context accepts agent as optional unknown.
export type GuardedPrimaryTool<T extends { execute: (args: any, context: any) => Promise<any> }> = Omit<T, "execute"> & {
  execute: (
    args: Parameters<T["execute"]>[0],
    context: Parameters<T["execute"]>[1] & { agent?: unknown },
  ) => Promise<Awaited<ReturnType<T["execute"]>>>
}

export type GuardedPrimaryTools<T extends Record<string, { execute: (args: any, context: any) => Promise<any> }>> = {
  [K in keyof T]: GuardedPrimaryTool<T[K]>
}

export function guardPrimaryRole<T extends { execute: (args: any, context: any) => Promise<any> }>(toolID: string, definition: T): GuardedPrimaryTool<T> {
  return {
    ...definition,
    execute: async (args: any, context: any) => {
      const reason = refusalReason(toolID, context?.agent)
      if (reason !== null) throw new PrimaryRoleRefusal(reason)
      return definition.execute(args, context)
    },
  }
}

export function guardPrimaryRoles<T extends Record<string, { execute: (args: any, context: any) => Promise<any> }>>(tools: T): GuardedPrimaryTools<T> {
  const guarded: Record<string, unknown> = {}
  for (const [toolID, definition] of Object.entries(tools)) guarded[toolID] = guardPrimaryRole(toolID, definition)
  return guarded as GuardedPrimaryTools<T>
}
