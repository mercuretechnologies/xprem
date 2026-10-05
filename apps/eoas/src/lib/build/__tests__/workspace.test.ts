import fs from 'fs-extra';
import { createRequire } from 'module';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { BuildLog } from '../log';
import { terminateBuildCommand } from '../run';
import { installMetroCheck, withTemporaryDirectory } from '../workspace';

// The transformer loads the compiled checker from dist/, so `npm run build` must have run.
describe('Metro environment check', () => {
  let project: string;
  let temporary: string;
  beforeEach(async () => {
    project = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-metro-project-'));
    temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-metro-temporary-'));
  });
  afterEach(async () => {
    await fs.remove(project);
    await fs.remove(temporary);
  });

  it.each([false, true])(
    'wraps the project transformer and reports unknown variables (existing config: %s)',
    async existingConfig => {
      const upstream = path.join(project, 'upstream.cjs');
      await fs.writeFile(
        upstream,
        'module.exports = { transform: async args => args, getCacheKey: () => "upstream" };'
      );
      const config = { transformer: { babelTransformerPath: upstream }, customOption: 'retained' };
      if (existingConfig) {
        await fs.writeFile(
          path.join(project, 'metro.config.cjs'),
          `module.exports = Promise.resolve(${JSON.stringify(config)});`
        );
        await fs.outputFile(
          path.join(project, 'node_modules/metro-config/index.js'),
          'exports.loadConfig = async ({config}) => require(config);'
        );
      } else {
        await fs.outputFile(
          path.join(project, 'node_modules/expo/metro-config.js'),
          `exports.getDefaultConfig = () => (${JSON.stringify(config)});`
        );
      }

      const report = await installMetroCheck(project, temporary, ['EXPO_PUBLIC_NAV']);

      const nodeRequire = createRequire(path.join(project, 'package.json'));
      const metro = await nodeRequire('./metro.config.cjs');
      expect(metro.customOption).toBe('retained');
      expect(metro.resetCache).toBe(true);
      const transformer = nodeRequire(metro.transformer.babelTransformerPath);
      expect(transformer.getCacheKey()).toBe('upstream');

      const known = {
        src: 'process.env.EXPO_PUBLIC_NAV',
        filename: path.join(project, 'index.ts'),
      };
      expect(await transformer.transform(known)).toEqual(known);
      await expect(
        transformer.transform({ ...known, src: 'process.env.UNKNOWN_KEY' })
      ).rejects.toThrow('EOAS environment check failed');
      expect(await fs.readFile(report, 'utf8')).toContain('UNKNOWN_KEY');

      const dependency = {
        src: 'process.env.UNKNOWN_KEY',
        filename: path.join(project, 'node_modules/dependency/index.js'),
      };
      expect(await transformer.transform(dependency)).toEqual(dependency);
    }
  );
});

vi.mock('../run', () => ({
  runBuildCommand: vi.fn(),
  terminateBuildCommand: vi.fn().mockResolvedValue(undefined),
}));

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void;
  const promise = new Promise<void>(done => {
    resolve = done;
  });
  return { promise, resolve };
}

it.each([false, true])(
  'closes logs and releases the workspace only once on interruption (termination fails: %s)',
  async terminationFails => {
    if (terminationFails) {
      vi.mocked(terminateBuildCommand).mockRejectedValueOnce(new Error('Unable to stop process'));
    }
    const parent = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-workspace-test-'));
    vi.spyOn(os, 'tmpdir').mockReturnValue(parent);
    const exit = vi.spyOn(process, 'exit').mockImplementation(() => undefined as never);
    const ready = deferred();
    const finishWork = deferred();
    const finishClose = deferred();
    const nextReady = deferred();
    const finishNext = deferred();
    const log = {
      abort: vi.fn(),
      general: { write: vi.fn(), warn: vi.fn() },
      close: vi.fn(() => finishClose.promise),
    } as unknown as BuildLog;
    const listeners = process.listeners('SIGINT');
    const project = '/projects/cancelled-build';
    const build = withTemporaryDirectory(
      log,
      async () => {
        ready.resolve();
        await finishWork.promise;
      },
      project
    );
    let nextBuild: Promise<void> | undefined;
    try {
      // Pause interruption while logs close, then let normal completion release the workspace.
      await ready.promise;
      const interrupt = process
        .listeners('SIGINT')
        .find(listener => !listeners.includes(listener))!;
      interrupt('SIGINT');
      await expect.poll(() => vi.mocked(log.close).mock.calls.length).toBe(1);
      expect(log.general.warn).toHaveBeenCalledTimes(terminationFails ? 1 : 0);
      finishWork.resolve();
      await build;

      // Another build now owns the same directory and lock.
      let marker = '';
      nextBuild = withTemporaryDirectory(
        log,
        async directory => {
          marker = path.join(directory, 'new-build');
          await fs.writeFile(marker, 'in progress');
          nextReady.resolve();
          await finishNext.promise;
        },
        project
      );
      await nextReady.promise;
      // The delayed interrupt must reuse its first cleanup, not delete the new workspace.
      finishClose.resolve();
      await expect.poll(() => exit.mock.calls.length).toBe(1);

      expect(await fs.readFile(marker, 'utf8')).toBe('in progress');
      await expect(withTemporaryDirectory(log, async () => {}, project)).rejects.toThrow(
        'already running'
      );
    } finally {
      finishWork.resolve();
      finishClose.resolve();
      finishNext.resolve();
      await build;
      await nextBuild;
      vi.restoreAllMocks();
      await fs.remove(parent);
    }
  }
);
