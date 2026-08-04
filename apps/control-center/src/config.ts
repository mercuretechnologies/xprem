import Constants from 'expo-constants';
import * as Updates from 'expo-updates';

/** What the update server needs to answer this device, all of it already on hand. */
export type SurfConfig = {
  origin: string;
  appId: string;
  channel: string;
  runtimeVersion: string;
  /**
   * The request headers baked at build time. setUpdateRequestHeadersOverride
   * REPLACES the whole set rather than merging into it, and expo-updates only
   * accepts keys that were declared at build time — so every override has to be
   * rebuilt from this, or the poll loses its channel and its app id.
   */
  requestHeaders: Record<string, string>;
};

export const BRANCH_HEADER = 'xprem-branch';

/** State the manifest already carries, so nothing has to be stored on the side. */
export type LoadedState = {
  /** The branch actually served. Not necessarily the one that was asked for. */
  branch: string | null;
  /** Set when the server refused a branch because its update crashed here. */
  refusedBranch: string | null;
};

function extra(): Record<string, unknown> {
  return ((Updates.manifest as { extra?: Record<string, unknown> } | undefined)?.extra ??
    {}) as Record<string, unknown>;
}

export function readLoadedState(): LoadedState {
  const manifestExtra = extra();
  return {
    branch: typeof manifestExtra.branch === 'string' ? manifestExtra.branch : null,
    refusedBranch:
      typeof manifestExtra.branchSurfingRefused === 'string' &&
      manifestExtra.branchSurfingRefused.length > 0
        ? manifestExtra.branchSurfingRefused
        : null,
  };
}

/**
 * The keys the override cannot be built without. expo-updates only accepts an
 * override whose keys were declared at build time, and the override replaces the
 * whole header set — so a build missing any of these can be sent into a state
 * where every poll is answered 400 and no update can reach it to fix that.
 */
const REQUIRED_BUILD_HEADERS = ['expo-app-id', 'expo-channel-name', BRANCH_HEADER];

function declared(requestHeaders: Record<string, string>, key: string): boolean {
  return typeof requestHeaders[key] === 'string';
}

/**
 * Returns null when this build cannot surf at all: no update config, a runtime too
 * old for the header override, or a build-time header set the override could not
 * be rebuilt from. The component renders nothing in that case rather than offering
 * a switch that would strand the device.
 */
export function readConfig(): SurfConfig | null {
  const updates = Constants.expoConfig?.updates as
    | { url?: string; requestHeaders?: Record<string, string> }
    | undefined;
  if (!updates?.url || typeof Updates.setUpdateRequestHeadersOverride !== 'function') {
    return null;
  }

  const requestHeaders = updates.requestHeaders ?? {};
  // A key whose value came from an unset env var is dropped from the config
  // entirely, so this checks presence rather than truthiness — the empty string
  // is a declaration, undefined is not.
  const missing = REQUIRED_BUILD_HEADERS.filter(key => !declared(requestHeaders, key));
  if (missing.length > 0) {
    // Loud, because the symptom is a panel that never appears and the cause is
    // three lines away in app config.
    console.warn(
      `[xprem] Branch surfing is unavailable: ${missing.join(', ')} ${
        missing.length === 1 ? 'is' : 'are'
      } missing from updates.requestHeaders. A header can only be overridden at ` +
        `runtime if it was declared at build time.`
    );
    return null;
  }

  const appId = requestHeaders['expo-app-id'];
  const channel = Updates.channel;
  const runtimeVersion = Updates.runtimeVersion;
  if (!appId || !channel || !runtimeVersion) {
    return null;
  }

  let origin: string;
  try {
    origin = new URL(updates.url).origin;
  } catch {
    return null;
  }

  return { origin, appId, channel, runtimeVersion, requestHeaders };
}
