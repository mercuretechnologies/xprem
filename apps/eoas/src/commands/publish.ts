import { Env, Platform } from '@expo/eas-build-job';
import spawnAsync from '@expo/spawn-async';
import { Command, Flags } from '@oclif/core';
import { randomUUID } from 'crypto';
import FormData from 'form-data';
import fs from 'fs-extra';
import mime from 'mime';
import path from 'path';

import {
  RequestUploadUrlItem,
  activeRolloutConflictMessage,
  computeFilesRequests,
  requestUploadUrls,
  resolveUploadRequests,
} from '../lib/assets';
import { getAuthHeaders, retrieveCredentials, validateCredentials } from '../lib/auth';
import {
  RequestedPlatform,
  getPrivateExpoConfigAsync,
  getPublicExpoConfigAsync,
  requireExpoAppId,
  resolveServerUrl,
} from '../lib/expoConfig';
import { fetchWithRetries, fetchWithRetriesRebuildingBody } from '../lib/fetch';
import Log from '../lib/log';
import { ora } from '../lib/ora';
import { isExpoInstalled } from '../lib/package';
import { resolvePackageRunner, splitPackageRunner } from '../lib/packageRunner';
import { confirmAsync } from '../lib/prompts';
import { RateLimiter } from '../lib/rateLimiter';
import { ensureRepoIsCleanAsync } from '../lib/repo';
import { resolveRuntimeVersionAsync } from '../lib/runtimeVersion';
import { resolveVcsClient } from '../lib/vcs';
import { resolveWorkflowAsync } from '../lib/workflow';

export default class Publish extends Command {
  static override args = {};
  static override description = 'Publish a new update to the self-hosted update server';
  static override examples = ['<%= config.bin %> <%= command.id %>'];
  static override flags = {
    platform: Flags.string({
      type: 'option',
      options: Object.values(RequestedPlatform),
      default: RequestedPlatform.All,
      required: false,
    }),
    channel: Flags.string({
      description: 'Name of the channel to publish the update to',
      required: false,
      deprecated: {
        message:
          'Channel was initially used to provide RELEASE_CHANNEL in the environment when resolving the runtime version. It is no longer needed, you can use RELEASE_CHANNEL={channel} eoas publish --branch={branch} instead',
      },
    }),
    disableRepositoryCheck: Flags.boolean({
      description: 'Disable repository check (Useful for CI/CD)',
      default: false,
      hidden: true,
    }),
    branch: Flags.string({
      description: 'Name of the branch to point to',
      required: true,
    }),
    serverUrl: Flags.string({
      description:
        'URL of the self-hosted update server to publish to. Defaults to the origin of updates.url from your Expo config',
      required: false,
    }),
    nonInteractive: Flags.boolean({
      description: 'Run command in non-interactive mode',
      default: false,
    }),
    outputDir: Flags.string({
      description:
        "Where to write build output. You can override the default dist output directory if it's being used by something else",
      default: 'dist',
    }),
    packageRunner: Flags.string({
      description:
        'Package runner to use for spawning Expo CLI commands (e.g. npx, bunx, "pnpm exec"). Can also be set via EOAS_PACKAGE_RUNNER env var. Defaults to npx.',
      required: false,
    }),
    message: Flags.string({
      char: 'm',
      description:
        'A short message describing the update. Defaults to the latest git commit message.',
      required: false,
    }),
    dumpSourcemap: Flags.boolean({
      description:
        'Emit Hermes source maps alongside the bundle so the published artifact can be symbolicated by tools like Sentry or PostHog.',
      default: false,
    }),
    'rollout-percentage': Flags.integer({
      min: 1,
      max: 99,
      description:
        'Publish this update as a progressive rollout served to N% of devices (1-99). The remaining devices keep receiving the previous update of each branch/runtime version. With --platform all, the rollout applies independently to each platform. Progression (increase, end, revert) is managed from the dashboard.',
    }),
    'upload-rate': Flags.string({
      description:
        'Maximum number of asset uploads started per second. Accepts decimals (e.g. 1.5). Lower this if your storage provider rate-limits uploads.',
      default: '10',
    }),
  };
  private sanitizeFlags(flags: any): {
    platform: RequestedPlatform;
    branch: string;
    customServerUrl?: string;
    nonInteractive: boolean;
    disableRepositoryCheck: boolean;
    outputDir: string;
    packageRunner: string;
    providedDeprecatedChannel?: string;
    message?: string;
    dumpSourcemap: boolean;
    rolloutPercentage?: number;
    uploadRate: number;
  } {
    const uploadRate = Number(flags['upload-rate']);
    if (!Number.isFinite(uploadRate) || uploadRate <= 0) {
      Log.error('--upload-rate must be a positive number');
      process.exit(1);
    }
    return {
      disableRepositoryCheck: flags.disableRepositoryCheck,
      platform: flags.platform,
      branch: flags.branch,
      customServerUrl: flags.serverUrl,
      nonInteractive: flags.nonInteractive,
      outputDir: flags.outputDir,
      packageRunner: resolvePackageRunner(flags.packageRunner, process.cwd()),
      providedDeprecatedChannel: flags.channel,
      message: flags.message,
      dumpSourcemap: flags.dumpSourcemap,
      rolloutPercentage: flags['rollout-percentage'],
      uploadRate,
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
    const {
      platform,
      nonInteractive,
      branch,
      customServerUrl,
      outputDir,
      packageRunner,
      providedDeprecatedChannel,
      disableRepositoryCheck,
      message,
      dumpSourcemap,
      rolloutPercentage,
      uploadRate,
    } = this.sanitizeFlags(flags);
    if (!branch) {
      Log.error('Branch name is required');
      process.exit(1);
    }
    // Bucket capacity is ~1s of budget so a low --upload-rate also caps the
    // initial burst, not just the steady rate.
    const publishAssetsRateLimiter = new RateLimiter(
      'publish-eoas-assets',
      Math.max(1, Math.ceil(uploadRate)),
      uploadRate
    );
    const projectDir = process.cwd();
    const hasExpo = isExpoInstalled(projectDir);
    if (!hasExpo) {
      Log.error('Expo is not installed in this project. Please install Expo first.');
      process.exit(1);
    }
    const vcsClient = resolveVcsClient(true);
    if (!disableRepositoryCheck) {
      await ensureRepoIsCleanAsync(vcsClient, nonInteractive);
    }
    const config = await getPrivateExpoConfigAsync(projectDir, {
      env: {
        ...(process.env as Env),
        ...(providedDeprecatedChannel ? { RELEASE_CHANNEL: providedDeprecatedChannel } : {}),
      },
      packageRunner,
    });
    const serverUrl = await resolveServerUrl(config, customServerUrl).catch(e => {
      Log.error(e.message);
      process.exit(1);
    });
    const appId = requireExpoAppId(config);
    // A URL passed on the command line needs no confirmation, and `eoas init`
    // would not fix it anyway.
    if (!nonInteractive && !customServerUrl) {
      const confirmed = await confirmAsync({
        message: `Is this the correct URL of your self-hosted update server? ${serverUrl}`,
        name: 'export',
        type: 'confirm',
      });
      if (!confirmed) {
        Log.error('Please run `eoas init` to setup the correct update url');
        process.exit(1);
      }
    }

    const commitHash = await vcsClient.getCommitHashAsync();

    let resolvedMessage = message;
    if (!resolvedMessage && vcsClient.canGetLastCommitMessage()) {
      resolvedMessage = (await vcsClient.getLastCommitMessageAsync()) ?? undefined;
    }

    const runtimeSpinner = ora('🔄 Resolving runtime version...').start();
    const runtimeVersions = [
      ...(!platform || platform === RequestedPlatform.All || platform === RequestedPlatform.Ios
        ? [
            {
              runtimeVersion: (
                await resolveRuntimeVersionAsync({
                  exp: config,
                  platform: 'ios',
                  workflow: await resolveWorkflowAsync(projectDir, Platform.IOS, vcsClient),
                  projectDir,
                  env: {
                    ...(process.env as Env),
                    ...(providedDeprecatedChannel
                      ? { RELEASE_CHANNEL: providedDeprecatedChannel }
                      : {}),
                  },
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
                  exp: config,
                  platform: 'android',
                  workflow: await resolveWorkflowAsync(projectDir, Platform.ANDROID, vcsClient),
                  projectDir,
                  env: {
                    ...(process.env as Env),
                    ...(providedDeprecatedChannel
                      ? { RELEASE_CHANNEL: providedDeprecatedChannel }
                      : {}),
                  },
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
    const cleaningSpinner = ora(`🗑️ Cleaning up ${outputDir} directory...`).start();
    try {
      await fs.remove(path.join(projectDir, outputDir));
      cleaningSpinner.succeed('✅ Cleanup completed');
    } catch (e) {
      cleaningSpinner.fail('❌ Failed to clean up the output directory');
      Log.error(e);
      process.exit(1);
    }
    const exportSpinner = ora('📦 Exporting project files...').start();
    try {
      const specifiedPlatform = platform === RequestedPlatform.All ? [] : ['--platform', platform];
      const sourcemapArgs = dumpSourcemap ? ['--dump-sourcemap'] : [];
      const [runnerCommand, runnerArgs] = splitPackageRunner(packageRunner);
      const { stdout } = await spawnAsync(
        runnerCommand,
        [
          ...runnerArgs,
          'expo',
          'export',
          '--output-dir',
          outputDir,
          ...sourcemapArgs,
          ...specifiedPlatform,
        ],
        {
          cwd: projectDir,
          env: {
            ...process.env,
            EXPO_NO_DOTENV: '1',
          },
        }
      );
      exportSpinner.succeed('🚀 Project exported successfully');
      Log.withInfo(stdout);
    } catch (e) {
      exportSpinner.fail(`❌ Failed to export the project, ${e}`);
      process.exit(1);
    }
    const publicConfig = await getPublicExpoConfigAsync(projectDir, {
      skipSDKVersionRequirement: true,
      packageRunner,
    });
    if (!publicConfig) {
      Log.error(
        'Could not find Expo config in this project. Please make sure you have an Expo config.'
      );
      process.exit(1);
    }
    // eslint-disable-next-line
    fs.writeJsonSync(path.join(projectDir, outputDir, 'expoConfig.json'), publicConfig, {
      spaces: 2,
    });
    Log.withInfo(`expoConfig.json file created in ${outputDir} directory`);
    const uploadFilesSpinner = ora('📤 Uploading files...').start();
    const files = computeFilesRequests(projectDir, outputDir, platform || RequestedPlatform.All);
    if (!files.length) {
      uploadFilesSpinner.fail('No files to upload');
      process.exit(1);
    }
    let uploadUrls: {
      uploadRequests: RequestUploadUrlItem[];
      updateId: string;
      platform: string;
      runtimeVersion: string;
      rolloutPercentage?: number;
      publishGroup?: string;
    }[] = [];
    // One group id for the whole run: every platform update of this publish
    // shares it, so control plane servers list them as a single publish.
    const publishGroupId = randomUUID();
    try {
      uploadUrls = await Promise.all(
        runtimeVersions.map(async ({ runtimeVersion, platform }) => {
          if (!runtimeVersion) {
            throw new Error('Runtime version is not resolved');
          }
          return {
            ...(await requestUploadUrls({
              body: {
                fileNames: files.map(file => file.path),
              },
              requestUploadUrl: `${serverUrl}/${appId}/requestUploadUrl/${branch}`,
              auth: credentials,
              runtimeVersion,
              platform,
              commitHash,
              message: resolvedMessage,
              rolloutPercentage,
              publishGroup: publishGroupId,
              branch,
            })),
            runtimeVersion,
            platform,
          };
        })
      );
      // Every path and URL the server handed back is checked here, before a
      // single file is opened. A server that forges a filePath would otherwise
      // make the CLI read arbitrary files off this machine and PUT them wherever
      // it likes. Validated per response: each platform gets its own upload URLs
      // for the same files.
      const resolvedUploads = (
        await Promise.all(
          uploadUrls.map(({ uploadRequests }) =>
            resolveUploadRequests({
              uploadRequests,
              exportDir: path.join(projectDir, outputDir),
              manifest: files,
            })
          )
        )
      ).flat();
      await Promise.all(
        resolvedUploads.map(async ({ item: itm, absolutePath, manifestEntry }) => {
          await publishAssetsRateLimiter.take();
          const isLocalBucketFileUpload = itm.requestUploadUrl.startsWith(
            `${serverUrl}/${appId}/uploadLocalFile`
          );
          if (isLocalBucketFileUpload) {
            // A stream-backed body is consumed by the first attempt, so each
            // retry rebuilds the multipart body from a buffer.
            const fileBuffer = await fs.readFile(absolutePath);
            const response = await fetchWithRetriesRebuildingBody(itm.requestUploadUrl, () => {
              const formData = new FormData();
              formData.append(itm.fileName, fileBuffer, {
                filename: path.basename(absolutePath),
              });
              return {
                method: 'PUT',
                headers: {
                  ...formData.getHeaders(),
                  ...getAuthHeaders(credentials),
                },
                body: formData,
                // The URL was validated as a string; following a redirect would
                // send these bytes to an origin nothing ever checked.
                redirect: 'error',
              };
            });
            if (!response.ok) {
              Log.error('Failed to upload file', await response.text());
              throw new Error('Failed to upload file');
            }
            return;
          }
          let contentType = mime.getType(manifestEntry.ext);
          if (!contentType) {
            contentType = 'application/octet-stream';
          }
          const buffer = await fs.readFile(absolutePath);
          const response = await fetchWithRetries(itm.requestUploadUrl, {
            method: 'PUT',
            headers: {
              'Content-Type': contentType,
              'Cache-Control': 'max-age=31556926',
              ...(itm.headers ?? {}),
            },
            body: buffer,
            // Only the URL string was validated. node-fetch follows up to 20
            // redirects by default with no protocol or host check, so a server
            // handing back a valid https URL that 302s to http://internal-host
            // would exfiltrate the bundle in cleartext to an origin of its
            // choosing. Nothing legitimate redirects an upload PUT.
            redirect: 'error',
          });
          if (!response.ok) {
            Log.error('❌ File upload failed', await response.text());
            process.exit(1);
          }
        })
      );

      uploadFilesSpinner.succeed('✅ Files uploaded successfully');
    } catch (e) {
      uploadFilesSpinner.fail('❌ Failed to upload static files');
      Log.error(e);
      process.exit(1);
    }

    const markAsFinishedSpinner = ora('🔗 Marking the updates as finished...').start();
    const results = await Promise.all(
      uploadUrls.map(
        async ({
          updateId,
          platform,
          runtimeVersion,
          rolloutPercentage: echoedRolloutPercentage,
        }) => {
          const markAsUploadedUrl = new URL(`${serverUrl}/${appId}/markUpdateAsUploaded/${branch}`);
          markAsUploadedUrl.searchParams.set('platform', platform);
          markAsUploadedUrl.searchParams.set('updateId', updateId);
          markAsUploadedUrl.searchParams.set('runtimeVersion', runtimeVersion);

          const response = await fetchWithRetries(markAsUploadedUrl.toString(), {
            method: 'POST',
            headers: {
              ...getAuthHeaders(credentials),
              'Content-Type': 'application/json',
            },
          });
          // If success and status code = 200
          if (response.ok) {
            Log.withInfo(`✅ Update ready for ${platform}`);
            // Announce only when the server echoed the percentage back: an old server
            // silently ignores the param and ships the update to 100% of devices.
            if (rolloutPercentage !== undefined && echoedRolloutPercentage !== undefined) {
              Log.withInfo(
                `Progressive rollout started at ${rolloutPercentage}% for ${platform}. Manage it from the dashboard.`
              );
            }
            return 'deployed';
          }
          // If response.status === 406 duplicate update
          if (response.status === 406) {
            Log.withInfo(`⚠️ There is no change in the update for ${platform}, ignored...`);
            if (rolloutPercentage !== undefined) {
              Log.withInfo(`No changes detected for ${platform}, no rollout was started.`);
            }
            return 'identical';
          }
          // The partial unique index can activate a rollout on this (branch, rtv) between
          // requestUploadUrl and markUpdateAsUploaded, closing the publish race with a 409.
          if (response.status === 409) {
            Log.error(activeRolloutConflictMessage(branch));
            return 'error';
          }
          Log.error('❌ Failed to mark the update as finished for platform', platform);
          Log.newLine();
          Log.error(await response.text());
          return 'error';
        }
      )
    );
    const erroredUpdates = results.filter(result => result === 'error');
    const hasSuccess = results.some(result => result === 'deployed');
    const allIdentical = results.every(result => result === 'identical');
    if (allIdentical) {
      markAsFinishedSpinner.warn('⚠️ No changes found in the update, nothing to deploy');
      return;
    }
    if (erroredUpdates.length) {
      markAsFinishedSpinner.fail('❌ Some errors occurred while marking updates as finished');
      throw new Error();
    } else {
      markAsFinishedSpinner.succeed(
        `\n✅ Your update has been successfully pushed to ${serverUrl}`
      );
    }
    if (hasSuccess) {
      Log.withInfo(`🌿 Branch: \`${branch}\``);
      Log.withInfo(`⏳ Deployed at: \`${new Date().toUTCString()}\`\n`);
      const groupAcknowledged = uploadUrls.every(u => u.publishGroup === publishGroupId);
      if (groupAcknowledged) {
        Log.withInfo(`📦 Publish group: \`${publishGroupId}\``);
      } else if (uploadUrls.length > 1) {
        // Only worth a note when several platforms were published: a single
        // update has nothing to group anyway.
        Log.withInfo(
          'ℹ️ Platform updates were published without grouping (publish groups require a server in control plane mode).'
        );
      }
      Log.withInfo('🔥 Your users will receive the latest update automatically!');
    }
  }
}
