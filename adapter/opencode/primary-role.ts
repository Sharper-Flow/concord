// primary-role — refuses Concord tools a named primary profile may not call.
//
// The coordinator profile's frontmatter denies its mutating Concord tools, but
// OpenCode merges session permissions over agent permissions, so an "always
// allow" granted under another agent survives a switch to the coordinator.
// The host supplies the responding agent with every tool call, so the guard
// reads that identity per call and never trusts the advisory switch notice.
// The denied set is generated from contracts/agent-primary-profiles.v1.json.
import { primaryProfileDeniedConcordTools } from "./generated-primary-profiles"

export class PrimaryRoleRefusal extends Error {}

const ALL_DENIED = new Set(Object.values(primaryProfileDeniedConcordTools).flat())

// A step with no agent identity cannot be proven to be some other agent, so a
// tool any profile denies refuses closed.
function refusalReason(toolID: string, agent: unknown): string | null {
  if (!ALL_DENIED.has(toolID)) return null
  if (typeof agent !== "string" || agent.length === 0) {
    return `${toolID} is refused: the host supplied no agent identity for this step, so the caller cannot be proven to hold Concord execution authority; the call records nothing`
  }
  const denied = primaryProfileDeniedConcordTools[agent]
  if (denied === undefined || !denied.includes(toolID)) return null
  return `${toolID} is refused for ${agent}: that role supervises live sessions and holds no Concord execution authority; a repository-local operator session makes this call; the call records nothing`
}

export function guardPrimaryRole<T extends { execute: (args: any, context: any) => Promise<any> }>(toolID: string, definition: T): T {
  return {
    ...definition,
    execute: async (args: any, context: any) => {
      const reason = refusalReason(toolID, context?.agent)
      if (reason !== null) throw new PrimaryRoleRefusal(reason)
      return definition.execute(args, context)
    },
  }
}

export function guardPrimaryRoles<T extends Record<string, { execute: (args: any, context: any) => Promise<any> }>>(tools: T): T {
  const guarded: Record<string, unknown> = {}
  for (const [toolID, definition] of Object.entries(tools)) guarded[toolID] = guardPrimaryRole(toolID, definition)
  return guarded as T
}
