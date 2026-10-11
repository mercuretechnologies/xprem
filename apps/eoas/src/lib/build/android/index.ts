import { AndroidConfig } from '@expo/config-plugins';
import fg from 'fast-glob';
import fs from 'fs-extra';
import path from 'path';

import { AndroidCache, restoreAndroidCache } from './cache';
import { withGradleHome } from './gradleHome';
import { BuildStep } from '../steps';
import { logGradleProfile } from './gradleProfile';
import { runPostInstallHook } from '../hooks';
import { AndroidToolsOptions, configureAndroidSdk, resolveAndroidTools } from './tools';
import { AndroidProfile } from '../../buildConfig/types';
import Log from '../../log';
import { secretsToRedact } from '../errors';
import { BuildLog, withBuildLog } from '../log';
import { NativeBuild, prepareNativeBuild, runNativeBuild } from '../native';
import { BuildInputs, BuildOptions, platformProfile } from '../prepare';
import { BuildCommand, runBuildCommand } from '../run';
import { fetchCredentials } from '../server';
import { copyTemplate, withTemporaryDirectory } from '../workspace';

export type AndroidBuildOptions = BuildOptions & AndroidToolsOptions;

export interface AndroidCredentials {
  keystore: string;
  keystorePassword: string;
  keyAlias: string;
  keyPassword: string;
}

interface AndroidBuild extends BuildInputs {
  options: AndroidBuildOptions;
  credentials: AndroidCredentials;
  android: AndroidProfile;
}

const MAX_VERSION_CODE = 2100000000;

export async function buildAndroid(project: string, options: AndroidBuildOptions): Promise<string> {
  return await withBuildLog(
    project,
    options.profile,
    'Android build',
    async buildLog => {
      const build = await prepareBuild(project, options, buildLog);
      const secrets = secretsToRedact(build.variables, [
        build.credentials.keystore,
        build.credentials.keystorePassword,
        build.credentials.keyPassword,
      ]);
      buildLog.maskSecrets(secrets);
      const run = (): Promise<string> =>
        withTemporaryDirectory(
          buildLog,
          temporary => runNativeBuild(build, androidBuild(build), temporary, buildLog, secrets),
          options.remoteCache === false ? undefined : project
        );
      if (options.remoteCache === false) {
        buildLog.general.info('Remote cache disabled. Using the local Gradle configuration.');
        return await run();
      }
      return await withGradleHome(build, async directory => {
        build.env = { ...build.env, GRADLE_USER_HOME: directory };
        buildLog.general.info(`Gradle home: ${directory}`);
        return await run();
      });
    },
    options.verbose || Log.isDebug
  );
}

async function prepareBuild(
  project: string,
  options: AndroidBuildOptions,
  buildLog: BuildLog
): Promise<AndroidBuild> {
  const inputs = await prepareNativeBuild<AndroidCredentials>(project, options, buildLog, {
    platform: 'android',
    toolsTitle: BuildStep.CHECK_ANDROID_TOOLS,
    credentialsTitle: BuildStep.FETCH_ANDROID_CREDENTIALS,
    resolveTools: async (local, stepLog, recordTool) => {
      const tools = await resolveAndroidTools(
        project,
        options,
        { ...process.env, ...local },
        message => {
          stepLog.info(message);
        },
        recordTool
      );
      stepLog.info(`Android SDK: ${tools.ANDROID_HOME}`);
      stepLog.info(`JAVA_HOME: ${tools.JAVA_HOME}`);
      return tools;
    },
    describe: profile => {
      const { applicationId, mode, artifact } = platformProfile(profile, 'android');
      return { applicationId, mode, extension: artifact };
    },
    fetchCredentials: endpoint =>
      fetchCredentials<AndroidCredentials>(endpoint, 'android', [
        'keystore',
        'keystorePassword',
        'keyAlias',
        'keyPassword',
      ]),
  });
  return { ...inputs, options, android: platformProfile(inputs.profile, 'android') };
}

function androidBuild(build: AndroidBuild): NativeBuild {
  const { applicationId, artifact, developmentClient, mode } = build.android;
  let cache: AndroidCache | undefined;
  return {
    platform: 'android',
    displayName: 'Android',
    artifactType: artifact,
    mode,
    developmentClient,
    buildNumberName: 'versionCode',
    maintainedProjectNotice:
      'Using maintained Android project. Native settings are retained; package, versionCode, signing and the expo-updates configuration are overridden in the temporary copy.',
    withIdentity: (expo, versionCode) => {
      if (versionCode > MAX_VERSION_CODE) {
        throw new Error('Allocated build number exceeds the Android versionCode limit.');
      }
      return { ...expo, android: { ...expo.android, package: applicationId, versionCode } };
    },
    restoreCache: async ({ temporary, buildLog }) => {
      if (build.options.remoteCache === false) {
        return;
      }
      cache = await buildLog.runStep(BuildStep.RESTORE_BUILD_CACHE, stepLog =>
        restoreAndroidCache(build, temporary, stepLog)
      );
    },
    saveCache: async ({ buildLog }) => {
      if (cache) {
        await buildLog.runStep(BuildStep.SAVE_BUILD_CACHE, stepLog => cache!.save(stepLog));
      }
    },
    compile: async ({ working, temporary, buildNumber, buildLog, secrets }) => {
      await runPostInstallHook(build, working, buildLog, secrets);
      const signing = await buildLog.runStep(BuildStep.CONFIGURE_ANDROID_SIGNING, async () => {
        const keystore = await writeKeystore(build.credentials, temporary);
        await configureAndroidSdk(working, build.toolEnv.ANDROID_HOME);
        const signingFile = await configureSigning(
          build,
          working,
          temporary,
          keystore,
          buildNumber
        );
        await prepareGradlew(working);
        return signingFile;
      });
      await buildLog.runStep(
        artifact === 'apk' ? BuildStep.BUILD_APK : BuildStep.BUILD_AAB,
        async stepLog => {
          const command = gradleCommand(build, working, signing);
          if (cache) {
            command.args.push(
              '--profile',
              '--gradle-user-home',
              build.env.GRADLE_USER_HOME!,
              ...cache.args
            );
            command.env = { ...command.env, ...cache.env };
          }
          try {
            await runBuildCommand(command, stepLog, secrets);
          } finally {
            await cache?.report(stepLog);
          }
        }
      );
      if (cache) {
        await buildLog.runStep(BuildStep.GRADLE_BUILD_PROFILE, stepLog =>
          logGradleProfile(path.join(working, 'android'), stepLog)
        );
      }
    },
    findArtifact: async ({ working }) => {
      const candidates = await fg(
        `android/app/build/outputs/${artifact === 'aab' ? 'bundle' : 'apk'}/**/*.${artifact}`,
        { cwd: working, absolute: true }
      );
      const matching = candidates.filter(file => file.toLowerCase().includes(mode));
      if (matching.length !== 1) {
        throw new Error(
          'Expected one build artifact; split APKs/product flavors are not supported.'
        );
      }
      return matching[0];
    },
  };
}

// The server validated the keystore, alias and passwords when they were stored.
async function writeKeystore(credentials: AndroidCredentials, temporary: string): Promise<string> {
  const keystore = path.join(temporary, 'keystore');
  await fs.writeFile(keystore, Buffer.from(credentials.keystore, 'base64'), { mode: 0o600 });
  return keystore;
}

// Gradle reads signing.json through EOAS_SIGNING_FILE; the script itself holds no secrets.
async function configureSigning(
  build: AndroidBuild,
  working: string,
  temporary: string,
  keystore: string,
  versionCode: number
): Promise<string> {
  const signing = path.join(temporary, 'signing.json');
  await fs.writeJson(
    signing,
    {
      ...build.credentials,
      keystore,
      applicationId: build.android.applicationId,
      versionCode,
    },
    { mode: 0o600 }
  );
  const buildGradle = AndroidConfig.Paths.getAppBuildGradleFilePath(working);
  await copyTemplate('gradle/eoas.gradle', path.join(path.dirname(buildGradle), 'eoas.gradle'));
  await fs.appendFile(
    buildGradle,
    buildGradle.endsWith('.kts')
      ? '\napply(from = "eoas.gradle")\n'
      : '\napply from: "eoas.gradle"\n'
  );
  return signing;
}

// Checkouts made on Windows or unpacked from archives lose the executable bit
// and may carry CRLF line endings. A missing wrapper is left for spawn to report.
async function prepareGradlew(working: string): Promise<void> {
  const gradlew = path.join(working, 'android/gradlew');
  if (!(await fs.pathExists(gradlew))) {
    return;
  }
  const script = await fs.readFile(gradlew, 'utf8');
  if (script.includes('\r')) {
    await fs.writeFile(gradlew, script.replace(/\r\n/g, '\n'));
  }
  await fs.chmod(gradlew, 0o755);
}

function gradleCommand(build: AndroidBuild, working: string, signing: string): BuildCommand {
  const { artifact, mode } = build.android;
  const task = `${artifact === 'aab' ? 'bundle' : 'assemble'}${
    mode === 'release' ? 'Release' : 'Debug'
  }`;
  return {
    title: `Building signed ${artifact.toUpperCase()}`,
    command: path.join(working, 'android/gradlew'),
    args: [`:app:${task}`, '--no-daemon', '--console=plain'],
    cwd: path.join(working, 'android'),
    env: { ...build.env, EOAS_SIGNING_FILE: signing, LC_ALL: 'C.UTF-8' },
  };
}
