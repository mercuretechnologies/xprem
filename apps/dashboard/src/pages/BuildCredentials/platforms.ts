import { AppIdentifier } from '@/lib/api';

export type Platform = AppIdentifier['platform'];

export type PlatformSection = {
  platform: Platform;
  label: string;
  enabled: boolean;
  disabledNote?: string;
};

// One entry per platform, in display order.
export const PLATFORMS: PlatformSection[] = [
  { platform: 'android', label: 'Android', enabled: true },
  { platform: 'ios', label: 'iOS', enabled: true },
];

/** Returns a known platform display label or preserves an unknown platform value. */
export const platformLabel = (platform: Platform) =>
  PLATFORMS.find(section => section.platform === platform)?.label ?? platform;

// Whether the identifier has everything it needs to sign builds; per platform
/** because each platform stores different credentials. */
export const isCredentialsConfigured = (identifier: AppIdentifier) =>
  identifier.platform === 'android'
    ? identifier.hasAndroidCredentials
    : identifier.hasIosCredentials;
