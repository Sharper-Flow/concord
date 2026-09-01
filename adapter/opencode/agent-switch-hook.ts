// agent-switch-hook — tells the model when the active agent changed.
//
// OpenCode resolves the tool set and the permission ruleset per assistant step
// from the newest user message's agent (session/prompt.ts reads
// `lastUser.agent`, and SessionTools.resolve merges `agent.permission` with
// `session.permission`). The model is never told the agent changed, so it
// keeps reasoning from the previous agent's limits and refuses work the new
// agent allows. The Concord posture agents deliberately carry different
// permission sets, so a posture switch the model cannot see defeats that
// separation (issue #677).
//
// `experimental.chat.system.transform` carries only `sessionID` and `model`,
// so the agent name comes from `chat.message` and is correlated by session.
//
// Known limitation: `chat.message` fires inside prompt() before the user
// message persists, so an aborted or errored prompt still updates the tracked
// agent. The from-name can therefore name a prompt that never became a
// persisted message; the to-name is always the agent actually responding.
//
// The SENTINEL is byte-identical to the host-level opencode-agent-switch
// plugin so that whichever emitter runs first wins and the other skips, until
// that plugin is retired (issue #677).

const SENTINEL = "\n\n--- ACTIVE AGENT CHANGED ---\n"

// Session state is process-local and unbounded otherwise. The cap evicts the
// least recently recorded session, which is always a session whose notice was
// either consumed or superseded.
const MAX_SESSIONS = 512

// Title, summary, and agent-authoring calls reuse the session id but are not
// the operator's turn. Consuming the one-shot notice there would drop it.
const internalCallPatterns = [
  /You are a title generator\. You output ONLY a thread title/i,
  /You are a context summarization agent\./i,
  /You are an elite AI agent architect/i,
]

type ChatMessageInput = { sessionID?: string; agent?: string }
type TransformInput = { sessionID?: string }
type TransformOutput = { system: string[] }

export function createAgentSwitchNotice() {
  const lastAgent = new Map<string, string>()
  const pendingNotice = new Map<string, string>()

  function remember(map: Map<string, string>, key: string, value: string): void {
    map.delete(key)
    map.set(key, value)
    while (map.size > MAX_SESSIONS) {
      const oldest = map.keys().next()
      if (oldest.done) break
      map.delete(oldest.value)
    }
  }

  function renderNotice(from: string, to: string): string {
    return (
      `The active agent changed from \`${from}\` to \`${to}\`.\n` +
      `Your tool set and permission rules now come from \`${to}\`. ` +
      `Re-check any capability you found unavailable earlier in this session; do not carry the previous agent's limits forward.\n` +
      `Permissions already granted as "always allow" remain granted for the rest of the session.`
    )
  }

  return {
    chatMessage: async (input: ChatMessageInput): Promise<void> => {
      try {
        const sessionID = input?.sessionID
        const agent = input?.agent
        if (typeof sessionID !== "string" || !sessionID) return
        if (typeof agent !== "string" || !agent) return

        const previous = lastAgent.get(sessionID)
        remember(lastAgent, sessionID, agent)
        if (previous === undefined || previous === agent) return

        remember(pendingNotice, sessionID, renderNotice(previous, agent))
      } catch {
        // A tracking failure must never block the user's turn.
      }
    },

    transform: async (input: TransformInput, output: TransformOutput): Promise<void> => {
      try {
        const sessionID = input?.sessionID
        if (typeof sessionID !== "string" || !sessionID) return

        const notice = pendingNotice.get(sessionID)
        if (!notice) return

        if (!output || !Array.isArray(output.system) || output.system.length === 0) return
        const existing = output.system[0]
        if (typeof existing !== "string") return
        if (internalCallPatterns.some((pattern) => pattern.test(existing))) return

        pendingNotice.delete(sessionID)
        if (existing.includes(SENTINEL)) return
        output.system[0] = existing + SENTINEL + notice
      } catch {
        // Transform failures leave the caller's system prompt unchanged.
      }
    },
  }
}
