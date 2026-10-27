import { expect, test } from "bun:test";
import { archiveSpecs } from "./ci-warm-native-npm";
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";
import { chmod, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";

const entry = (spec: string, metadata = {}) => [spec, "", metadata, "sha512-YWJjZA=="];
test("warms every platform archive once with exact npm specs", () => {
  expect(archiveSpecs({ lockfileVersion: 2, packages: {
    other: entry("pkg@1.2.3"),
    nested: entry("pkg@1.2.3"),
    mac: entry("@scope/darwin@4.5.6", { os: "darwin" }),
    prerelease: entry("pre@2.0.0-alpha.1+build"),
    bundled: ["embedded@1.0.0", "", { bundled: true }, "sha1-bundled"],
  } })).toEqual(["@scope/darwin@4.5.6", "pkg@1.2.3", "pre@2.0.0-alpha.1+build"]);
});

test("refuses malformed or non-registry closure before npm execution", () => {
  for (const bad of [
    ["https://evil.invalid/archive", "", {}, "sha512-YWJjZA=="],
    entry("--registry=https://evil.invalid"),
    ["pkg@1.0.0", "https://evil.invalid", {}, "sha512-YWJjZA=="],
    ["pkg@1.0.0", "", {}, "sha1-YWJjZA=="],
    ["pkg@1.0.0"], null,
  ]) expect(() => archiveSpecs({ lockfileVersion: 1, packages: { bad } })).toThrow();
  for (const lock of [{}, { lockfileVersion: 0, packages: {} }, { lockfileVersion: 1, packages: {} }]) {
    expect(() => archiveSpecs(lock)).toThrow();
  }
});

test("the generated public fixture lock parses with Bun's actual JSONC loader", async () => {
  const lock = (await import(pathToFileURL(resolve(import.meta.dir, "../internal/store/testdata/native-vitest/bun.lock")).href)).default;
  const specs = archiveSpecs(lock);
  expect(specs).toContain("vitest@4.1.11");
  expect(specs).toContain("vite@8.2.2");
  expect(specs.length).toBeGreaterThan(15);
});

test("real warmer invokes only script-disabled public npm cache adds in a clean environment", async () => {
  const scratch = await mkdtemp(resolve(tmpdir(), "native-npm-warm-test-"));
  try {
    const cache = resolve(scratch, "cache");
    await mkdir(cache);
    await Bun.write(resolve(scratch, "node"), "#!/bin/sh\nprintf 'v24.15.0\\n'\n");
    await Bun.write(resolve(scratch, "npm"), `#!/bin/sh
set -eu
[ -z "\${NPM_CONFIG_REGISTRY+x}" ] || exit 42
cache="\${5#--cache=}"
printf '%s\\0' "$@" >> "$cache/calls"
`);
    await chmod(resolve(scratch, "node"), 0o700);
    await chmod(resolve(scratch, "npm"), 0o700);
    const command = [process.execPath, resolve(import.meta.dir, "ci-warm-native-npm.ts")];
    const env = { ...process.env, PATH: `${scratch}:/usr/bin:/bin`, OC_NATIVE_NPM_CACHE: cache,
      NPM_CONFIG_REGISTRY: "https://must-not-be-used.invalid/" };
    const result = Bun.spawnSync(command, { env });
    expect(result.stderr.toString()).toBe("");
    expect(result.exitCode).toBe(0);
    const args = (await readFile(resolve(cache, "calls"), "utf8")).split("\0").slice(0, -1);
    const lock = (await import(pathToFileURL(resolve(import.meta.dir, "../internal/store/testdata/native-vitest/bun.lock")).href)).default;
    const specs = archiveSpecs(lock);
    expect(args.length).toBe(specs.length * 9);
    for (let i = 0; i < specs.length; i++) {
      const invocation = args.slice(i * 9, (i + 1) * 9);
      expect(invocation.slice(0, 5)).toEqual(["cache", "add", "--ignore-scripts", "--registry=https://registry.npmjs.org/", `--cache=${cache}`]);
      expect(invocation[5]).toStartWith("--userconfig=");
      expect(invocation[6]).toStartWith("--globalconfig=");
      expect(invocation.slice(7)).toEqual(["--", specs[i]]);
    }
    await Bun.write(resolve(scratch, "node"), "#!/bin/sh\nprintf 'v22.0.0\\n'\n");
    expect(Bun.spawnSync(command, { env }).exitCode).not.toBe(0);
    expect((await readFile(resolve(cache, "calls"), "utf8")).split("\0").length - 1).toBe(args.length);
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}, 30_000);
