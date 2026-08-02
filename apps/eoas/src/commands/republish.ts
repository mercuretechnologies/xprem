import { Env } from '@expo/eas-build-job';
import { Command, Flags } from '@oclif/core';

import { getAuthHeaders, retrieveCredentials, validateCredentials } from '../lib/auth';
import { getPrivateExpoConfigAsync, requireExpoAppId, resolveServerUrl } from '../lib/expoConfig';
import { fetchWithRetries } from '../lib/fetch';
import Log from '../lib/log';
import { ora } from '../lib/ora';
import { isExpoInstalled } from '../lib/package';
import { promptAsync } from '../lib/prompts';
import {
  ServerUpdateItem,
  describePublishGroup,
  fetchRuntimeVersions,
  fetchUpdates,
  groupPublishedUpdates,
} from '../lib/serverUpdates';
import { resolveVcsClient } from '../lib/vcs';

export default class Publish extends Command {
  static override args = {};
  static override description = 'Republish a previous update to a branch';
  static override examples = ['<%= config.bin %> <%= command.id %>'];
  static override flags = {
    branch: Flags.string({
      description: 'Name of the branch to point to',
      required: true,
    }),
    platform: Flags.string({
      type: 'option',
      options: ['ios', 'android', 'all'],
      default: 'all',
      required: true,
    }),
    serverUrl: Flags.string({
      description:
        'URL of the self-hosted update server to republish on. Defaults to the origin of updates.url from your Expo config',
      required: false,
    }),
  };
  private sanitizeFlags(flags: any): {
    branch: string;
    platform: string;
    customServerUrl?: string;
  } {
    return {
      branch: flags.branch,
      platform: flags.platform,
      customServerUrl: flags.serverUrl,
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
    const { branch, platform, customServerUrl } = this.sanitizeFlags(flags);
    if (!branch) {
      Log.error('Branch name is required');
      process.exit(1);
    }
    if (!platform) {
      Log.error('Platform is required');
      process.exit(1);
    }
    const vcsClient = resolveVcsClient(true);
    await vcsClient.ensureRepoExistsAsync();
    const projectDir = process.cwd();
    const hasExpo = isExpoInstalled(projectDir);
    if (!hasExpo) {
      Log.error('Expo is not installed in this project. Please install Expo first.');
      process.exit(1);
    }
    const privateConfig = await getPrivateExpoConfigAsync(projectDir, {
      env: process.env as Env,
    });
    const baseUrl = await resolveServerUrl(privateConfig, customServerUrl).catch(e => {
      Log.error(e.message);
      process.exit(1);
    });
    const appId = requireExpoAppId(privateConfig);
    let runtimeVersions;
    try {
      runtimeVersions = await fetchRuntimeVersions({ baseUrl, appId, branch, credentials });
    } catch (e) {
      Log.error(e instanceof Error ? e.message : e);
      process.exit(1);
    }
    const filteredRuntimeVersions = runtimeVersions.filter(
      runtimeVersion => runtimeVersion.numberOfUpdates > 1
    );
    if (filteredRuntimeVersions.length === 0) {
      Log.error('No runtime versions found');
      process.exit(1);
    }
    const selectedRuntimeVersion = await promptAsync({
      type: 'select',
      name: 'runtimeVersion',
      message: 'Select a runtime version',
      choices: filteredRuntimeVersions.map(runtimeVersion => ({
        title: runtimeVersion.runtimeVersion,
        value: runtimeVersion.runtimeVersion,
      })),
    });
    Log.log(`Selected runtime version: ${selectedRuntimeVersion.runtimeVersion}`);
    let allUpdates: ServerUpdateItem[];
    try {
      allUpdates = await fetchUpdates({
        baseUrl,
        appId,
        branch,
        runtimeVersion: selectedRuntimeVersion.runtimeVersion,
        credentials,
      });
    } catch (e) {
      Log.error(e instanceof Error ? e.message : e);
      process.exit(1);
    }
    // Rollback markers have no files to republish.
    const updates = allUpdates.filter(u => u.updateUUID !== 'Rollback to embedded');
    if (updates.length === 0) {
      Log.error(
        `No republishable updates found for runtime version ${selectedRuntimeVersion.runtimeVersion}.`
      );
      process.exit(1);
    }

    const { groups } = groupPublishedUpdates(updates);
    // Offer the group mode only when there is something to group and the user
    // did not already narrow the run to one platform.
    let mode: 'group' | 'single' = 'single';
    if (platform === 'all' && groups.length > 0) {
      const selectedMode = await promptAsync({
        type: 'select',
        name: 'mode',
        message: 'What do you want to republish?',
        choices: [
          {
            title: 'A full publish (all its platforms together)',
            description: 'Only for servers in control plane mode',
            value: 'group',
          },
          {
            title: 'A single platform update',
            description: 'Pick one iOS or Android update',
            value: 'single',
          },
        ],
      });
      mode = selectedMode.mode;
    }

    if (mode === 'group') {
      const selectedGroup = await promptAsync({
        type: 'select',
        name: 'group',
        message: 'Select a publish to republish',
        choices: groups.map(group => ({
          ...describePublishGroup(group),
          value: group,
        })),
      });
      const group = selectedGroup.group;
      const republishUrl = new URL(`${baseUrl}/${appId}/republish/${branch}`);
      republishUrl.searchParams.set('runtimeVersion', selectedRuntimeVersion.runtimeVersion);
      republishUrl.searchParams.set('publishGroup', group.publishGroup);
      const republishSpinner = ora(
        `🔄 Republishing ${group.platforms.join(' + ')} updates...`
      ).start();
      const response = await fetchWithRetries(republishUrl.toString(), {
        method: 'POST',
        headers: {
          ...getAuthHeaders(credentials),
          'use-cli-auth': 'true',
          'Content-Type': 'application/json',
        },
      });
      if (!response.ok) {
        republishSpinner.fail('❌ Republish failed');
        Log.error(`Failed to republish publish group: ${await response.text()}`);
        process.exit(1);
      }
      const result = (await response.json()) as { publishGroup?: string };
      republishSpinner.succeed(
        result.publishGroup
          ? `✅ Republished ${group.platforms.join(' + ')} as publish group ${result.publishGroup}`
          : `✅ Republished ${group.platforms.join(' + ')}`
      );
      return;
    }

    const platformUpdates = updates.filter(u => platform === 'all' || u.platform === platform);
    if (platformUpdates.length === 0) {
      Log.error(
        `No republishable updates found for runtime version ${selectedRuntimeVersion.runtimeVersion} on platform ${platform}.`
      );
      process.exit(1);
    }
    const selectedUpdated = await promptAsync({
      type: 'select',
      name: 'update',
      message: 'Select an update to republish',
      choices: platformUpdates.map(update => ({
        title: update.updateUUID,
        value: update,
        description: `Created at: ${update.createdAt}, Platform: ${update.platform}, Commit hash: ${update.commitHash}`,
      })),
    });
    Log.log(`Re-publishing update: ${selectedUpdated.update.updateUUID}`);
    const republishUrl = new URL(`${baseUrl}/${appId}/republish/${branch}`);
    republishUrl.searchParams.set('platform', selectedUpdated.update.platform);
    republishUrl.searchParams.set('runtimeVersion', selectedRuntimeVersion.runtimeVersion);
    republishUrl.searchParams.set('updateId', selectedUpdated.update.updateId);
    republishUrl.searchParams.set('commitHash', selectedUpdated.update.commitHash);
    const republishSpinner = ora('🔄 Republishing update...').start();
    const republishResponse = await fetchWithRetries(republishUrl.toString(), {
      method: 'POST',
      headers: {
        ...getAuthHeaders(credentials),
        'use-cli-auth': 'true',
        'Content-Type': 'application/json',
      },
    });
    if (!republishResponse.ok) {
      republishSpinner.fail('❌ Republish failed');
      Log.error(`Failed to republish update: ${await republishResponse.text()}`);
      process.exit(1);
    }
    republishSpinner.succeed('✅ Republish successful');
  }
}
