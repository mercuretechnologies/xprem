import { Command } from '@oclif/core';
import fs from 'fs-extra';
import path from 'path';

import {
  createOrModifyExpoConfigAsync,
  getExpoConfigUpdateUrl,
  getPrivateExpoConfigAsync,
} from '../lib/expoConfig';
import Log from '../lib/log';
import { ora } from '../lib/ora';
import { isExpoInstalled } from '../lib/package';
import { confirmAsync, promptAsync } from '../lib/prompts';
import { ensurePrivateKeyIgnored, isValidUpdateUrl } from '../lib/utils';

export default class Init extends Command {
  static override args = {};
  static override description = 'Configure your existing expo project with xprem';
  static override examples = ['<%= config.bin %> <%= command.id %>'];
  static override flags = {};
  public async run(): Promise<void> {
    const projectDir = process.cwd();
    const hasExpo = isExpoInstalled(projectDir);
    if (!hasExpo) {
      Log.error('Expo is not installed in this project. Please install Expo first.');
      return;
    }
    const config = await getPrivateExpoConfigAsync(projectDir);
    if (!config) {
      Log.error(
        'Could not find Expo config in this project. Please make sure you have an Expo config.'
      );
      return;
    }
    const detectedAppId = (config.extra as { eas?: { projectId?: string } } | undefined)?.eas
      ?.projectId;
    const { appId } = await promptAsync({
      message:
        'Enter the project id for this project (sent as the expo-app-id header).\n' +
        '  See https://mercure-technologies.gitbook.io/expo-open-ota/stateless-mode/getting-started for details.',
      name: 'appId',
      type: 'text',
      initial: detectedAppId,
      validate: v => !!v,
    });
    const { updateUrl: promptedUrl } = await promptAsync({
      message: 'Enter the URL of your update server (ex: https://customota.com)',
      name: 'updateUrl',
      type: 'text',
      initial: (getExpoConfigUpdateUrl(config) || '').replace(/\/manifest$/, ''),
      validate: v => {
        return !!v && isValidUpdateUrl(v);
      },
    });
    let manifestEndpoint = `${promptedUrl}/manifest`;
    const updateUrl = getExpoConfigUpdateUrl(config);
    if (updateUrl && !updateUrl.includes('expo.dev')) {
      const confirmed = await confirmAsync({
        message: `Expo config already has an update URL set to ${updateUrl}. Do you want to replace it?`,
        name: 'replace',
        type: 'confirm',
      });
      if (!confirmed) {
        manifestEndpoint = updateUrl;
      }
    }
    const confirmed = await confirmAsync({
      message: 'Do you have already generated your certificates for code signing?',
      name: 'certificates',
      type: 'confirm',
    });
    if (!confirmed) {
      Log.fail('You need to generate your certificates first by using npx eoas generate-certs');
      return;
    }
    const { codeSigningCertificatePath } = await promptAsync({
      message: 'Enter the path to your code signing certificate (ex: ./certs/certificate.pem)',
      name: 'codeSigningCertificatePath',
      type: 'text',
      initial: './certs/certificate.pem',
      validate: v => {
        try {
          const fullPath = path.resolve(projectDir, v);
          // eslint-disable-next-line
          const fileExists = fs.existsSync(fullPath);
          if (!fileExists) {
            Log.newLine();
            Log.error('File does not exist');
            return false;
          }
          // eslint-disable-next-line
          const key = fs.readFileSync(fullPath, 'utf8');
          if (!key) {
            Log.error('Empty key');
            return false;
          }
          return true;
        } catch {
          return false;
        }
      },
    });
    // The code signing fields are guarded so the dev server can run without the
    // private key: DISABLE_CODE_SIGNING=true expo start --dev-client. The strings
    // are emitted as raw expressions by createOrModifyExpoConfigAsync.
    const newUpdateConfig = {
      url: manifestEndpoint,
      codeSigningMetadata:
        "process.env.DISABLE_CODE_SIGNING ? undefined : { keyid: 'main', alg: 'rsa-v1_5-sha256' }",
      codeSigningCertificate: `process.env.DISABLE_CODE_SIGNING ? undefined : '${codeSigningCertificatePath
        .replace(/\\/g, '\\\\')
        .replace(/'/g, "\\'")}'`,
      enabled: true,
      requestHeaders: {
        'expo-channel-name': 'process.env.RELEASE_CHANNEL',
        'expo-app-id': appId,
      },
    };
    const updateConfigSpinner = ora('Updating Expo config').start();
    try {
      await createOrModifyExpoConfigAsync(projectDir, {
        updates: newUpdateConfig,
      });
      updateConfigSpinner.succeed(
        'Expo config successfully updated do not forget to format the file with prettier or eslint'
      );
    } catch (e) {
      updateConfigSpinner.fail('Failed to update Expo config');
      Log.error(e);
    }
    ensurePrivateKeyIgnored(projectDir);
  }
}
