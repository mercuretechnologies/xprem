import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { describe, expect, it } from 'vitest';

import { ensureGitIgnored, ensurePrivateKeyIgnored, isValidUpdateUrl } from '../utils';

describe('isValidUpdateUrl', () => {
  it('accepts a bare https origin', () => {
    expect(isValidUpdateUrl('https://customota.com')).toBe(true);
    expect(isValidUpdateUrl('http://localhost:3000')).toBe(true);
  });

  it('accepts a path prefix the server is mounted under', () => {
    expect(isValidUpdateUrl('https://api.example.com/ota')).toBe(true);
    expect(isValidUpdateUrl('https://api.example.com/path1/path2')).toBe(true);
  });

  it('rejects the /manifest device endpoint, a missing scheme, or a non-http scheme', () => {
    expect(isValidUpdateUrl('https://customota.com/manifest')).toBe(false);
    expect(isValidUpdateUrl('https://api.example.com/ota/manifest')).toBe(false);
    expect(isValidUpdateUrl('customota.com')).toBe(false);
    expect(isValidUpdateUrl('ftp://customota.com')).toBe(false);
  });

  it('rejects a query string or fragment', () => {
    expect(isValidUpdateUrl('https://api.example.com/ota?foo=1')).toBe(false);
    expect(isValidUpdateUrl('https://api.example.com/ota#section')).toBe(false);
  });
});

function makeProject(gitignoreContent?: string): string {
  // eslint-disable-next-line node/no-sync
  const projectDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-gitignore-'));
  if (gitignoreContent !== undefined) {
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(path.join(projectDir, '.gitignore'), gitignoreContent);
  }
  return projectDir;
}

function readGitignore(projectDir: string): string {
  // eslint-disable-next-line node/no-sync
  return fs.readFileSync(path.join(projectDir, '.gitignore'), 'utf8');
}

describe('ensurePrivateKeyIgnored', () => {
  it('creates a .gitignore when the project has none', () => {
    const projectDir = makeProject();
    ensurePrivateKeyIgnored(projectDir);
    expect(readGitignore(projectDir)).toBe(
      '# Code signing private key (server-side secret, never commit it)\nprivate-key.pem\n'
    );
  });

  it('appends to an existing .gitignore without touching its content', () => {
    const projectDir = makeProject('node_modules/\n.expo/\n');
    ensurePrivateKeyIgnored(projectDir);
    const gitignore = readGitignore(projectDir);
    expect(gitignore.startsWith('node_modules/\n.expo/\n')).toBe(true);
    expect(gitignore).toContain('\nprivate-key.pem\n');
  });

  it('terminates the last line when the existing file has no trailing newline', () => {
    const projectDir = makeProject('node_modules/');
    ensurePrivateKeyIgnored(projectDir);
    expect(readGitignore(projectDir)).toContain('node_modules/\n');
    expect(readGitignore(projectDir)).toContain('\nprivate-key.pem\n');
  });

  it('does nothing when a bare private-key.pem rule already exists', () => {
    const existing = 'node_modules/\nprivate-key.pem\n';
    const projectDir = makeProject(existing);
    ensurePrivateKeyIgnored(projectDir);
    expect(readGitignore(projectDir)).toBe(existing);
  });

  it('adds the generic rule when only a path-specific entry exists', () => {
    const projectDir = makeProject('certs/private-key.pem\n');
    ensurePrivateKeyIgnored(projectDir);
    const lines = readGitignore(projectDir).trim().split('\n');
    expect(lines[0]).toBe('certs/private-key.pem');
    expect(lines[lines.length - 1]).toBe('private-key.pem');
  });

  it('is not fooled by a comment mentioning the key', () => {
    const projectDir = makeProject('# private-key.pem is sensitive\n');
    ensurePrivateKeyIgnored(projectDir);
    const lines = readGitignore(projectDir).trim().split('\n');
    expect(lines[lines.length - 1]).toBe('private-key.pem');
  });

  it('appends after a negated entry so the final rule wins', () => {
    const projectDir = makeProject('private-key.pem\n!private-key.pem\n');
    ensurePrivateKeyIgnored(projectDir);
    const lines = readGitignore(projectDir).trim().split('\n');
    expect(lines[lines.length - 1]).toBe('private-key.pem');
    expect(lines.lastIndexOf('private-key.pem')).toBeGreaterThan(
      lines.lastIndexOf('!private-key.pem')
    );
  });
});

// server:init ignores its secret files through the same helper, with a
// path-bearing pattern rather than a bare filename.
describe('ensureGitIgnored', () => {
  it('adds a path-bearing pattern with its reason', () => {
    const projectDir = makeProject('node_modules/\n');
    ensureGitIgnored(projectDir, 'xprem-helm/secrets.yaml', 'holds server secrets');
    const gitignore = readGitignore(projectDir);
    expect(gitignore).toContain('# holds server secrets\nxprem-helm/secrets.yaml\n');
    expect(gitignore.startsWith('node_modules/\n')).toBe(true);
  });

  it('does not duplicate a rule that is already in force', () => {
    const projectDir = makeProject('.env.xprem\n');
    ensureGitIgnored(projectDir, '.env.xprem', 'holds server secrets');
    expect(readGitignore(projectDir)).toBe('.env.xprem\n');
  });

  it('re-adds a pattern that a later negation cancelled', () => {
    const projectDir = makeProject('.env.xprem\n!.env.xprem\n');
    ensureGitIgnored(projectDir, '.env.xprem', 'holds server secrets');
    const lines = readGitignore(projectDir).trim().split('\n');
    expect(lines[lines.length - 1]).toBe('.env.xprem');
  });
});
