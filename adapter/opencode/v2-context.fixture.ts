import type { Plugin } from "@opencode/plugin"

export function fakeV2Context(get: (input: { sessionID: string }) => Promise<any> = async () => ({ location: { directory: "/synthetic/project" } })) {
  const tools: any[] = []
  const hooks: Array<{ name: string; hook: any }> = []
  const context = {
    location: { directory: "/synthetic/project" },
    tool: { transform: async (transform: any) => transform({ add: (tool: any) => tools.push(tool) }) },
    session: { get, hook: async (name: string, hook: any) => { hooks.push({ name, hook }) } },
  } as unknown as Plugin.Context
  return { context, tools, hooks }
}
