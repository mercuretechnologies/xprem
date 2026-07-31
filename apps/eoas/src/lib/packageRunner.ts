import fs from 'fs-extra';
import path from 'path';

const DEFAULT_PACKAGE_RUNNER = 'npx';

const VALID_RUNNER_RE = /^[a-zA-Z0-9._-]+( [a-zA-Z0-9._-]+)*$/;

function assertValidRunner(value: string, source: string): void {
  if (!VALID_RUNNER_RE.test(value)) {
    throw new Error(
      `Invalid package runner "${value}" (from ${source}). Expected a binary name with optional subcommands like npx, bunx or "pnpm exec".`
    );
  }
}

const PACKAGE_MANAGER_RUNNERS: Record<string, string> = {
  bun: 'bunx',
  // pnpm removed the `pnpx` binary in v7. `pnpm exec` runs the project-local
  // Expo CLI with the same local-first semantics as npx and bunx.
  pnpm: 'pnpm exec',
  yarn: 'npx',
  npm: 'npx',
};

/**
 * Resolves the package runner command to use for spawning Expo CLI commands.
 *
 * Priority:
 * 1. Explicit value passed as argument (e.g. from --packageRunner CLI flag)
 * 2. EOAS_PACKAGE_RUNNER environment variable
 * 3. Inferred from packageManager field in package.json
 * 4. Falls back to 'npx'
 *
 * Supported values: npx, bunx, "pnpm exec", or any other package runner
 * binary, optionally followed by subcommand words.
 */
export function resolvePackageRunner(explicit?: string, projectDir?: string): string {
  if (explicit) {
    assertValidRunner(explicit, '--packageRunner flag');
    return explicit;
  }
  if (process.env.EOAS_PACKAGE_RUNNER) {
    assertValidRunner(process.env.EOAS_PACKAGE_RUNNER, 'EOAS_PACKAGE_RUNNER environment variable');
    return process.env.EOAS_PACKAGE_RUNNER;
  }

  if (projectDir) {
    const detected = detectRunnerFromPackageJson(projectDir);
    if (detected) {
      return detected;
    }
  }

  return DEFAULT_PACKAGE_RUNNER;
}

/**
 * Walks up from projectDir to find a package.json with a packageManager field
 * and maps it to the corresponding package runner binary.
 */
function detectRunnerFromPackageJson(startDir: string): string | undefined {
  let dir = path.resolve(startDir);
  const root = path.parse(dir).root;

  while (dir !== root) {
    const pkgPath = path.join(dir, 'package.json');
    try {
      // eslint-disable-next-line node/no-sync
      if (fs.existsSync(pkgPath)) {
        // eslint-disable-next-line node/no-sync
        const pkg = fs.readJsonSync(pkgPath);
        if (pkg.packageManager) {
          const name = pkg.packageManager.split('@')[0];
          return PACKAGE_MANAGER_RUNNERS[name];
        }
      }
    } catch {
      // Ignore read errors, keep walking up
    }
    dir = path.dirname(dir);
  }

  return undefined;
}

/**
 * Splits a resolved package runner into a spawnable command and its leading
 * arguments (e.g. "pnpm exec" -> ['pnpm', ['exec']]).
 */
export function splitPackageRunner(runner: string): [string, string[]] {
  const [command, ...args] = runner.split(' ');
  return [command, args];
}
