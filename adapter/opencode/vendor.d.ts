// Dev-only ambient declarations for the adapter typecheck. This file is part of
// the typecheck toolchain, not the installed archive (it is NOT in
// install.ADAPTER_FILES). It mirrors the host surface the adapter uses:
// `@opencode-ai/plugin` pinned 1.18.x and the Bun/Node globals the adapter
// imports from. Update this file when the adapter uses a new host surface, the
// way the worker-evidence vector records what the adapter believes Go's
// canonical encoder emits.
declare module "@opencode-ai/plugin" {
  // ToolContext is the host-injected tool-call surface. Only fields the adapter
  // actually reads appear here; an unused surface is an unsound surface.
  export interface ToolContext {
    sessionID: string
    messageID: string
    agent: string
    directory: string
    worktree: string
    abort: AbortSignal
    ask(input: unknown): Promise<void>
  }

  // Zod-like schema namespace. The adapter's argsSchema / zodSchema helpers
  // compose these to build a per-tool input validator; every chainable method
  // returns ZodLike so the local `let value = ...` reassignment pattern in
  // zodSchema does not lose method availability mid-chain.
  interface ZodLike {
    literal(value: unknown): ZodLike
    union(values: ZodLike[]): ZodLike
    object(shape: Record<string, ZodLike>): ZodLike
    array(item: ZodLike): ZodLike
    string(): ZodLike
    number(): ZodLike
    unknown(): ZodLike
    null(): ZodLike
    strict(): ZodLike
    min(n: number): ZodLike
    max(n: number): ZodLike
    int(): ZodLike
    regex(pattern: RegExp): ZodLike
  }

  export interface ToolDefinition {
    description: string
    args?: unknown
    execute(args: any, context: ToolContext): unknown
  }

  // tool() is called with a definition and returns the definition itself with
  // its typed shape; the namespace adds the static `.schema` zod namespace the
  // adapter's argsSchema helper reads. The constraint binds the return type to
  // the input so `.execute` is callable on the result, the way adapter tests
  // exercise the tool body directly.
  export function tool<T extends ToolDefinition>(definition: T): T
  export namespace tool {
    export const schema: ZodLike
  }
}

// bun:test surface used by adapter test files. Matchers are limited to the
// set observed across adapter/opencode/*.test.ts. The optional second arg
// carries a diagnostic message, mirroring the runtime's `expect(value, hint)`.
declare module "bun:test" {
  export function test(name: string, fn: () => void | Promise<void>): void

  interface Expectation<T> {
    toBe(expected: T): void
    toBe(expected: T, hint?: string): void
    toEqual(expected: unknown): void
    toEqual(expected: unknown, hint?: string): void
    toContain(expected: unknown): void
    toContain(expected: unknown, hint?: string): void
    toMatch(pattern: string | RegExp): void
    toMatch(pattern: string | RegExp, hint?: string): void
    toBeDefined(): void
    toBeDefined(hint?: string): void
    toBeUndefined(): void
    toBeUndefined(hint?: string): void
    toBeNull(): void
    toBeNull(hint?: string): void
    toBeGreaterThan(expected: number): void
    toBeGreaterThan(expected: number, hint?: string): void
    toBeGreaterThanOrEqual(expected: number): void
    toBeGreaterThanOrEqual(expected: number, hint?: string): void
    toHaveLength(expected: number): void
    toHaveLength(expected: number, hint?: string): void
    not: Expectation<T>
  }

  export function expect<T = unknown>(value: T): Expectation<T>
  export function expect<T = unknown>(value: T, hint?: string): Expectation<T>

  // mock.module replaces a module's exports for the rest of the test run.
  export const mock: {
    module(specifier: string, factory: () => Record<string, unknown>): void
  }
}

// Bun globals used by the adapter. The signatures are pinned to the call sites
// in dispatch.ts, concord.ts, credentials.ts, and dispatch.test.ts.
declare const Bun: {
  spawn(argv: string[], options?: { stdin?: "pipe" | "ignore"; stdout?: "pipe" | "ignore"; stderr?: "pipe" | "ignore" }): {
    stdin: { write(chunk: string): Promise<number>; end(): Promise<void> }
    stdout: ReadableStream<Uint8Array>
    stderr: ReadableStream<Uint8Array>
    exited: Promise<number>
    kill(): void
  }
  file(path: string | URL): {
    exists(): Promise<boolean>
    text(): Promise<string>
    arrayBuffer(): Promise<ArrayBuffer>
  }
  write(destination: string, contents: string | Uint8Array | ArrayBuffer): Promise<number>
  readonly Glob: new (pattern: string) => { scan(options: { cwd: string; onlyFiles: boolean }): AsyncIterable<string> }
  readonly SHA256: { hash(input: string | Uint8Array, encoding: "hex"): string }
}

// Buffer is a Uint8Array subclass with encoding-aware toString. The adapter
// uses hex and base64 string conversions, plus byte length and concat helpers;
// the chainable toString is what `Buffer.from(...).toString("base64")` needs.
interface Buffer extends Uint8Array {
  toString(encoding?: "utf8" | "base64" | "hex"): string
}
declare const Buffer: {
  from(input: string, encoding: "hex" | "base64"): Buffer
  from(input: Uint8Array): Buffer
  from(input: ArrayBuffer): Buffer
  from(input: string): Buffer
  concat(parts: Uint8Array[]): Buffer
  byteLength(input: string, encoding?: "utf8" | "hex" | "base64"): number
}

// process.env is read off many CONCORD_* and host-injected variables and
// mutated by tests that swap a variable and restore it. process.cwd() is the
// default for computeHostPromptProvenance's spawn directory.
declare const process: {
  env: { [key: string]: string | undefined }
  cwd(): string
}

// TextEncoder is part of Bun/Node's WHATWG surface; the adapter only invokes
// the default constructor and .encode().
declare const TextEncoder: { new (): { encode(input?: string): Uint8Array } }

// URL is constructed from a string base + relative in dispatch.test.ts only
// (new URL("./concord.ts", import.meta.url)). Declare the surface narrowly.
declare const URL: { new (input: string, base?: string | URL): URL }
interface URL {
  toString(): string
}

// AbortController / AbortSignal are in every dispatch path's runner signature.
// The adapter constructs an AbortController to default the signal and reads
// .signal + .aborted. Declared as a class so it can be used as a type as well
// as a value (the test helpers' contextFor default parameter, for one).
declare class AbortController {
  constructor()
  readonly signal: AbortSignal
  abort(): void
}
interface AbortSignal {
  readonly aborted: boolean
  addEventListener(type: "abort", listener: () => void, options?: { once?: boolean }): void
  removeEventListener(type: "abort", listener: () => void): void
}

// Response wraps a Bun child stream so its bytes can be awaited as text.
declare const Response: {
  new (body: ReadableStream<Uint8Array>): { text(): Promise<string> }
}

// crypto.randomUUID appears in dispatch.ts and credentials.ts.
declare const crypto: { randomUUID(): string }

// Array.fromAsync is what dispatch.ts uses to drain a Bun.Glob scan into an
// array. It is part of esnext, not es2022, so the dev-only tsconfig leaves
// the lib at es2022 and declares the helper here.
interface ArrayConstructor {
  fromAsync<T>(iterable: AsyncIterable<T>): Promise<T[]>
}

// Node built-in modules the adapter imports. Only the symbols each module
// exports are declared; this is the dev-only surface, not @types/node.
declare module "node:crypto" {
  export interface KeyObject { readonly type: string }
  export function sign(algorithm: string | null, data: Buffer | Uint8Array, key: KeyObject): Buffer
  export function createPrivateKey(input: { key: Buffer; format: "der"; type: "pkcs8" }): KeyObject
}
declare module "node:fs/promises" {
  export function mkdtemp(prefix: string): Promise<string>
}
declare module "node:path" {
  export function join(...parts: string[]): string
  export function resolve(...parts: string[]): string
}
declare module "node:os" {
  export function tmpdir(): string
}

// Augment ImportMeta so test files can read import.meta.url without enabling
// the dom lib.
interface ImportMeta {
  readonly url: string
}
