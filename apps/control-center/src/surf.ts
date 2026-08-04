import * as Updates from 'expo-updates';
import { Platform } from 'react-native';
import { BRANCH_HEADER, SurfConfig } from './config';

export type SurfableBranch = {
  name: string;
  lastUpdateAt: string;
};

/** A page of branches, plus how many matched in all, so the panel can offer the rest. */
export type BranchPage = {
  branches: SurfableBranch[];
  total: number;
};

/**
 * Asks the server which branches this build may be served. Returns null when the
 * channel does not allow surfing at all — distinct from an empty page, which
 * means it does but has nothing else built for this runtime version. The panel
 * hides itself on null and shows an empty state on [].
 *
 * The default answer is the newest 50; pass all to ask for the rest, which the
 * panel only does when a tester taps for it.
 */
export async function listBranches(
  config: SurfConfig,
  signal?: AbortSignal,
  all = false
): Promise<BranchPage | null> {
  const response = await fetch(`${config.origin}/branch_lists${all ? '?all=1' : ''}`, {
    method: 'GET',
    headers: {
      'expo-app-id': config.appId,
      'expo-channel-name': config.channel,
      'expo-runtime-version': config.runtimeVersion,
      // The list is per platform, like the manifest: a branch whose only update
      // is for the other one cannot be served here, so it must not be offered.
      'expo-platform': Platform.OS,
    },
    signal,
  });
  if (response.status === 404) {
    Updates.setUpdateRequestHeadersOverride(null);
    return null;
  }
  if (!response.ok) {
    throw new Error(`Could not reach the update server (${response.status}).`);
  }
  const page = (await response.json()) as BranchPage;
  // The build carrying this code outlives the server it was written against, so
  // a body of another shape hides the panel rather than crashing the host app.
  if (!page || !Array.isArray(page.branches) || typeof page.total !== 'number') {
    return null;
  }
  return page;
}

/**
 * Rebuilds the WHOLE header set with one value changed: the override replaces
 * the native set and persists across relaunches.
 *
 * The channel and app id are pinned from the running values rather than trusted
 * from Constants.expoConfig: that copy is evaluated when the JS bundle is
 * exported, and a config spelled `process.env.RELEASE_CHANNEL` evaluates to
 * undefined in any bundle exported without the variable. Spreading it then
 * strips the channel out of every future poll — observed live as /manifest
 * answering 400 "No channel name provided" until the override is cleared.
 */
function applyBranchHeader(config: SurfConfig, branch: string) {
  const headers: Record<string, string> = {};
  for (const [key, value] of Object.entries(config.requestHeaders)) {
    if (typeof value === 'string' && value !== '') {
      headers[key] = value;
    }
  }
  headers['expo-channel-name'] = config.channel;
  headers['expo-app-id'] = config.appId;
  headers[BRANCH_HEADER] = branch;
  Updates.setUpdateRequestHeadersOverride(headers);
}

export type SurfOutcome = 'reloading' | 'nothing-to-load';

/**
 * Points this device at a branch and reloads onto it. Returns without reloading
 * when the server has nothing new: the caller says so rather than leaving the
 * tester looking at an unchanged screen.
 */
export async function surfTo(config: SurfConfig, branch: string | null): Promise<SurfOutcome> {
  if (branch) {
    applyBranchHeader(config, branch);
  } else {
    // Returning to the build's own branch is not "override with an empty value",
    // it is no override at all: null removes it and the native side reverts to
    // the headers baked at build time — the one state that can never be wrong.
    Updates.setUpdateRequestHeadersOverride(null);
  }
  const { isAvailable } = await Updates.checkForUpdateAsync();
  if (!isAvailable) {
    return 'nothing-to-load';
  }
  await Updates.fetchUpdateAsync();
  await Updates.reloadAsync();
  return 'reloading';
}
