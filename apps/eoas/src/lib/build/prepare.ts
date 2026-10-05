import { ExpoConfig } from '@expo/config';
import { parse as parseDotenv } from 'dotenv';
import fs from 'fs-extra';
import path from 'path';

import { mergeEnvironment } from './environment';
import { BuildLog, LogWriter } from './log';
import { BuildPlatform, fetchEnvironment, resolveIdentifier } from './server';
import { CONFIG_FILENAME, readConfig } from '../buildConfig/config';
import { ResourceNameSchema } from '../buildConfig/schema';
import { BuildProfile } from '../buildConfig/types';
import { getExpoAppId, getPrivateExpoConfigAsync, resolveServerUrl } from '../expoConfig';

// Flags shared by every platform's build command.
export interface BuildOptions {
  profile: string;
  channel?: string;
  envFile?: string;
  ignoreEnvCheck?: boolean;
  serverUrl?: string;
  appId?: string;
  output?: string;
  packageRunner?: string;
  verbose?: boolean;
  stream?: boolean;
  remoteCache?: boolean;
}

export type NodeEnv = 'development' | 'production';

// What every platform has resolved before the project is copied and tools run.
export interface BuildInputs {
  project: string;
  options: BuildOptions;
  profile: BuildProfile;
  packageRunner: [command: string, args: string[]];
  endpoint: string;
  variables: Record<string, string>;
  output: string;
  nodeEnv: NodeEnv;
  // Paths the platform's tool check selected (JAVA_HOME, ANDROID_HOME, ...).
  toolEnv: Record<string, string>;
  // Versions the tool check found (xcode, cocoapods, java).
  toolVersions: Record<string, string>;
  // Environment for every tool the build spawns.
  env: NodeJS.ProcessEnv;
}

export async function selectProfile(project: string, options: BuildOptions): Promise<BuildProfile> {
  const config = await readConfig(path.join(project, CONFIG_FILENAME));
  if (!Object.prototype.hasOwnProperty.call(config.profiles, options.profile)) {
    throw new Error('Unknown build profile.');
  }
  const profile = config.profiles[options.profile];
  if (options.channel === undefined) {
    return profile;
  }
  if (ResourceNameSchema.validate(options.channel).error) {
    throw new Error('Invalid --channel.');
  }
  // --channel replaces whichever channel or environment the profile selects.
  return { ...profile, channel: options.channel, environment: undefined };
}

export function platformProfile<P extends BuildPlatform>(
  profile: BuildProfile,
  platform: P
): NonNullable<BuildProfile[P]> {
  const section = profile[platform];
  if (!section) {
    throw new Error(`This build profile has no ${platform} section in ${CONFIG_FILENAME}.`);
  }
  return section;
}

export async function readEnvFile(
  project: string,
  envFile?: string
): Promise<Record<string, string>> {
  return envFile ? parseDotenv(await fs.readFile(path.resolve(project, envFile))) : {};
}

export function nodeEnvFor(mode: 'debug' | 'release'): NodeEnv {
  return mode === 'debug' ? 'development' : 'production';
}

// Environment for evaluating app.config.* in Node.
export function configEnvironment(
  variables: Record<string, string>,
  toolEnv: Record<string, string>,
  nodeEnv: NodeEnv
): Record<string, string> {
  return { ...variables, ...toolEnv, NODE_ENV: nodeEnv, CI: '1' };
}

export function spawnEnvironment(
  variables: Record<string, string>,
  toolEnv: Record<string, string>,
  nodeEnv: NodeEnv
): NodeJS.ProcessEnv {
  return { ...process.env, ...configEnvironment(variables, toolEnv, nodeEnv), EXPO_NO_DOTENV: '1' };
}

// Bootstrap needs only the server origin and app scope. Explicit flags allow
// config files that cannot evaluate until server variables are available.
export async function resolveBuildEndpoint(
  project: string,
  options: BuildOptions,
  platform: BuildPlatform,
  applicationId: string,
  env: Record<string, string>
): Promise<string> {
  const bootstrap =
    options.serverUrl && options.appId
      ? undefined
      : await getPrivateExpoConfigAsync(project, { env, packageRunner: options.packageRunner });
  const server = await resolveServerUrl(bootstrap ?? ({} as ExpoConfig), options.serverUrl);
  const appId = options.appId ?? (bootstrap && getExpoAppId(bootstrap));
  if (typeof appId !== 'string' || !appId) {
    throw new Error('Set expo-app-id in updates.requestHeaders or pass --appId.');
  }
  const root = `${server}/${encodeURIComponent(appId)}/build`;
  return await resolveIdentifier(root, platform, applicationId);
}

export async function fetchBuildEnvironment(
  endpoint: string,
  profile: BuildProfile,
  local: Record<string, string>,
  stepLog: LogWriter
): Promise<Record<string, string>> {
  let remote: Record<string, string> = {};
  if (profile.channel) {
    remote = await fetchEnvironment(endpoint, { channel: profile.channel });
    stepLog.info(`Server environment keys: ${keyList(remote)}`);
  } else if (profile.environment) {
    remote = await fetchEnvironment(endpoint, { environment: profile.environment });
    stepLog.info(`Server environment keys: ${keyList(remote)}`);
  } else {
    stepLog.info('No channel/environment selected; no server environment fetched.');
  }
  const variables = mergeEnvironment(remote, local, profile.channel);
  stepLog.info(`Build environment keys: ${keyList(variables)}`);
  stepLog.info(
    'Expo embeds EXPO_PUBLIC_* in JavaScript; other variables are available to app config/tooling, not automatically embedded.'
  );
  return variables;
}

function keyList(record: Record<string, string>): string {
  return Object.keys(record).sort().join(', ') || '(none)';
}

export async function resolveOutputPath(
  project: string,
  options: BuildOptions,
  extension: string,
  buildLog: BuildLog
): Promise<string> {
  const output = path.resolve(
    project,
    options.output ?? `build-artifacts/${path.basename(buildLog.path, '.log')}.${extension}`
  );
  if (await fs.pathExists(output)) {
    throw new Error('Output already exists; choose another --output.');
  }
  return output;
}
