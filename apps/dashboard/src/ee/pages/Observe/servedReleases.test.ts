// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { UpdateFeedPage, UpdateFeedQuery, UpdateFeedRecord } from '@/lib/api';
import { readServingHeads, servedReleases, servingBranches } from './servedReleases';

const head = (overrides: Partial<UpdateFeedRecord> = {}): UpdateFeedRecord => ({
  branch: 'production',
  runtimeVersion: '1',
  platform: 'ios',
  updateId: '1',
  updateUUID: '11111111-1111-4111-8111-111111111111',
  createdAt: '2026-10-01T10:00:00Z',
  commitHash: '',
  message: 'iOS runtime 1',
  ...overrides,
});

test('runtime and platform filters select eligible heads instead of the newest branch publish', () => {
  const older = head();
  const newer = head({
    runtimeVersion: '2',
    platform: 'android',
    updateId: '2',
    updateUUID: '22222222-2222-4222-8222-222222222222',
    createdAt: '2026-10-03T10:00:00Z',
  });
  const picked = servedReleases([newer, older], ['1'], ['ios']);
  assert.equal(picked.length, 1);
  assert.equal(picked[0].record.updateUUID, older.updateUUID);
  assert.deepEqual(picked[0].group?.updateUUIDs, [older.updateUUID]);
  assert.equal(servedReleases([newer, older], ['1', '2'], ['ios', 'android']).length, 2);
  assert.equal(servedReleases([newer, older], ['missing']).length, 0);
});

test('branch selection reads only eligible primary and rollout branches, including plural selections', () => {
  const channels = [
    { branchName: 'main', rollout: { rolloutBranchName: 'next' } },
    { branchName: 'main' },
    { branchName: 'staging' },
  ];
  assert.deepEqual(servingBranches(channels), ['main', 'next', 'staging']);
  assert.deepEqual(servingBranches(channels, ['main']), ['main']);
  assert.deepEqual(servingBranches(channels, ['next']), ['next']);
  assert.deepEqual(servingBranches(channels, ['main', 'next']), ['main', 'next']);
  assert.deepEqual(servingBranches(channels, ['missing']), []);
  const primary = head({ branch: 'main' });
  const rollout = head({
    branch: 'next',
    updateId: '2',
    updateUUID: '22222222-2222-4222-8222-222222222222',
  });
  assert.deepEqual(
    servedReleases([primary, rollout], [], [], ['next']).map(entry => entry.record.branch),
    ['next']
  );
  assert.equal(servedReleases([primary, rollout], [], [], ['main', 'next']).length, 2);
});

test('current platform heads group only the OTA platforms still serving that publish', () => {
  const ios = head({ publishGroup: 'group' });
  const android = head({
    publishGroup: 'group',
    platform: 'android',
    updateId: '2',
    updateUUID: '22222222-2222-4222-8222-222222222222',
  });
  const all = servedReleases([ios, android]);
  assert.equal(all.length, 1);
  assert.deepEqual(all[0].group?.updateUUIDs, [ios.updateUUID, android.updateUUID]);
  const filtered = servedReleases([ios, android], [], ['ios']);
  assert.deepEqual(filtered[0].group?.platforms, ['ios']);
});

test('embedded rollback heads remain visible and supply no invalid health UUID', () => {
  const rollback = head({
    updateId: '3',
    updateUUID: 'Rollback to embedded',
    createdAt: '2026-10-04T10:00:00Z',
    message: 'Rollback iOS',
  });
  const android = head({
    platform: 'android',
    updateId: '2',
    updateUUID: '22222222-2222-4222-8222-222222222222',
  });
  const served = servedReleases([rollback, android]);
  assert.equal(served.length, 2);
  assert.equal(served.find(entry => entry.record.updateId === rollback.updateId)?.group, null);
  assert.deepEqual(
    served.flatMap(entry => entry.group?.updateUUIDs ?? []),
    [android.updateUUID]
  );
});

test('an active rollout preserves both candidate and control rows of the same runtime and platform', () => {
  const candidate = head({
    updateId: '4',
    rolloutPercentage: 10,
    controlUpdateId: '3',
    createdAt: '2026-10-04T10:00:00Z',
  });
  const control = head({ updateId: '3', updateUUID: 'Rollback to embedded' });
  const served = servedReleases([candidate, control], ['1'], ['ios']);
  assert.equal(served.length, 2);
  assert.equal(served[0].group?.rolloutPercentage, 10);
  assert.equal(served[1].group, null);
  assert.deepEqual(
    served.map(release => release.record.updateId),
    ['4', '3']
  );
});

test('all branch head pages are read with latestOnly before local runtime filtering', async () => {
  const recent = Array.from({ length: 100 }, (_, index) =>
    head({
      runtimeVersion: `new-${index}`,
      updateId: String(index + 10),
    })
  );
  const older = head({ runtimeVersion: 'old' });
  const requests: UpdateFeedQuery[] = [];
  const pages: UpdateFeedPage[] = [
    { items: recent, nextCursor: 'older-heads' },
    { items: [older] },
  ];
  const loaded = await readServingHeads('production', async query => {
    requests.push(query);
    return pages[requests.length - 1];
  });
  assert.deepEqual(requests, [
    { branch: 'production', latestOnly: true, limit: 100 },
    { branch: 'production', latestOnly: true, limit: 100, cursor: 'older-heads' },
  ]);
  assert.equal(loaded.length, 101);
  assert.equal(servedReleases(loaded, ['old'])[0].record.runtimeVersion, 'old');
});

test('a failed continuation rejects the complete serving read instead of presenting partial heads', async () => {
  let reads = 0;
  await assert.rejects(
    readServingHeads('production', async () => {
      if (reads++ === 0) return { items: [head()], nextCursor: 'older-heads' };
      throw new Error('Feed unavailable');
    }),
    /Feed unavailable/
  );
});
