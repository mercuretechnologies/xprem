import fs from 'fs-extra';
import path from 'path';

import Log from './log';

export function isValidUpdateUrl(updateUrl: string): boolean {
  try {
    const parsed = new URL(updateUrl);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return false;
    }
    if (!parsed.host) {
      return false;
    }
    // A query or fragment would corrupt the `${url}/manifest` concatenation.
    if (parsed.search || parsed.hash) {
      return false;
    }
    return !parsed.pathname.replace(/\/+$/, '').endsWith('/manifest');
  } catch {
    return false;
  }
}

// Appends pattern to the project .gitignore unless an identical rule is already
// in force. Only an exact existing rule counts: comments, negated entries and
// rules that differ in path do not guarantee the same protection. Appending at
// the end also wins over an earlier negated entry, since the last matching
// gitignore rule prevails. A pattern without a slash matches at every directory
// level; one with a slash is relative to the .gitignore.
export function ensureGitIgnored(projectDir: string, pattern: string, reason: string): void {
  const gitignorePath = path.join(projectDir, '.gitignore');
  try {
    // eslint-disable-next-line node/no-sync
    const gitignore = fs.existsSync(gitignorePath) ? fs.readFileSync(gitignorePath, 'utf8') : '';
    const lines = gitignore.split(/\r?\n/).map(line => line.trim());
    const lastRule = lines.lastIndexOf(pattern);
    const lastNegation = lines.lastIndexOf(`!${pattern}`);
    if (lastRule !== -1 && lastRule > lastNegation) {
      return;
    }
    const separator = gitignore === '' ? '' : gitignore.endsWith('\n') ? '\n' : '\n\n';
    // eslint-disable-next-line node/no-sync
    fs.appendFileSync(gitignorePath, `${separator}# ${reason}\n${pattern}\n`);
    Log.succeed(`Added ${pattern} to .gitignore`);
  } catch {
    Log.warn(
      `Could not update .gitignore. Make sure ${pattern} is never committed to your repository.`
    );
  }
}

export function ensurePrivateKeyIgnored(projectDir: string): void {
  ensureGitIgnored(
    projectDir,
    'private-key.pem',
    'Code signing private key (server-side secret, never commit it)'
  );
}
