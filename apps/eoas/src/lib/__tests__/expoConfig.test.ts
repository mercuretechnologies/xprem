import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { describe, expect, it } from 'vitest';

import { createOrModifyExpoConfigAsync, resolveServerUrl } from '../expoConfig';

const guardedUpdates = {
  url: 'https://ota.example.com/manifest',
  codeSigningMetadata:
    "process.env.DISABLE_CODE_SIGNING ? undefined : { keyid: 'main', alg: 'rsa-v1_5-sha256' }",
  codeSigningCertificate:
    "process.env.DISABLE_CODE_SIGNING ? undefined : './certs/certificate.pem'",
  enabled: true,
  requestHeaders: {
    'expo-channel-name': 'process.env.RELEASE_CHANNEL',
    'expo-app-id': 'my-app',
  },
};

// Evaluates a generated app.config.js against a fake process.env, so tests
// never mutate the real environment.
function evaluateConfig(code: string, env: Record<string, string>): any {
  const mod: { exports: any } = { exports: {} };
  const body = code.replace('export default', 'module.exports =');
  // eslint-disable-next-line no-new-func
  new Function('module', 'process', body)(mod, { env });
  return mod.exports({ config: { name: 'demo', slug: 'demo' } });
}

function makeTmpDir(prefix: string): string {
  // eslint-disable-next-line node/no-sync
  return fs.mkdtempSync(path.join(os.tmpdir(), prefix));
}

describe('createOrModifyExpoConfigAsync', () => {
  it('generates a dynamic config from a static app.json, with code signing guarded on DISABLE_CODE_SIGNING', async () => {
    const projectDir = makeTmpDir('eoas-static-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.json'),
      JSON.stringify({ expo: { name: 'demo', slug: 'demo' } }, null, 2)
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const generated = fs.readFileSync(path.join(projectDir, 'app.config.js'), 'utf8');

    const signed = evaluateConfig(generated, { RELEASE_CHANNEL: 'staging' });
    expect(signed.updates.codeSigningCertificate).toBe('./certs/certificate.pem');
    expect(signed.updates.codeSigningMetadata).toEqual({ keyid: 'main', alg: 'rsa-v1_5-sha256' });
    expect(signed.updates.requestHeaders['expo-channel-name']).toBe('staging');
    expect(signed.updates.requestHeaders['expo-app-id']).toBe('my-app');

    const unsigned = evaluateConfig(generated, { DISABLE_CODE_SIGNING: '1' });
    expect(unsigned.updates.codeSigningCertificate).toBeUndefined();
    expect(unsigned.updates.codeSigningMetadata).toBeUndefined();
    expect(unsigned.updates.url).toBe('https://ota.example.com/manifest');
  });

  it('preserves backslashes and quotes of raw expressions when generating from app.json', async () => {
    const projectDir = makeTmpDir('eoas-win-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.json'),
      JSON.stringify({ expo: { name: 'demo', slug: 'demo' } }, null, 2)
    );

    // A Windows path with an apostrophe, escaped for a single-quoted JS string
    // exactly like init.ts does.
    const certificatePath = "C:\\certs d'Axel\\certificate.pem";
    const escaped = certificatePath.replace(/\\/g, '\\\\').replace(/'/g, "\\'");
    await createOrModifyExpoConfigAsync(projectDir, {
      updates: {
        ...guardedUpdates,
        codeSigningCertificate: `process.env.DISABLE_CODE_SIGNING ? undefined : '${escaped}'`,
      },
    });

    // eslint-disable-next-line node/no-sync
    const generated = fs.readFileSync(path.join(projectDir, 'app.config.js'), 'utf8');
    const signed = evaluateConfig(generated, {});
    expect(signed.updates.codeSigningCertificate).toBe(certificatePath);
  });

  it('rewrites the updates key of an existing app.config.js and preserves the rest', async () => {
    const projectDir = makeTmpDir('eoas-dynamic-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.js'),
      `export default ({ config }) => {
  return {
    ...config,
    name: 'demo',
    slug: 'demo',
    updates: { url: 'https://u.expo.dev/xxx' },
  };
};
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.js'), 'utf8');

    const signed = evaluateConfig(modified, {});
    expect(signed.name).toBe('demo');
    expect(signed.updates.url).toBe('https://ota.example.com/manifest');
    expect(signed.updates.codeSigningCertificate).toBe('./certs/certificate.pem');

    const unsigned = evaluateConfig(modified, { DISABLE_CODE_SIGNING: '1' });
    expect(unsigned.updates.codeSigningCertificate).toBeUndefined();
    expect(unsigned.updates.codeSigningMetadata).toBeUndefined();
  });

  it('rewrites a TypeScript app.config.ts and preserves its type annotations', async () => {
    const projectDir = makeTmpDir('eoas-ts-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.ts'),
      `import { ExpoConfig } from '@expo/config-types';
import { ConfigContext } from '@expo/config';

export default ({ config }: ConfigContext): ExpoConfig => {
  return {
    ...(config as ExpoConfig),
    runtimeVersion: '1.0.0',
    updates: { url: 'https://old.example.com/manifest' },
  };
};
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.ts'), 'utf8');
    expect(modified).toContain('({ config }: ConfigContext): ExpoConfig');
    expect(modified).toContain('...(config as ExpoConfig)');
    expect(modified).toContain('https://ota.example.com/manifest');
    expect(modified).not.toContain('https://old.example.com/manifest');
    expect(modified).toContain(
      "process.env.DISABLE_CODE_SIGNING ? undefined : './certs/certificate.pem'"
    );
    // The updates key must be replaced, not duplicated.
    expect(modified.match(/updates:/g)).toHaveLength(1);
  });

  it('handles an arrow function with an expression body', async () => {
    const projectDir = makeTmpDir('eoas-expr-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.js'),
      `export default ({ config }) => ({ ...config, name: 'demo', slug: 'demo' });
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.js'), 'utf8');
    const signed = evaluateConfig(modified, {});
    expect(signed.name).toBe('demo');
    expect(signed.updates.codeSigningCertificate).toBe('./certs/certificate.pem');
  });

  it('handles a TypeScript expression-body arrow returning a cast object', async () => {
    const projectDir = makeTmpDir('eoas-ts-expr-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.ts'),
      `import { ExpoConfig } from '@expo/config-types';
import { ConfigContext } from '@expo/config';

export default ({ config }: ConfigContext) =>
  ({ ...config, name: 'demo', slug: 'demo' }) as ExpoConfig;
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.ts'), 'utf8');
    expect(modified).toContain('https://ota.example.com/manifest');
    expect(modified).toContain(
      "process.env.DISABLE_CODE_SIGNING ? undefined : './certs/certificate.pem'"
    );
  });

  it('handles a CommonJS module.exports config', async () => {
    const projectDir = makeTmpDir('eoas-cjs-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.js'),
      `module.exports = ({ config }) => {
  return { ...config, name: 'demo', slug: 'demo' };
};
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.js'), 'utf8');
    const mod: { exports: any } = { exports: {} };
    // eslint-disable-next-line no-new-func
    new Function('module', 'process', modified)(mod, { env: {} });
    const signed = mod.exports({ config: {} });
    expect(signed.updates.codeSigningCertificate).toBe('./certs/certificate.pem');
  });

  it('handles a config exporting a plain object', async () => {
    const projectDir = makeTmpDir('eoas-object-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.js'),
      `export default { name: 'demo', slug: 'demo' };
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.js'), 'utf8');
    expect(modified).toContain("name: 'demo'");
    expect(modified).toContain('https://ota.example.com/manifest');
  });

  it('is idempotent: a second run replaces the updates key instead of duplicating it', async () => {
    const projectDir = makeTmpDir('eoas-rerun-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.ts'),
      `import { ConfigContext } from '@expo/config';

export default ({ config }: ConfigContext) => {
  return { ...config, name: 'demo', slug: 'demo' };
};
`
    );

    await createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates });
    await createOrModifyExpoConfigAsync(projectDir, {
      updates: { ...guardedUpdates, url: 'https://second-run.example.com/manifest' },
    });

    // eslint-disable-next-line node/no-sync
    const modified = fs.readFileSync(path.join(projectDir, 'app.config.ts'), 'utf8');
    expect(modified).toContain('https://second-run.example.com/manifest');
    expect(modified).not.toContain('https://ota.example.com/manifest');
    expect(modified.match(/updates:/g)).toHaveLength(1);
  });

  it('fails loudly when the exported config object cannot be located', async () => {
    const projectDir = makeTmpDir('eoas-opaque-');
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(
      path.join(projectDir, 'app.config.js'),
      `const makeConfig = require('./make-config');
module.exports = makeConfig();
`
    );

    await expect(
      createOrModifyExpoConfigAsync(projectDir, { updates: guardedUpdates })
    ).rejects.toThrow('Could not find the exported config object');
  });
});

describe('resolveServerUrl', () => {
  const config = { updates: { url: 'https://ota.example.com/manifest' } } as any;

  it('falls back to the origin of the Expo config update url', async () => {
    await expect(resolveServerUrl(config)).resolves.toBe('https://ota.example.com');
  });

  it('prefers the custom server url and keeps only its origin', async () => {
    await expect(resolveServerUrl(config, 'https://publish.example.com/some/path')).resolves.toBe(
      'https://publish.example.com'
    );
  });

  it('resolves without any update url in the config when a custom one is passed', async () => {
    await expect(resolveServerUrl({} as any, 'https://publish.example.com')).resolves.toBe(
      'https://publish.example.com'
    );
  });

  it('rejects when neither the config nor the flag provides a url', async () => {
    await expect(resolveServerUrl({} as any)).rejects.toThrow('Update url is not setup');
  });

  it('rejects an unparseable custom server url', async () => {
    await expect(resolveServerUrl(config, 'not-a-url')).rejects.toThrow('Invalid --serverUrl');
  });
});
