import { mock } from "bun:test"
import { fakeTool } from "./plugin-tool.fixture"
import "./test_preload"

// The acceptance harness exercises the native plugin through the repository's
// host fixture, not an ambient or automatically installed host package.
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))
