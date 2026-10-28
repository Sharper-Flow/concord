import { test, expect } from 'vitest';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { networkInterfaces } from 'node:os';
import { value } from '../src/value';

test('local server', async () => {
  const loopback = Object.values(networkInterfaces()).flat().find(address => address?.internal && address.family === 'IPv4')?.address;
  if (!loopback) throw new Error('the workload has no IPv4 loopback interface');
  const server = createServer((_request, response) => response.end(`synthetic-${value}`));
  server.listen(0, loopback);
  try {
    await once(server, 'listening');
    const address = server.address();
    if (!address || typeof address === 'string') throw new Error('missing synthetic listener');
    const response = await fetch(`http://${loopback}:${address.port}`, { headers: { authorization: 'Bearer synthetic-test-only' } });
    expect(await response.text()).toBe('synthetic-7');
  } finally {
    await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  }
});
