const SENTINEL = "\n\n--- ACTIVE AGENT CHANGED ---\n"
const MAX_SESSIONS = 512

export function createAgentSwitchNotice() {
  const lastAgent = new Map<string, string>()
  return {
    context(input: { sessionID: string; agent: string; system: Array<{ type: "text"; text: string }> }): void {
      const { sessionID, agent, system } = input
      if (!sessionID || !agent || !Array.isArray(system)) return
      const previous = lastAgent.get(sessionID)
      lastAgent.delete(sessionID)
      lastAgent.set(sessionID, agent)
      while (lastAgent.size > MAX_SESSIONS) {
        const oldest = lastAgent.keys().next()
        if (oldest.done) break
        lastAgent.delete(oldest.value)
      }
      if (previous === undefined || previous === agent || system.some((part) => part.text.includes(SENTINEL))) return
      system.push({ type: "text", text: SENTINEL +
        `The active agent changed from \`${previous}\` to \`${agent}\`.\n` +
        `Your tool set and permission rules now come from \`${agent}\`. ` +
        `Re-check any capability you found unavailable earlier in this session; do not carry the previous agent's limits forward.\n` +
        `Permissions already granted as "always allow" remain granted for the rest of the session.` })
    },
  }
}
