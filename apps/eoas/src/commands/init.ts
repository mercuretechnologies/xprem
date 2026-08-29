import { Command } from '@oclif/core';
import chalk from 'chalk';
import fs from 'fs-extra';
import path from 'path';

import { Credentials, retrieveExpoCredentials } from '../lib/auth';
import {
  createOrModifyExpoConfigAsync,
  getExpoConfigUpdateUrl,
  getPrivateExpoConfigAsync,
} from '../lib/expoConfig';
import Log from '../lib/log';
import { ora } from '../lib/ora';
import { isExpoInstalled } from '../lib/package';
import { confirmAsync, promptAsync, selectAsync } from '../lib/prompts';
import {
  ExpoImportPlan,
  ExpoImportPlanItem,
  ImportKeysConfig,
  fetchHistoryJobStatus,
  fetchImportPreview,
  importExpoApp,
  loginAsAdmin,
} from '../lib/serverImport';
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
        '  See https://mercure-technologies.gitbook.io/xprem/stateless-mode/getting-started for details.',
      name: 'appId',
      type: 'text',
      initial: detectedAppId,
      validate: v => !!v,
    });
    const { updateUrl: promptedUrl } = await promptAsync({
      message:
        'Enter the URL of your update server (ex: https://customota.com or https://api.example.com/ota)',
      name: 'updateUrl',
      type: 'text',
      initial: (getExpoConfigUpdateUrl(config) || '').replace(/\/manifest$/, ''),
      validate: v => {
        return !!v && isValidUpdateUrl(v);
      },
    });
    let manifestEndpoint = `${promptedUrl.replace(/\/+$/, '')}/manifest`;
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
      // Branch surfing: a build on a channel that allows it can be pointed at
      // another branch at runtime, and expo-updates only accepts an override for
      // header keys that already existed at build time — so these have to be
      // declared here even when the value is empty. Dropping one of them does not
      // disable the feature, it strips that header from every poll for the rest of
      // the install; the picker refuses to appear rather than let that happen.
      requestHeaders: {
        'expo-channel-name': {
          __comment: 'Declare as a literal if you surf branches: see xprem-branch below.',
          value: 'process.env.RELEASE_CHANNEL',
        },
        'expo-app-id': appId,
        'xprem-branch': {
          __comment: 'Branch surfing — the branch to serve; empty means the channel decides.',
          value: '',
        },
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

    // Control-plane servers can copy the app straight from Expo (same UUID,
    // branches, channels, optionally history) so the first publish just works.
    await offerServerImport(manifestEndpoint.replace(/\/manifest$/, ''), appId);
  }
}

// offerServerImport is the optional last step of init: on a control-plane
// server, import the app from Expo so the server is ready without a dashboard
// visit. The server itself refuses when it has no control plane — there is no
// public capability probe. Every failure warns and returns, an import problem
// never fails init.
async function offerServerImport(baseUrl: string, expoAppId: string): Promise<void> {
  Log.newLine();
  Log.withInfo(
    'Servers with a control plane (DB_URL set) can import this app from Expo right\n' +
      'now (same project UUID, branches and channels), ready before your first publish.'
  );
  const wantsImport = await confirmAsync({
    message:
      'Import this app from Expo into your server now? (control plane only, requires an admin account)',
    name: 'import',
    type: 'confirm',
  });
  if (!wantsImport) {
    Log.log('You can import it later from the dashboard (New app > Import from Expo).');
    return;
  }
  try {
    const expoCredentials = await resolveExpoCredentials();
    const adminToken = await promptAdminLogin(baseUrl);
    if (!adminToken) {
      return;
    }

    const previewSpinner = ora('Fetching the import preview from your server').start();
    let plan: ExpoImportPlan;
    try {
      plan = await fetchImportPreview({ baseUrl, adminToken, expoCredentials, expoAppId });
      previewSpinner.succeed('Import preview ready');
    } catch (e) {
      previewSpinner.fail('Could not preview the import');
      throw e;
    }
    if (plan.conflict) {
      Log.warn(`Nothing to do: ${plan.conflict}`);
      return;
    }
    printImportPlan(plan);

    const confirmed = await confirmAsync({
      message: 'Proceed with this import?',
      name: 'proceed',
      type: 'confirm',
    });
    if (!confirmed) {
      Log.log('Import skipped. You can run it later from the dashboard.');
      return;
    }

    const keysConfig = await promptKeysConfig();
    const historyLimit = await selectAsync<number>(
      'Also copy the newest published updates? (runs in the background on the server)',
      [
        { title: 'No, structure only', value: 0 },
        { title: 'The 10 newest publishes', value: 10 },
        { title: 'The 25 newest publishes', value: 25 },
        { title: 'The 50 newest publishes', value: 50 },
      ]
    );

    const importSpinner = ora('Importing the app into your server').start();
    let result;
    try {
      result = await importExpoApp(
        { baseUrl, adminToken, expoCredentials, expoAppId },
        keysConfig,
        historyLimit
      );
      importSpinner.succeed(
        `Imported "${result.name}" with ${result.branchCount} branch(es) and ${result.channelCount} channel(s)`
      );
    } catch (e) {
      importSpinner.fail('Import failed');
      throw e;
    }
    for (const skipped of result.skipped ?? []) {
      Log.warn(`Skipped ${skipped}`);
    }
    if (result.historyJobId) {
      await followHistoryJob(baseUrl, adminToken, result.historyJobId);
    }
    Log.withTick('Your server knows this app: you can publish right away.');
  } catch (e) {
    Log.warn(`Server import skipped: ${e instanceof Error ? e.message : e}`);
    Log.warn(
      'Your local configuration is done; you can import the app from the dashboard instead.'
    );
  }
}

// resolveExpoCredentials prefers what the machine already has (EXPO_TOKEN or
// the expo-cli session) and only prompts for a token as a last resort.
async function resolveExpoCredentials(): Promise<Credentials> {
  const credentials = retrieveExpoCredentials();
  if (credentials.token || credentials.sessionSecret) {
    Log.log(chalk.dim('Using the Expo credentials already on this machine.'));
    return credentials;
  }
  const { expoToken } = await promptAsync({
    message: 'Enter an Expo access token (create one at https://expo.dev/settings/access-tokens)',
    name: 'expoToken',
    type: 'password',
    validate: v => !!v,
  });
  return { token: expoToken };
}

// promptAdminLogin exchanges dashboard admin credentials for a session token,
// with two retries on bad credentials; null means the person gave up.
async function promptAdminLogin(baseUrl: string): Promise<string | null> {
  for (let attempt = 0; attempt < 3; attempt++) {
    const { email } = await promptAsync({
      message: 'Dashboard admin email',
      name: 'email',
      type: 'text',
      validate: v => !!v,
    });
    const { password } = await promptAsync({
      message: 'Dashboard admin password',
      name: 'password',
      type: 'password',
      validate: v => !!v,
    });
    const spinner = ora('Signing in to your server').start();
    try {
      const token = await loginAsAdmin(baseUrl, email, password);
      spinner.succeed('Signed in');
      return token;
    } catch (e) {
      spinner.fail(`Sign in failed: ${e instanceof Error ? e.message : e}`);
    }
  }
  Log.warn('Too many failed sign-ins.');
  return null;
}

function planLine(item: ExpoImportPlanItem, kind: 'branch' | 'channel'): string {
  if (item.skipReason) {
    return `  ${chalk.red('✗')} ${item.name} ${chalk.dim(`— ${item.skipReason}`)}`;
  }
  const mapping =
    kind === 'channel'
      ? chalk.dim(item.mappedBranch ? ` → ${item.mappedBranch}` : ' (unmapped)')
      : '';
  const warning = item.warning ? ` ${chalk.yellow(`— ${item.warning}`)}` : '';
  return `  ${chalk.green('✓')} ${item.name}${mapping}${warning}`;
}

function printImportPlan(plan: ExpoImportPlan): void {
  const lines: string[] = [`App: ${chalk.bold(plan.name)} ${chalk.dim(`(${plan.appId})`)}`];
  if (plan.name !== plan.expoName) {
    lines.push(
      chalk.yellow(`The Expo name "${plan.expoName}" is not usable here, the UUID is used instead.`)
    );
  }
  lines.push('', `Branches (${plan.branches.length}):`);
  lines.push(...plan.branches.map(item => planLine(item, 'branch')));
  lines.push('', `Channels (${plan.channels.length}):`);
  lines.push(...plan.channels.map(item => planLine(item, 'channel')));
  Log.note(lines.join('\n'), 'Import preview');
}

// promptKeysConfig picks where the app's signing keys live; "database" needs
// nothing from the person, which is why it is the default.
async function promptKeysConfig(): Promise<ImportKeysConfig> {
  const mode = await selectAsync<'database' | 'aws-secrets-manager'>(
    "Where should the server store this app's code signing keys?",
    [
      {
        title: 'On the server (recommended)',
        value: 'database',
        description: 'generated and sealed in the database, nothing to configure',
      },
      {
        title: 'AWS Secrets Manager',
        value: 'aws-secrets-manager',
        description: 'the keys live in two existing secrets',
      },
    ]
  );
  if (mode === 'database') {
    return { mode };
  }
  const { publicSecretId } = await promptAsync({
    message: 'Secret id holding the public key',
    name: 'publicSecretId',
    type: 'text',
    validate: v => !!v,
  });
  const { privateSecretId } = await promptAsync({
    message: 'Secret id holding the private key',
    name: 'privateSecretId',
    type: 'text',
    validate: v => !!v,
  });
  return { mode, publicSecretId, privateSecretId };
}

// followHistoryJob polls the background history job until it settles, so init
// ends with the server fully ready instead of "check the dashboard later".
async function followHistoryJob(baseUrl: string, adminToken: string, jobId: string): Promise<void> {
  const spinner = ora('Copying the update history').start();
  for (;;) {
    let status;
    try {
      status = await fetchHistoryJobStatus(baseUrl, adminToken, jobId);
    } catch (e) {
      spinner.warn(
        `Lost track of the history import (${
          e instanceof Error ? e.message : e
        }); it keeps running on the server.`
      );
      return;
    }
    if (status.state === 'done') {
      spinner.succeed(`Update history copied: ${status.imported} update(s) imported`);
      for (const skipped of status.skipped ?? []) {
        Log.warn(`Skipped ${skipped}`);
      }
      return;
    }
    if (status.state === 'failed' || status.state === 'canceled') {
      spinner.fail(
        status.state === 'failed'
          ? `History import failed: ${status.error || 'unknown error'}`
          : 'History import canceled'
      );
      return;
    }
    spinner.update(`Copying the update history (${status.processed}/${status.total})`);
    await new Promise(resolve => setTimeout(resolve, 2000));
  }
}
