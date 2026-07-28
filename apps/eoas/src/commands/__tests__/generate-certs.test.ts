// Disk behaviour of the generate-certs command: the private key lands as 0600,
// and an existing one is only replaced after an explicit confirmation. Key
// generation is stubbed, the prompts are answered from the test.
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { confirmAsync, promptAsync } from '../../lib/prompts';
import GenerateCerts from '../generate-certs';

vi.mock('../../lib/prompts', () => ({
  promptAsync: vi.fn(),
  confirmAsync: vi.fn(),
}));
vi.mock('@expo/code-signing-certificates', () => ({
  generateKeyPair: () => ({}),
  generateSelfSignedCodeSigningCertificate: () => ({}),
  convertKeyPairToPEM: () => ({ publicKeyPEM: 'PUBLIC KEY', privateKeyPEM: 'PRIVATE KEY' }),
  convertCertificateToCertificatePEM: () => 'CERTIFICATE',
}));

const eoasRoot = path.resolve(__dirname, '../../..');
const onWindows = process.platform === 'win32';

let projectDir: string;
let certsDir: string;
let privateKeyPath: string;

const answers: Record<string, unknown> = {
  certificateOutputDir: './certs',
  keyOutputDir: './certs',
  certificateCommonName: 'Test Org',
  certificateValidityDurationYears: 10,
};

function promptedNames(): string[] {
  return vi
    .mocked(promptAsync)
    .mock.calls.map(([questions]) =>
      String((Array.isArray(questions) ? questions[0] : questions).name)
    );
}

function permissions(filePath: string): string {
  // eslint-disable-next-line node/no-sync
  return (fs.statSync(filePath).mode & 0o777).toString(8);
}

// Permissions a plain fs write gets in this environment, so the assertions on
// the public files do not depend on the umask of the machine running the tests.
function defaultPermissions(): string {
  const probePath = path.join(projectDir, 'probe');
  // eslint-disable-next-line node/no-sync
  fs.writeFileSync(probePath, 'probe');
  return permissions(probePath);
}

describe('generate-certs command', () => {
  beforeEach(() => {
    // eslint-disable-next-line node/no-sync
    projectDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-generate-certs-'));
    certsDir = path.join(projectDir, 'certs');
    privateKeyPath = path.join(certsDir, 'private-key.pem');
    // eslint-disable-next-line node/no-sync
    fs.ensureDirSync(certsDir);
    vi.spyOn(process, 'cwd').mockReturnValue(projectDir);
    vi.mocked(promptAsync).mockImplementation(async (questions: any) => {
      const question = Array.isArray(questions) ? questions[0] : questions;
      if (!(question.name in answers)) {
        throw new Error(`Unexpected prompt: ${question.name}`);
      }
      return { [question.name]: answers[question.name] };
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.clearAllMocks();
    // eslint-disable-next-line node/no-sync
    fs.removeSync(projectDir);
  });

  it.skipIf(onWindows)('writes the private key readable by its owner only', async () => {
    await GenerateCerts.run([], eoasRoot);

    // eslint-disable-next-line node/no-sync
    expect(fs.readFileSync(privateKeyPath, 'utf8')).toBe('PRIVATE KEY');
    expect(permissions(privateKeyPath)).toBe('600');
    // Public material keeps the permissions any other file would get.
    expect(permissions(path.join(certsDir, 'public-key.pem'))).toBe(defaultPermissions());
    expect(permissions(path.join(certsDir, 'certificate.pem'))).toBe(defaultPermissions());
    expect(confirmAsync).not.toHaveBeenCalled();
  });

  it('keeps the existing private key when the overwrite is declined', async () => {
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(privateKeyPath, 'EXISTING KEY');
    vi.mocked(confirmAsync).mockResolvedValue(false);

    await GenerateCerts.run([], eoasRoot);

    // eslint-disable-next-line node/no-sync
    expect(fs.readFileSync(privateKeyPath, 'utf8')).toBe('EXISTING KEY');
    // eslint-disable-next-line node/no-sync
    expect(fs.existsSync(path.join(certsDir, 'certificate.pem'))).toBe(false);
    expect(confirmAsync).toHaveBeenCalledTimes(1);
    // Aborted before asking anything else.
    expect(promptedNames()).toEqual(['certificateOutputDir', 'keyOutputDir']);
  });

  it.skipIf(onWindows)(
    'replaces a loosely permissioned private key with a 0600 one when confirmed',
    async () => {
      // eslint-disable-next-line node/no-sync
      fs.writeFileSync(privateKeyPath, 'EXISTING KEY', { mode: 0o644 });
      vi.mocked(confirmAsync).mockResolvedValue(true);

      await GenerateCerts.run([], eoasRoot);

      // eslint-disable-next-line node/no-sync
      expect(fs.readFileSync(privateKeyPath, 'utf8')).toBe('PRIVATE KEY');
      expect(permissions(privateKeyPath)).toBe('600');
    }
  );
});
