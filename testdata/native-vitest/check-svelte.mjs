import { spawnSync } from 'node:child_process';

for (const argv of [['node_modules/@sveltejs/kit/svelte-kit.js', 'sync'], ['node_modules/svelte-check/bin/svelte-check', '--tsconfig', './tsconfig.json', '--fail-on-warnings']]) {
  const child = spawnSync(process.execPath, argv, { stdio: 'inherit', env: process.env });
  if (child.error) throw child.error;
  if (child.status !== 0) process.exit(child.status ?? 1);
}
