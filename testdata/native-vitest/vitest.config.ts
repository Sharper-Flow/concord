import { defineConfig } from 'vitest/config';
import { sveltekit } from '@sveltejs/kit/vite';

export default defineConfig({
  plugins: [sveltekit()],
  test: {
    projects: [
      { extends: true, test: { name: 'server', environment: 'node', include: ['tests/server.test.ts'] } },
      {
        extends: true,
        resolve: { conditions: ['browser'] },
        test: { name: 'app', environment: 'jsdom', include: ['tests/app.test.ts'], setupFiles: ['tests/setup.ts'] },
      },
    ],
  },
});
