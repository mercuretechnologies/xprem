import { Env, Platform } from '@expo/eas-build-job';
import { Command, Flags } from '@oclif/core';

import { getAuthHeaders, retrieveCredentials, validateCredentials } from '../lib/auth';
import {
  RequestedPlatform,
  getPrivateExpoConfigAsync,
  requireExpoAppId,
  resolveServerUrl,
} from '../lib/expoConfig';
import { fetchWithRetries } from '../lib/fetch';
import Log from '../lib/log';
import { ora } from '../lib/ora';
import { isExpoInstalled } from '../lib/package';
import { confirmAsync } from '../lib/prompts';
import { resolveRuntimeVersionAsync } from '../lib/runtimeVersion';
import { resolveVcsClient } from '../lib/vcs';
import { resolveWorkflowAsync } from '../lib/workflow';

export default class Publish extends Command {
  static override args = {};
  static override description = 'Publish a new rollback to the self-hosted update server';
  static override examples = ['<%= config.bin %> <%= command.id %>'];
  static override flags = {
    platform: Flags.string({
      type: 'option',
      options: Object.values(RequestedPlatform),
      default: RequestedPlatform.All,
      required: false,
    }),
    branch: Flags.string({
      description: 'Name of the branch to point to',
      required: true,
    }),
    serverUrl: Flags.string({
      description:
        'URL of the self-hosted update server to roll back on. Defaults to updates.url from your Expo config, minus a trailing /manifest',
      required: false,
    }),
    nonInteractive: Flags.boolean({
      description: 'Run command in non-interactive mode',
      default: false,
    }),
  };
  private sanitizeFlags(flags: any): {
    platform: RequestedPlatform;
    branch: string;
    customServerUrl?: string;
    nonInteractive: boolean;
  } {
    return {
      platform: flags.platform,
      branch: flags.branch,
      customServerUrl: flags.serverUrl,
      nonInteractive: flags.nonInteractive,
    };
  }
  public async run(): Promise<void> {
    const credentials = retrieveCredentials();
    if (!validateCredentials(credentials)) {
      Log.error(
        'Invalid credentials. Please run `eas login or set EXPO_ACCESS_TOKEN or EOO_TOKEN environment variable`'
      );
      process.exit(1);
    }
    const { flags } = await this.parse(Publish);
    const { platform, branch, customServerUrl, nonInteractive } = this.sanitizeFlags(flags);
    if (!branch) {
      Log.error('Branch name is required');
      process.exit(1);
    }
    const vcsClient = resolveVcsClient(true);
    await vcsClient.ensureRepoExistsAsync();
    const commitHash = await vcsClient.getCommitHashAsync();
    const projectDir = process.cwd();
    const hasExpo = isExpoInstalled(projectDir);
    if (!hasExpo) {
      Log.error('Expo is not installed in this project. Please install Expo first.');
      process.exit(1);
    }
    if (!nonInteractive) {
      const confirmed = await confirmAsync({
        message: `Are you sure you want to publish a rollback to the branch ${branch} ?`,
        name: 'export',
        type: 'confirm',
      });
      if (!confirmed) {
        Log.error('Operation cancelled');
        process.exit(1);
      }
    }

    const privateConfig = await getPrivateExpoConfigAsync(projectDir, {
      env: process.env as Env,
    });
    if (privateConfig?.updates?.disableAntiBrickingMeasures) {
      Log.error(
        'When using disableAntiBrickingMeasures, expo-updates is ignoring the embeded update of the app, please use republish command instead'
      );
      process.exit(1);
    }
    const baseUrl = await resolveServerUrl(privateConfig, customServerUrl).catch(e => {
      Log.error(e.message);
      process.exit(1);
    });
    const appId = requireExpoAppId(privateConfig);
    const runtimeSpinner = ora('🔄 Resolving runtime version...').start();
    const runtimeVersions = [
      ...(!platform || platform === RequestedPlatform.All || platform === RequestedPlatform.Ios
        ? [
            {
              runtimeVersion: (
                await resolveRuntimeVersionAsync({
                  exp: privateConfig,
                  platform: 'ios',
                  workflow: await resolveWorkflowAsync(projectDir, Platform.IOS, vcsClient),
                  projectDir,
                  env: process.env as Env,
                })
              )?.runtimeVersion,
              platform: 'ios',
            },
          ]
        : []),
      ...(!platform || platform === RequestedPlatform.All || platform === RequestedPlatform.Android
        ? [
            {
              runtimeVersion: (
                await resolveRuntimeVersionAsync({
                  exp: privateConfig,
                  platform: 'android',
                  workflow: await resolveWorkflowAsync(projectDir, Platform.ANDROID, vcsClient),
                  projectDir,
                  env: process.env as Env,
                })
              )?.runtimeVersion,
              platform: 'android',
            },
          ]
        : []),
    ].filter(({ runtimeVersion }) => !!runtimeVersion);
    if (!runtimeVersions.length) {
      runtimeSpinner.fail('Could not resolve runtime versions for the requested platforms');
      Log.error('Could not resolve runtime versions for the requested platforms');
      process.exit(1);
    }
    runtimeSpinner.succeed('✅ Runtime versions resolved');
    const rollbackSpinner = ora('📦 Uploading rollback...').start();
    const erroredPlatforms: { platform: string; reason: string }[] = [];
    await Promise.all(
      runtimeVersions.map(async ({ runtimeVersion, platform }) => {
        const rollbackUrl = new URL(`${baseUrl}/${appId}/rollback/${branch}`);
        rollbackUrl.searchParams.set('commitHash', commitHash ?? '');
        rollbackUrl.searchParams.set('platform', platform);
        rollbackUrl.searchParams.set('runtimeVersion', runtimeVersion ?? '');

        const response = await fetchWithRetries(rollbackUrl.toString(), {
          method: 'POST',
          headers: {
            ...getAuthHeaders(credentials),
          },
        });
        if (!response.ok) {
          erroredPlatforms.push({
            platform,
            reason: await response.text(),
          });
        }
      })
    );
    if (erroredPlatforms.length) {
      rollbackSpinner.fail('❌ Rollback failed');
      erroredPlatforms.forEach(({ platform, reason }) => {
        Log.error(`Failed to publish rollback for ${platform}: ${reason}`);
      });
      process.exit(1);
    } else {
      rollbackSpinner.succeed('✅ Rollback published successfully');
    }
  }
}
