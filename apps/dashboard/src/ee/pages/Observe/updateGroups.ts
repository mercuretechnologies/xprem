import type { UpdateFeedRecord, UpdateHealthRecord } from '@/lib/api';
import { uuidPattern, type FilterKey } from './filters';

// A publish sends one update per platform, so the thing a human calls "my
// updateGroup" is the publish group, not the update row.
export type UpdateGroup = {
  key: string;
  publishGroup: string | null;
  updateUUIDs: string[];
  platforms: string[];
  createdAt: Date;
  branch: string;
  runtimeVersion: string;
  message: string;
  shortId: string;
  rolloutPercentage: number | null;
  healthRelevant: boolean;
};

// A publish message carries a whole commit body. The subject line is what
// identifies the change; the rest belongs in git, not in a chart legend.
export const subjectLine = (message: string) => message.split('\n')[0]?.trim() ?? '';

// What to call a publish. Eight hex characters identify it but say nothing
// about it, so the message the person who shipped it wrote comes first, and
// the short id falls back for a publish that carried no --message.
export const groupTitle = (group: UpdateGroup) => subjectLine(group.message) || group.shortId;

// The line under the title. The short id stays visible whenever the message
// took its place, because that is the value people paste into a filter.
export const groupContext = (
  group: UpdateGroup,
  formatDate: (date: Date) => string,
  // The branch drops out wherever it already heads the section: repeating it
  // on every line of a section named after it says nothing.
  { branch = true }: { branch?: boolean } = {}
) =>
  [
    subjectLine(group.message) ? group.shortId : null,
    branch ? group.branch : null,
    `runtime ${group.runtimeVersion}`,
    formatDate(group.createdAt),
  ]
    .filter(Boolean)
    .join(' · ');

export type UpdateGroupHealth = {
  devices: number;
  successful: number;
  faulty: number;
  // Devices that tried to run it, crashed or not. The honest denominator for
  // a failure rate: nothing here counts a device that never received it.
  attempts: number;
  failureRate: number | null;
};

export const platformLabel = (platforms: string[]) =>
  platforms.length === 0
    ? 'all platforms'
    : platforms
        .map(platform =>
          platform === 'ios' ? 'iOS' : platform === 'android' ? 'Android' : platform
        )
        .join(' and ');

export const buildUpdateGroups = (items: UpdateFeedRecord[]): UpdateGroup[] => {
  const byKey = new Map<string, UpdateGroup>();
  for (const item of items) {
    // "Rollback to embedded" rows carry a literal where a UUID belongs, and
    // the health endpoint rejects anything that is not one.
    if (!uuidPattern.test(item.updateUUID)) continue;
    const key = item.publishGroup || item.updateUUID;
    const createdAt = new Date(item.createdAt);
    const existing = byKey.get(key);
    if (!existing) {
      byKey.set(key, {
        key,
        publishGroup: item.publishGroup || null,
        updateUUIDs: [item.updateUUID],
        platforms: item.platform ? [item.platform] : [],
        createdAt,
        branch: item.branch,
        runtimeVersion: item.runtimeVersion,
        message: item.message ?? '',
        shortId: (item.publishGroup || item.updateUUID).slice(0, 8),
        rolloutPercentage: item.rolloutPercentage ?? null,
        healthRelevant: item.healthRelevant,
      });
      continue;
    }
    existing.updateUUIDs.push(item.updateUUID);
    if (item.platform && !existing.platforms.includes(item.platform)) {
      existing.platforms.push(item.platform);
    }
    if (createdAt > existing.createdAt) existing.createdAt = createdAt;
    if (!existing.message && item.message) existing.message = item.message;
    if (item.rolloutPercentage != null) existing.rolloutPercentage = item.rolloutPercentage;
    existing.healthRelevant = existing.healthRelevant || item.healthRelevant;
  }
  return Array.from(byKey.values()).sort(
    (left, right) => right.createdAt.getTime() - left.createdAt.getTime()
  );
};

export const aggregateHealth = (
  updateGroup: UpdateGroup,
  records: Record<string, UpdateHealthRecord> | undefined
): UpdateGroupHealth => {
  const totals = updateGroup.updateUUIDs.reduce(
    (summary, id) => {
      const record = records?.[id];
      if (!record) return summary;
      summary.devices += record.devicesOnUpdate;
      summary.successful += record.successfulDevices;
      summary.faulty += record.faultyDevices;
      return summary;
    },
    { devices: 0, successful: 0, faulty: 0 }
  );
  const attempts = totals.successful + totals.faulty;
  return {
    ...totals,
    attempts,
    failureRate: attempts > 0 ? totals.faulty / attempts : null,
  };
};

// How to select exactly this updateGroup in an API query. A publish covers both
// platforms, so the group is the honest filter; only a updateGroup without one
// falls back to its single update.
export const updateGroupFilter = (updateGroup: UpdateGroup): Partial<Record<FilterKey, string>> =>
  updateGroup.publishGroup
    ? { updateGroupId: updateGroup.publishGroup, updateId: '' }
    : { updateId: updateGroup.updateUUIDs[0], updateGroupId: '' };

// How each update id should read on a chart or in a breakdown row. Two
// publishes can carry the same message (a republish, the same commit shipped
// twice) and a row has space for one line only, so a shared message keeps its
// short id attached and a unique one does not need it. Keyed by publish group
// AND by every update uuid inside it, because a row may name either.
export const titlesByUpdateId = (groups: UpdateGroup[]) => {
  const shared = new Set<string>();
  const seen = new Set<string>();
  for (const group of groups) {
    const title = groupTitle(group);
    if (seen.has(title)) shared.add(title);
    seen.add(title);
  }
  const titles = new Map<string, string>();
  for (const group of groups) {
    const title = groupTitle(group);
    // Leading with the id, not trailing: a row truncates on the right, so a
    // suffix is exactly the part that disappears.
    const name = shared.has(title) ? `${group.shortId} · ${title}` : title;
    if (group.publishGroup) titles.set(group.publishGroup, name);
    for (const updateUUID of group.updateUUIDs) titles.set(updateUUID, name);
  }
  return titles;
};

// The branch behind each update id, and how many publishes each branch carries.
// The second is the order branches appear in: the one everybody is on heads the
// table and canary sits at the bottom.
export const branchesByUpdateId = (groups: UpdateGroup[]) => {
  const branchOfUpdate = new Map<string, string>();
  const branchReach = new Map<string, number>();
  for (const group of groups) {
    if (group.publishGroup) branchOfUpdate.set(group.publishGroup, group.branch);
    for (const updateUUID of group.updateUUIDs) branchOfUpdate.set(updateUUID, group.branch);
    branchReach.set(group.branch, (branchReach.get(group.branch) ?? 0) + 1);
  }
  return { branchOfUpdate, branchReach };
};
