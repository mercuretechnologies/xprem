// A re-run of server:init must keep the master key already in the
// configuration: it seals the OTA signing keys and the SSO client secret in the
// database, and there is no rotation path back.
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { readExistingMasterKey } from '../server/init';

const MASTER_KEY = 'bWFzdGVyLWtleS10aGF0LW11c3Qtc3Vydml2ZQ==';

let projectDir: string;

function writeEnv(content: string): void {
  // eslint-disable-next-line node/no-sync
  fs.writeFileSync(path.join(projectDir, '.env.xprem'), content);
}

function writeHelmSecrets(content: string): void {
  const outDir = path.join(projectDir, 'xprem-helm');
  // eslint-disable-next-line node/no-sync
  fs.ensureDirSync(outDir);
  // eslint-disable-next-line node/no-sync
  fs.writeFileSync(path.join(outDir, 'secrets.yaml'), content);
}

describe('readExistingMasterKey', () => {
  beforeEach(() => {
    // eslint-disable-next-line node/no-sync
    projectDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-server-init-'));
    vi.spyOn(process, 'cwd').mockReturnValue(projectDir);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    // eslint-disable-next-line node/no-sync
    fs.removeSync(projectDir);
  });

  it('reports nothing when no configuration exists', async () => {
    await expect(readExistingMasterKey('docker')).resolves.toEqual({ unreadable: false });
  });

  it('carries the key over from an existing env file', async () => {
    writeEnv(`BASE_URL=https://ota.example.com\nDB_KEYS_MASTER_KEY_B64=${MASTER_KEY}\n`);

    await expect(readExistingMasterKey('docker')).resolves.toEqual({
      key: MASTER_KEY,
      unreadable: false,
    });
  });

  it('carries the key over from an existing helm secrets file', async () => {
    writeHelmSecrets(`secretEnv:\n  JWT_SECRET: jwt\n  DB_KEYS_MASTER_KEY_B64: ${MASTER_KEY}\n`);

    await expect(readExistingMasterKey('helm')).resolves.toEqual({
      key: MASTER_KEY,
      unreadable: false,
    });
  });

  it('ignores a placeholder left by a hand-edited file', async () => {
    writeEnv('DB_KEYS_MASTER_KEY_B64=<base64 32-byte key>\n');

    await expect(readExistingMasterKey('docker')).resolves.toEqual({ unreadable: false });
  });

  it('reads the file matching the deployment, not the other one', async () => {
    writeEnv(`DB_KEYS_MASTER_KEY_B64=${MASTER_KEY}\n`);

    await expect(readExistingMasterKey('helm')).resolves.toEqual({ unreadable: false });
  });

  it('flags an unparseable file instead of reporting no key', async () => {
    writeHelmSecrets('- this is a list\n- not a mapping\n');

    await expect(readExistingMasterKey('helm')).resolves.toEqual({ unreadable: true });
  });
});
