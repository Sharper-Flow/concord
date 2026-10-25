import { tool } from "@opencode-ai/plugin"
import { appendFileSync } from "node:fs"

// Observation only. The candidate plugin receives the unmodified host client.
function record(value: unknown) {
  appendFileSync(process.env.NATIVE_RESUME_TRACE!, JSON.stringify(value) + "\n")
}

export default async function observer(input: any) {
  record({ kind: "instance", directory: input.directory, worktree: input.worktree, pid: process.pid })
  return {
    tool: {
      fixture_context: tool({
        description: "Read synthetic fixture native tool context without changing it",
        args: {},
        async execute(_args, context) {
          const value = { kind: "context", sessionID: context.sessionID, directory: context.directory, worktree: context.worktree, pid: process.pid }
          record(value)
          return JSON.stringify(value)
        },
      }),
    },
    "tool.execute.before": async (context: unknown, output: unknown) => {
      record({ kind: "before", context, output })
    },
    "tool.execute.after": async (context: unknown, output: unknown) => {
      record({ kind: "after", context, output })
    },
  }
}
