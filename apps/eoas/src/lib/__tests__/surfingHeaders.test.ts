import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { describe, expect, it } from 'vitest';

import { createOrModifyExpoConfigAsync } from '../expoConfig';

// eoas init has to declare these, because expo-updates only accepts a runtime
// override for header keys that existed at build time. A build missing one can
// be sent into a state where every poll loses that header for good.
const updates = {
  url: 'https://ota.example.com/manifest',
  requestHeaders: {
    'expo-channel-name': {
      __comment: 'Declare as a literal if you surf branches: see xprem-branch below.',
      value: 'process.env.RELEASE_CHANNEL',
    },
    'expo-app-id': 'app-1',
    'xprem-branch': {
      __comment: 'Branch surfing — the branch to serve; empty means the channel decides.',
      value: '',
    },
  },
};

function project(configName: string, contents: string): string {
  // eslint-disable-next-line node/no-sync
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-surf-'));
  // eslint-disable-next-line node/no-sync
  fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify({ name: 'app' }));
  // eslint-disable-next-line node/no-sync
  fs.writeFileSync(path.join(dir, configName), contents);
  return dir;
}

function read(dir: string, fileName: string): string {
  // eslint-disable-next-line node/no-sync
  return fs.readFileSync(path.join(dir, fileName), 'utf8');
}

describe('init writes the branch-surfing headers', () => {
  it('emits the keys and their comments into app.config.ts', async () => {
    const dir = project('app.config.ts', 'export default { expo: { name: "app" } };\n');

    await createOrModifyExpoConfigAsync(dir, { updates });

    const written = read(dir, 'app.config.ts');
    expect(written).toContain('xprem-branch');
    expect(written).toContain('// Branch surfing — the branch to serve');
    // The channel is emitted as an expression, not the string "process.env...".
    expect(written).toContain('process.env.RELEASE_CHANNEL');
    expect(written).not.toContain("'process.env.RELEASE_CHANNEL'");
    // The marker itself must never reach the file.
    expect(written).not.toContain('__comment');
  });

  // A static app.json is rewritten as app.config.js through JSON.stringify,
  // which has no room for a comment. What matters there is that the marker is
  // stripped rather than serialised into the file the user has to live with.
  it('drops the comment marker on the generated static config', async () => {
    const dir = project('app.json', JSON.stringify({ expo: { name: 'app' } }, null, 2));

    await createOrModifyExpoConfigAsync(dir, { updates });

    const written = read(dir, 'app.config.js');
    expect(written).toContain('xprem-branch');
    expect(written).not.toContain('__comment');
    expect(written).toContain('process.env.RELEASE_CHANNEL');
  });
});
