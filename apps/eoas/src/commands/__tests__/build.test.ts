import path from 'path';
import { beforeEach, expect, it, vi } from 'vitest';

import { buildAndroid } from '../../lib/build/android';
import Build from '../build';

vi.mock('../../lib/build/android', () => ({ buildAndroid: vi.fn() }));
vi.mock('../../lib/build/ios', () => ({ buildIos: vi.fn() }));

beforeEach(() => vi.clearAllMocks());

it.each([
  { flags: [], remoteCache: true },
  { flags: ['--no-remote-cache'], remoteCache: false },
])('passes remoteCache=$remoteCache to the Android build', async ({ flags, remoteCache }) => {
  await Build.run(
    ['--profile', 'test', '--platform', 'android', ...flags],
    path.resolve(__dirname, '../../..')
  );
  expect(buildAndroid).toHaveBeenCalledWith(
    process.cwd(),
    expect.objectContaining({ profile: 'test', remoteCache })
  );
});
