import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { failBuildRecord, startBuildRecord, uploadBuildArtifact } from '../artifacts';
import { BuildLog, StepLogger } from '../log';
import { NativeBuild, runNativeBuild } from '../native';
import { BuildInputs } from '../prepare';
import { runBuildCommand } from '../run';
import { copyProject } from '../workspace';

vi.mock('../artifacts', () => ({
  startBuildRecord: vi.fn(),
  finishBuildRecord: vi.fn(),
  failBuildRecord: vi.fn(),
  uploadBuildArtifact: vi.fn(),
}));
vi.mock('../fingerprint', () => ({ fingerprintBuild: vi.fn() }));
vi.mock('../run', () => ({ runBuildCommand: vi.fn() }));
vi.mock('../server', () => ({ allocateBuildNumber: vi.fn().mockResolvedValue('1') }));
vi.mock('../workspace', () => ({
  copyProject: vi.fn(),
  evaluateExpoConfig: vi.fn().mockResolvedValue({ name: 'app', slug: 'app', version: '1.0.0' }),
  expoCommand: vi.fn(),
  installMetroCheck: vi.fn(),
  restoreMetroConfig: vi.fn(),
  validateBundle: vi.fn(),
  writeAppJson: vi.fn(),
}));
vi.mock('../../log', () => ({ default: { succeed: vi.fn() } }));

let directory: string;
let build: BuildInputs;
let native: NativeBuild;
let events: string[];
const step: StepLogger = {
  write: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  markSkipped: vi.fn(),
};
const buildLog: BuildLog = {
  path: 'build.log',
  general: step,
  maskSecrets: vi.fn(),
  streamTo: vi.fn(),
  runStep: (_name, work) => work(step),
  abort: vi.fn(),
  close: vi.fn().mockResolvedValue(undefined),
};

beforeEach(async () => {
  vi.clearAllMocks();
  directory = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-native-'));
  const working = path.join(directory, 'project');
  await fs.ensureDir(working);
  await fs.writeFile(path.join(working, 'app.apk'), 'signed application');
  events = [];
  build = {
    project: working,
    options: { profile: 'test', ignoreEnvCheck: true },
    profile: {},
    packageRunner: ['npx', []],
    endpoint: 'https://example.test/build',
    variables: {},
    output: path.join(directory, 'output', 'app.apk'),
    nodeEnv: 'production',
    toolEnv: {},
    toolVersions: {},
    env: {},
  };
  vi.mocked(copyProject).mockResolvedValue(working);
  vi.mocked(startBuildRecord).mockResolvedValue({
    schemaVersion: 1,
    id: 'build-id',
    endpoint: build.endpoint,
    artifactType: 'apk',
    metadata: { profile: 'test', mode: 'release', cliVersion: '1', startedAt: '2026-09-19' },
  });
  vi.mocked(failBuildRecord).mockResolvedValue(undefined);
  vi.mocked(runBuildCommand).mockImplementation(async () => {
    events.push('prebuild');
  });
  vi.mocked(uploadBuildArtifact).mockImplementation(async () => {
    events.push('upload');
    return 'https://example.test/artifacts/app.apk';
  });
  native = {
    platform: 'android',
    displayName: 'Android',
    artifactType: 'apk',
    mode: 'release',
    buildNumberName: 'versionCode',
    maintainedProjectNotice: 'Using Android project',
    withIdentity: expo => expo,
    restoreCache: vi.fn(async () => {
      events.push('restore');
    }),
    compile: vi.fn(async () => {
      events.push('compile');
    }),
    findArtifact: async workspace => path.join(workspace.working, 'app.apk'),
    saveCache: vi.fn(async () => {
      events.push('save');
    }),
  };
});

afterEach(async () => {
  await fs.remove(directory);
});

it('restores after prebuild and saves only after the artifact upload completes', async () => {
  await expect(runNativeBuild(build, native, directory, buildLog, [])).resolves.toBe(build.output);
  expect(events).toEqual(['prebuild', 'restore', 'compile', 'upload', 'save']);
  expect(native.saveCache).toHaveBeenCalledWith(vi.mocked(native.compile).mock.calls[0][0]);
});

it.each(['compile', 'upload'] as const)('does not save cache when %s fails', async failure => {
  const reject = async (): Promise<never> => {
    throw new Error(`${failure} failed`);
  };
  if (failure === 'compile') {
    vi.mocked(native.compile).mockImplementation(reject);
  } else {
    vi.mocked(uploadBuildArtifact).mockImplementation(reject);
  }
  await expect(runNativeBuild(build, native, directory, buildLog, [])).rejects.toThrow(
    `${failure} failed`
  );
  expect(native.saveCache).not.toHaveBeenCalled();
  expect(failBuildRecord).toHaveBeenCalledTimes(1);
});

it('builds platforms without cache hooks', async () => {
  native.platform = 'ios';
  delete native.restoreCache;
  delete native.saveCache;
  await expect(runNativeBuild(build, native, directory, buildLog, [])).resolves.toBe(build.output);
  expect(events).toEqual(['prebuild', 'compile', 'upload']);
});
