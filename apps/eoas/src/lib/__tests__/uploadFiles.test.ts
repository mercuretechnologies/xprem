/* eslint-disable node/no-sync */
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, describe, expect, it } from 'vitest';

import { buildUploadFiles, computeFilesRequests } from '../assets';
import { digestFile } from '../crypto';
import { RequestedPlatform } from '../expoConfig';

let projectDir: string;

afterEach(() => {
  if (projectDir) {
    fs.removeSync(projectDir);
  }
});

// The SDK 50+ export layout: the bundle lives under _expo/static/js/<platform>,
// not under bundles/. The roles are what carry the split, so nothing has to
// infer a platform from a file name.
function writeExport(): string {
  projectDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-upload-'));
  const dist = path.join(projectDir, 'dist');
  const write = (relativePath: string, content: string): void => {
    const absolutePath = path.join(dist, relativePath);
    fs.mkdirpSync(path.dirname(absolutePath));
    fs.writeFileSync(absolutePath, content);
  };
  write('_expo/static/js/ios/AppEntry-ios.hbc', 'ios bundle');
  write('_expo/static/js/android/AppEntry-android.hbc', 'android bundle');
  write('assets/shared', 'shared asset');
  write('assets/ios-only', 'ios asset');
  write('assets/android-only', 'android asset');
  write('expoConfig.json', '{}');
  write(
    'metadata.json',
    JSON.stringify({
      version: 0,
      bundler: 'metro',
      fileMetadata: {
        ios: {
          bundle: '_expo/static/js/ios/AppEntry-ios.hbc',
          assets: [
            { path: 'assets/shared', ext: 'png' },
            { path: 'assets/ios-only', ext: 'png' },
          ],
        },
        android: {
          bundle: '_expo/static/js/android/AppEntry-android.hbc',
          assets: [
            { path: 'assets/shared', ext: 'png' },
            { path: 'assets/android-only', ext: 'png' },
          ],
        },
      },
    })
  );
  return dist;
}

describe('buildUploadFiles', () => {
  it('sends one platform, with the roles the server reads', async () => {
    const dist = writeExport();
    const all = await computeFilesRequests(projectDir, 'dist', RequestedPlatform.All);
    const ios = buildUploadFiles(all, 'ios');

    const launch = ios.filter(file => file.role === 'launch');
    expect(launch).toHaveLength(1);
    expect(launch[0].path).toBe('_expo/static/js/ios/AppEntry-ios.hbc');
    expect(launch[0]).toMatchObject(await digestFile(path.join(dist, launch[0].path)));

    expect(ios.filter(file => file.role === 'asset').map(file => file.path)).toEqual([
      'assets/shared',
      'assets/ios-only',
    ]);
    expect(ios.filter(file => file.role === 'config').map(file => file.path)).toEqual([
      'metadata.json',
      'expoConfig.json',
    ]);

    // Nothing belonging to the other platform travels on an ios publish.
    expect(ios.map(file => file.path)).not.toContain('assets/android-only');
    expect(ios.map(file => file.path)).not.toContain(
      '_expo/static/js/android/AppEntry-android.hbc'
    );
  });

  it('carries the manifest key on assets and omits it on config files', async () => {
    writeExport();
    const all = await computeFilesRequests(projectDir, 'dist', RequestedPlatform.All);

    for (const file of buildUploadFiles(all, 'android')) {
      if (file.role === 'config') {
        expect(file.key).toBeUndefined();
        expect(file.ext).toBeUndefined();
      } else {
        expect(file.key).toMatch(/^[0-9a-f]{32}$/);
        expect(file.ext).toBeTruthy();
      }
    }
  });

  it('claims no launch asset for a platform the export does not carry', async () => {
    writeExport();
    const iosOnly = await computeFilesRequests(projectDir, 'dist', RequestedPlatform.Ios);
    const android = buildUploadFiles(iosOnly, 'android');

    expect(android.every(file => file.role === 'config')).toBe(true);
  });
});
