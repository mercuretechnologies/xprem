import { Command, Flags } from '@oclif/core';

import { buildAndroid } from '../../lib/build/android';
import { createBuildOutputRedactor, secretsToRedact } from '../../lib/build/errors';
import { buildIos } from '../../lib/build/ios';
import { selectProfile } from '../../lib/build/prepare';
import { BuildPlatform } from '../../lib/build/server';
import Log from '../../lib/log';

export default class Build extends Command {
  static override description =
    'Build a signed Android APK/AAB or iOS IPA locally using remote xprem credentials.\n\nEnvironment checking inspects app.config source (not its imported Node helpers) and application modules visited by Metro before Expo inlining (including workspace modules, excluding node_modules). Static dot/bracket accesses and destructuring are checked; dynamic or aliased process.env access requires --ignoreEnvCheck. Dead branches within visited modules may still require keys. Only direct process.env.EXPO_PUBLIC_X accesses are inlined by Expo (bracket/destructured reads are only checked for presence); other supplied variables remain config/tooling inputs. Debug APKs normally load JavaScript from Metro; their graph is validated with export --dev. Maintained Android projects retain their native Expo settings. Product flavors and split artifacts are not supported. iOS builds need macOS with Xcode and CocoaPods; app extensions are not supported.';
  static override flags = {
    profile: Flags.string({ char: 'e', required: true, description: 'Profile in xprem.json' }),
    platform: Flags.string({
      options: ['android', 'ios'],
      description: 'Platform to build; required when the profile has both android and ios',
    }),
    channel: Flags.string({ description: 'Override the profile channel/environment selection' }),
    envFile: Flags.string({ description: 'Dotenv file overriding individual server variables' }),
    ignoreEnvCheck: Flags.boolean({
      description: 'Bypass static environment checks (including unverifiable dynamic access)',
      default: false,
    }),
    serverUrl: Flags.string({ description: 'Override updates.url for the build server' }),
    appId: Flags.string({ description: 'Override expo-app-id for bootstrap config resolution' }),
    output: Flags.string({
      description:
        'Destination APK/AAB/IPA file (must not already exist; defaults to a unique name in build-artifacts)',
    }),
    packageRunner: Flags.string({ description: 'Package runner used for Expo commands' }),
    javaHome: Flags.string({
      description: 'JDK directory exported as JAVA_HOME for Gradle',
    }),
    androidSdk: Flags.string({
      description: 'Local Android SDK directory (overrides ANDROID_HOME and sdk.dir)',
    }),
    xcode: Flags.string({
      description:
        'Xcode version for an iOS build, such as 26 or 26.1 (defaults to .xcode-version, then a question when several are installed, then the selected Xcode)',
    }),
    verbose: Flags.boolean({
      description: 'Print every tool output line with its build phase (full logs are always saved)',
      default: false,
    }),
    stream: Flags.boolean({
      description: 'Stream build logs to xprem for live viewing in the dashboard',
      default: false,
    }),
    'remote-cache': Flags.boolean({
      description: 'Use xprem remote Gradle and C/C++ caches for Android builds',
      default: true,
      allowNo: true,
    }),
  };
  static override examples = [
    '<%= config.bin %> build --profile production --channel production --envFile .env.build',
  ];
  public async run(): Promise<void> {
    const { flags } = await this.parse(Build);
    const options = {
      profile: flags.profile,
      channel: flags.channel,
      envFile: flags.envFile,
      ignoreEnvCheck: flags.ignoreEnvCheck,
      serverUrl: flags.serverUrl,
      appId: flags.appId,
      output: flags.output,
      packageRunner: flags.packageRunner,
      verbose: flags.verbose,
      stream: flags.stream,
      remoteCache: flags['remote-cache'],
    };
    try {
      const platform = await resolvePlatform(flags.platform as BuildPlatform | undefined, options);
      if (platform === 'ios') {
        await buildIos(process.cwd(), { ...options, xcode: flags.xcode });
      } else {
        await buildAndroid(process.cwd(), {
          ...options,
          javaHome: flags.javaHome,
          androidSdk: flags.androidSdk,
        });
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Build failed.';
      Log.error(message);
      const cause = rootCause(error);
      if (cause && !message.includes(cause)) {
        Log.error(`Cause: ${createBuildOutputRedactor(secretsToRedact({}, []))(cause)}`);
      }
      this.exit(1);
    }
  }
}

// A profile with a single platform section needs no --platform.
async function resolvePlatform(
  requested: BuildPlatform | undefined,
  options: { profile: string }
): Promise<BuildPlatform> {
  if (requested) {
    return requested;
  }
  const profile = await selectProfile(process.cwd(), options);
  if (profile.android && profile.ios) {
    throw new Error('This profile has android and ios sections: pass --platform android or ios.');
  }
  return profile.ios ? 'ios' : 'android';
}

function rootCause(error: unknown): string | undefined {
  let cause: unknown;
  for (let current = error; current instanceof Error && current.cause; current = current.cause) {
    cause = current.cause;
  }
  return cause instanceof Error ? cause.message : undefined;
}
