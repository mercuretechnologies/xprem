import { Env } from '@expo/eas-build-job';
import { Command, Flags } from '@oclif/core';

import { getExpoAppId, getPrivateExpoConfigAsync, resolveServerUrl } from '../lib/expoConfig';
import { fetchWithRetries } from '../lib/fetch';
import Log from '../lib/log';
import { ora } from '../lib/ora';
import { isExpoInstalled } from '../lib/package';

// probeManifest asks the server for a manifest the way a client would, and
// reports what came back. `appId` is omitted to impersonate a v1 client — the
// point of this command is that the header may legitimately be absent.
//
// A runtime version nothing was ever published against is deliberate: the
// server answers "no update available" instead of streaming a bundle, which
// exercises app resolution without downloading anything. Resolution happens
// before any update lookup, so a 200 proves the app was resolved either way.
async function probeManifest({
  baseUrl,
  channel,
  appId,
}: {
  baseUrl: string;
  channel: string;
  appId?: string;
}): Promise<{ ok: boolean; status: number; body: string }> {
  const headers: Record<string, string> = {
    'expo-platform': 'ios',
    'expo-runtime-version': 'eoas-doctor-probe',
    'expo-protocol-version': '1',
    'expo-channel-name': channel,
  };
  if (appId) {
    headers['expo-app-id'] = appId;
  }
  const response = await fetchWithRetries(`${baseUrl}/manifest`, { method: 'GET', headers });
  return {
    ok: response.ok,
    status: response.status,
    body: (await response.text()).trim(),
  };
}

export default class Doctor extends Command {
  static override args = {};
  static override description =
    "Check that this project's clients can reach the update server — including builds shipped before expo-app-id existed";
  static override examples = ['<%= config.bin %> <%= command.id %> --channel=production'];
  static override flags = {
    channel: Flags.string({
      description: 'Channel to probe with (must exist on the server)',
      required: true,
    }),
    url: Flags.string({
      description: 'Update server URL. Defaults to updates.url from your Expo config',
      required: false,
    }),
    appId: Flags.string({
      description:
        'App id to probe with. Defaults to updates.requestHeaders["expo-app-id"] from your Expo config',
      required: false,
    }),
  };

  public async run(): Promise<void> {
    const { flags } = await this.parse(Doctor);
    const projectDir = process.cwd();

    if (!isExpoInstalled(projectDir)) {
      Log.error('Expo is not installed in this project. Please install Expo first.');
      process.exit(1);
    }

    const privateConfig = await getPrivateExpoConfigAsync(projectDir, {
      env: process.env as Env,
    });
    const baseUrl = await resolveServerUrl(privateConfig, flags.url).catch(e => {
      Log.error(e.message);
      process.exit(1);
    });

    const appId = flags.appId ?? getExpoAppId(privateConfig);

    Log.log(`🩺 Probing ${baseUrl} on channel '${flags.channel}'`);
    Log.newLine();

    // Probe 1 — the fleet already in users' hands. Its binary predates
    // expo-app-id and cannot be made to send it without a store release, so
    // this is the probe that decides whether upgrading the server strands
    // those installs.
    const legacySpinner = ora('Probing as a v1 client (no expo-app-id header)...').start();
    const legacy = await probeManifest({ baseUrl, channel: flags.channel });
    if (legacy.ok) {
      legacySpinner.succeed('✅ v1 clients are served — the server falls back to its EXPO_APP_ID');
    } else {
      legacySpinner.fail(`❌ v1 clients are rejected — HTTP ${legacy.status}: ${legacy.body}`);
    }

    // Probe 2 — what a rebuilt binary sends. Skipped when the project has no
    // app id configured, which is itself the v1 shape and not an error.
    let modern: { ok: boolean; status: number; body: string } | undefined;
    if (appId) {
      const modernSpinner = ora(`Probing as a v2 client (expo-app-id: ${appId})...`).start();
      modern = await probeManifest({ baseUrl, channel: flags.channel, appId });
      if (modern.ok) {
        modernSpinner.succeed('✅ v2 clients are served');
      } else {
        modernSpinner.fail(`❌ v2 clients are rejected — HTTP ${modern.status}: ${modern.body}`);
      }
    } else {
      Log.warn(
        "⚠️  Skipping the v2 probe: no 'expo-app-id' in updates.requestHeaders. That is the v1 config shape — run 'npx eoas init' to add it."
      );
    }

    Log.newLine();

    if (!legacy.ok && !appId) {
      Log.error('Your v1 clients are being rejected and this project sends no app id at all.');
      Log.error(
        'Every install in the wild has lost OTA coverage. On the server, unset SKIP_LEGACY_APP_ID_FALLBACK to restore it immediately.'
      );
      process.exit(1);
    }
    if (!legacy.ok) {
      Log.warn('Clients built before expo-app-id existed are rejected by this server.');
      Log.warn(
        'That is correct only if every build your users run already ships the header. If any predate it, they have silently stopped updating — unset SKIP_LEGACY_APP_ID_FALLBACK on the server.'
      );
    }
    if (modern && !modern.ok) {
      Log.error(`The app id '${appId}' is not served by this server.`);
      Log.error(
        'Check it matches EXPO_APP_ID (single-app) or an app in the dashboard (control plane).'
      );
      process.exit(1);
    }
    if (legacy.ok && modern?.ok) {
      Log.succeed('Both v1 and v2 clients are served. This server is safe to cut over to.');
    }
  }
}
