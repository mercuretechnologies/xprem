/* eslint-disable node/no-sync */
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, describe, expect, it } from 'vitest';

import { digestFile, toBase64Url } from '../crypto';

let tmpDir: string;

afterEach(() => {
  if (tmpDir) {
    fs.removeSync(tmpDir);
  }
});

describe('digestFile', () => {
  it('matches the server ManifestAsset hash and key encodings', async () => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-hash-'));
    const filePath = path.join(tmpDir, 'hello');
    fs.writeFileSync(filePath, 'hello');
    await expect(digestFile(filePath)).resolves.toEqual({
      hash: 'LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ',
      key: '5d41402abc4b2a76b9719d911017c592',
    });
  });

  it('is sha256 of the bytes, then base64url', () => {
    expect(
      toBase64Url(
        Buffer.from('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824', 'hex')
      )
    ).toBe('LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ');
  });
});
