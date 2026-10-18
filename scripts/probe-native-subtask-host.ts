import fs from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"

// The debug tool persists a native subtask but starts no model or worker.
const root = await fs.mkdtemp(path.join(os.tmpdir(), "concord-native-subtask-host-"))
const source = fileURLToPath(new URL("../adapter/opencode/move-session.ts", import.meta.url))
const command = process.argv.length > 2 ? process.argv.slice(2) : [process.env.OPENCODE_BIN ?? "opencode"]
try {
  await fs.mkdir(path.join(root, ".opencode/plugins"), { recursive: true })
  await fs.writeFile(path.join(root, "opencode.json"), JSON.stringify({
    $schema: "https://opencode.ai/config.json",
    agent: {
      build: { model: "fixture/parent-default" },
      "concord-research": { mode: "subagent", description: "Synthetic queue target", model: "fixture/worker-choice" },
    },
  }))
  await fs.writeFile(path.join(root, ".opencode/plugins/native-probe.ts"), `
import { HostControlPlane } from ${JSON.stringify(source)}
export default async ({client}) => {
  const host = new HostControlPlane()
  host.bind({client})
  return { tool: { queue_native_probe: {
    description: "Queue a synthetic native subtask without running a model",
    args: {},
    async execute(_args, context) {
      await client.session.prompt({path: {id: context.sessionID}, body: {agent: context.agent, model: {providerID: "fixture", modelID: "selected-by-user"}, noReply: true, parts: [{type: "text", text: "Synthetic user-selected parent model"}]}})
      await host.queueSubtask(context.sessionID, context.agent, {agent: "concord-research", description: "Synthetic host probe", prompt: "Synthetic packet"}, context.abort)
      const reader = host.sessionReader()
      const parent = await reader.get(context.sessionID, context.abort)
      if (parent.data.model?.providerID !== "fixture" || parent.data.model?.id !== "selected-by-user") throw new Error("queueing changed the user's selected parent model")
      const messages = await reader.messages(context.sessionID, 10, undefined, context.abort)
      const parts = messages.data.flatMap(message => message.parts)
      const subtasks = parts.filter(part => part.type === "subtask")
      if (subtasks.length !== 1 || subtasks[0].prompt !== "Synthetic packet" || subtasks[0].agent !== "concord-research") throw new Error("queued subtask did not persist")
      if (parts.some(part => part.type === "tool" && part.tool === "task")) throw new Error("noReply executed a worker before its parent returned")
      return {title: "Synthetic native subtask", output: JSON.stringify({queued: true, workerStarted: false, parentSelectionPreserved: true}), metadata: {}}
    }
  } } }
}
`)
  const result = Bun.spawnSync([...command, "debug", "agent", "build", "--tool", "queue_native_probe", "--params", "{}"], {
    cwd: root,
    env: {
      PATH: process.env.PATH ?? "",
      HOME: root,
      XDG_CONFIG_HOME: path.join(root, "config"),
      XDG_DATA_HOME: path.join(root, "data"),
      XDG_CACHE_HOME: path.join(root, "cache"),
      OPENCODE_DISABLE_DEFAULT_PLUGINS: "1",
      OPENCODE_DISABLE_EXTERNAL_SKILLS: "1",
    },
    stdout: "pipe",
    stderr: "pipe",
    timeout: 120_000,
  })
  process.stdout.write(result.stdout)
  process.stderr.write(result.stderr)
  process.exitCode = result.exitCode
} finally {
  await fs.rm(root, { recursive: true, force: true })
}
