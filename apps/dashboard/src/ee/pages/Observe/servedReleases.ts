// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { UpdateFeedPage, UpdateFeedQuery, UpdateFeedRecord } from '@/lib/api';
import { buildUpdateGroups, type UpdateGroup } from './updateGroups';

export type ServedRelease = { key: string; group: UpdateGroup | null; record: UpdateFeedRecord };

export const servingBranches = (
  channels: Array<{ branchName?: string | null; rollout?: { rolloutBranchName: string } | null }>,
  selected: string[] = []
) =>
  Array.from(
    new Set(
      channels.flatMap(channel => [
        channel.branchName ?? '',
        channel.rollout?.rolloutBranchName ?? '',
      ])
    )
  ).filter(branch => branch && (selected.length === 0 || selected.includes(branch)));

// The server picks runtime/platform heads and active rollout controls before paging.
export const readServingHeads = async (
  branch: string,
  readPage: (query: UpdateFeedQuery) => Promise<UpdateFeedPage>
) => {
  const heads: UpdateFeedRecord[] = [];
  let cursor: string | undefined;
  do {
    const page = await readPage({
      branch,
      latestOnly: true,
      limit: 100,
      ...(cursor ? { cursor } : {}),
    });
    heads.push(...page.items);
    cursor = page.nextCursor;
  } while (cursor);
  return heads;
};

export const servedReleases = (
  heads: UpdateFeedRecord[],
  runtimes: string[] = [],
  platforms: string[] = [],
  branches: string[] = []
): ServedRelease[] => {
  const eligible = heads.filter(
    head =>
      (runtimes.length === 0 || runtimes.includes(head.runtimeVersion)) &&
      (platforms.length === 0 || platforms.includes(head.platform)) &&
      (branches.length === 0 || branches.includes(head.branch))
  );
  const ota = buildUpdateGroups(eligible).map(group => ({
    key: group.key,
    group,
    record: eligible.find(head => head.updateUUID === group.updateUUIDs[0])!,
  }));
  const embedded = eligible
    .filter(head => head.updateUUID === 'Rollback to embedded')
    .map(record => ({
      key: `${record.branch}:${record.runtimeVersion}:${record.platform}:${record.updateId}`,
      group: null,
      record,
    }));
  return [...ota, ...embedded].sort(
    (left, right) => Date.parse(right.record.createdAt) - Date.parse(left.record.createdAt)
  );
};

// The runtime of the newest head is the one the channel ships today.
export const currentRuntime = (releases: ServedRelease[]) =>
  releases
    .slice()
    .sort(
      (left, right) => Date.parse(right.record.createdAt) - Date.parse(left.record.createdAt)
    )[0]?.record.runtimeVersion;

export const onRuntime = (releases: ServedRelease[], runtime: string | undefined) =>
  releases.filter(release => release.record.runtimeVersion === runtime);
