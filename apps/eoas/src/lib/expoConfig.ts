// This file is copied from eas-cli[https://github.com/expo/eas-cli] to ensure consistent user experience across the CLI.
import { ExpoConfig, getConfig, getConfigFilePaths } from '@expo/config';
import { Env } from '@expo/eas-build-job';
import spawnAsync from '@expo/spawn-async';
import fs from 'fs-extra';
import Joi from 'joi';
import jscodeshift, { Collection } from 'jscodeshift';
import path from 'path';

import Log from './log';
import { isExpoInstalled } from './package';
import { resolvePackageRunner, splitPackageRunner } from './packageRunner';

export enum RequestedPlatform {
  Android = 'android',
  Ios = 'ios',
  All = 'all',
}

export type PublicExpoConfig = Omit<
  ExpoConfig,
  '_internal' | 'hooks' | 'ios' | 'android' | 'updates'
> & {
  ios?: Omit<ExpoConfig['ios'], 'config'>;
  android?: Omit<ExpoConfig['android'], 'config'>;
  updates?: Omit<ExpoConfig['updates'], 'codeSigningCertificate' | 'codeSigningMetadata'>;
};

export interface ExpoConfigOptions {
  env?: Env;
  skipSDKVersionRequirement?: boolean;
  skipPlugins?: boolean;
  packageRunner?: string;
}

interface ExpoConfigOptionsInternal extends ExpoConfigOptions {
  isPublicConfig?: boolean;
}

let wasExpoConfigWarnPrinted = false;

async function getExpoConfigInternalAsync(
  projectDir: string,
  opts: ExpoConfigOptionsInternal = {}
): Promise<ExpoConfig> {
  const originalProcessEnv: NodeJS.ProcessEnv = process.env;
  try {
    process.env = {
      ...process.env,
      ...opts.env,
    };

    let exp: ExpoConfig;
    if (isExpoInstalled(projectDir)) {
      const runner = resolvePackageRunner(opts.packageRunner, projectDir);
      const [runnerCommand, runnerArgs] = splitPackageRunner(runner);
      try {
        const { stdout } = await spawnAsync(
          runnerCommand,
          [
            ...runnerArgs,
            'expo',
            'config',
            '--json',
            ...(opts.isPublicConfig ? ['--type', 'public'] : []),
          ],

          {
            cwd: projectDir,
            env: {
              ...process.env,
              ...opts.env,
              EXPO_NO_DOTENV: '1',
            },
          }
        );
        exp = JSON.parse(stdout);
      } catch (err: any) {
        if (!wasExpoConfigWarnPrinted) {
          Log.warn(
            `Failed to read the app config from the project using "${runner} expo config" command: ${err.message}.`
          );
          Log.warn('Falling back to the version of "@expo/config" shipped with the EAS CLI.');
          wasExpoConfigWarnPrinted = true;
        }
        exp = getConfig(projectDir, {
          skipSDKVersionRequirement: true,
          ...(opts.isPublicConfig ? { isPublicConfig: true } : {}),
          ...(opts.skipPlugins ? { skipPlugins: true } : {}),
        }).exp;
      }
    } else {
      exp = getConfig(projectDir, {
        skipSDKVersionRequirement: true,
        ...(opts.isPublicConfig ? { isPublicConfig: true } : {}),
        ...(opts.skipPlugins ? { skipPlugins: true } : {}),
      }).exp;
    }

    const { error } = MinimalAppConfigSchema.validate(exp, {
      allowUnknown: true,
      abortEarly: true,
    });
    if (error) {
      throw new Error(`Invalid app config.\n${error.message}`);
    }
    return exp;
  } finally {
    process.env = originalProcessEnv;
  }
}

const MinimalAppConfigSchema = Joi.object({
  slug: Joi.string().required(),
  name: Joi.string().required(),
  version: Joi.string(),
  android: Joi.object({
    versionCode: Joi.number().integer(),
  }),
  ios: Joi.object({
    buildNumber: Joi.string(),
  }),
});

export async function getPrivateExpoConfigAsync(
  projectDir: string,
  opts: ExpoConfigOptions = {}
): Promise<ExpoConfig> {
  ensureExpoConfigExists(projectDir);
  return await getExpoConfigInternalAsync(projectDir, { ...opts, isPublicConfig: false });
}

export function ensureExpoConfigExists(projectDir: string): void {
  const paths = getConfigFilePaths(projectDir);
  if (!paths?.staticConfigPath && !paths?.dynamicConfigPath) {
    // eslint-disable-next-line node/no-sync
    fs.writeFileSync(path.join(projectDir, 'app.json'), JSON.stringify({ expo: {} }, null, 2));
  }
}

export function isUsingStaticExpoConfig(projectDir: string): boolean {
  const paths = getConfigFilePaths(projectDir);
  return !!(paths.staticConfigPath?.endsWith('app.json') && !paths.dynamicConfigPath);
}

export async function getPublicExpoConfigAsync(
  projectDir: string,
  opts: ExpoConfigOptions = {}
): Promise<PublicExpoConfig> {
  ensureExpoConfigExists(projectDir);

  return await getExpoConfigInternalAsync(projectDir, { ...opts, isPublicConfig: true });
}

export function getExpoConfigUpdateUrl(config: ExpoConfig): string | undefined {
  return config.updates?.url;
}

// getExpoAppId reads the app id without treating its absence as fatal. A config
// with no 'expo-app-id' is the shape every v1 project has, so a caller that
// diagnoses or migrates such a project needs to see the absence rather than be
// exited on. Commands that cannot proceed without an id use requireExpoAppId.
export function getExpoAppId(config: ExpoConfig): string | undefined {
  return (config.updates as { requestHeaders?: Record<string, string> } | undefined)
    ?.requestHeaders?.['expo-app-id'];
}

export function requireExpoAppId(config: ExpoConfig): string {
  const appId = getExpoAppId(config);
  if (!appId) {
    Log.error("Your Expo config is missing the 'expo-app-id' entry in updates.requestHeaders.");
    Log.error(
      "This usually means you're running eoas v2+ against a v1-style single-app config or your config is missing the 'expo-app-id' entry."
    );
    Log.error(
      "Fix: run 'npx eoas init' to migrate, or pin to the previous CLI via 'npx eoas@1 ...'."
    );
    process.exit(1);
  }
  return appId;
}

// exp is a config fragment. String values starting with 'process.env.' are
// emitted as raw JavaScript expressions rather than string literals, so callers
// can write env-dependent values like
// "process.env.DISABLE_CODE_SIGNING ? undefined : './certs/certificate.pem'".
export async function createOrModifyExpoConfigAsync(
  projectDir: string,
  exp: Record<string, any>
): Promise<void> {
  try {
    ensureExpoConfigExists(projectDir);
    const configPathJS = path.join(projectDir, 'app.config.js');
    const configPathTS = path.join(projectDir, 'app.config.ts');

    // eslint-disable-next-line node/no-sync
    const hasJsConfig = fs.existsSync(configPathJS);

    if (isUsingStaticExpoConfig(projectDir)) {
      Log.withInfo(
        'You are using a static app config. We will create a dynamic config file for you.'
      );

      const newConfigContent = `export default ({ config }) => ({
                                ...config,
                                ...${stringifyWithEnv(exp)}
                              });`;
      // eslint-disable-next-line node/no-sync
      fs.writeFileSync(configPathJS, newConfigContent);
    } else {
      const configPath = hasJsConfig ? configPathJS : configPathTS;
      // eslint-disable-next-line node/no-sync
      const existingCode = fs.readFileSync(configPath, 'utf8');
      const j = configPath.endsWith('.ts') ? jscodeshift.withParser('ts') : jscodeshift;
      const ast: Collection = j(existingCode);

      if (!updateExportedConfigObject(j, ast, exp)) {
        throw new Error(
          `Could not find the exported config object in ${path.basename(configPath)}.`
        );
      }
      const updatedCode = ast.toSource({
        quote: 'auto',
        trailingComma: true,
        reuseWhitespace: true,
      });

      // eslint-disable-next-line node/no-sync
      fs.writeFileSync(configPath, updatedCode);
    }
  } catch (e) {
    Log.withInfo('An error occurred while updating the Expo config. Please update it manually.');
    Log.newLine();
    Log.warn('Please modify your app.config.ts file manually by adding the following code:');
    Log.newLine();
    Log.withInfo(`${stringifyWithEnv(exp)}`);
    Log.newLine();
    throw e;
  }
}

// Finds the object literal the dynamic config exports (export default or
// module.exports; a function returning it, with expression or block body;
// optional 'as' casts) and merges exp into it. Returns false when no
// recognizable shape is found, so the caller can fail loudly instead of
// writing the file back unchanged.
function updateExportedConfigObject(
  j: typeof jscodeshift,
  ast: Collection,
  exp: Record<string, any>
): boolean {
  const exportedNodes: any[] = [];
  ast.find(j.ExportDefaultDeclaration).forEach(p => exportedNodes.push(p.value.declaration));
  ast
    .find(j.AssignmentExpression, {
      left: { object: { name: 'module' }, property: { name: 'exports' } },
    })
    .forEach(p => exportedNodes.push(p.value.right));

  for (const exportedNode of exportedNodes) {
    const configObject = resolveConfigObject(j, exportedNode);
    if (configObject) {
      updateObjectExpression(j, configObject, exp);
      return true;
    }
  }
  return false;
}

function resolveConfigObject(j: typeof jscodeshift, exportedNode: any): any {
  let node = unwrapExpression(exportedNode);
  if (
    j.ArrowFunctionExpression.check(node) ||
    j.FunctionExpression.check(node) ||
    j.FunctionDeclaration.check(node)
  ) {
    if (j.BlockStatement.check(node.body)) {
      const returnStatement: any = node.body.body.find((statement: any) =>
        j.ReturnStatement.check(statement)
      );
      node = returnStatement?.argument ?? null;
    } else {
      node = node.body;
    }
    node = node && unwrapExpression(node);
  }
  return node && j.ObjectExpression.check(node) ? node : null;
}

// Strips TS casts and parentheses: `({ ... } as ExpoConfig)` -> the object.
function unwrapExpression(node: any): any {
  let current = node;
  while (
    current &&
    (current.type === 'TSAsExpression' ||
      current.type === 'TSSatisfiesExpression' ||
      current.type === 'ParenthesizedExpression')
  ) {
    current = current.expression;
  }
  return current;
}

function updateObjectExpression(
  j: typeof jscodeshift,
  configObject: ReturnType<typeof j.objectExpression>,
  updates: Record<string, any>
): void {
  Object.entries(updates).forEach(([key, value]) => {
    // The default parser produces 'Property' nodes, the ts parser 'ObjectProperty'.
    const existingProperty = configObject.properties.find((prop: any) => {
      if (prop.type !== 'Property' && prop.type !== 'ObjectProperty') {
        return false;
      }
      return (
        (prop.key.type === 'Identifier' && prop.key.name === key) ||
        ((prop.key.type === 'StringLiteral' || prop.key.type === 'Literal') &&
          prop.key.value === key)
      );
    });

    if (existingProperty) {
      configObject.properties = configObject.properties.filter(prop => prop !== existingProperty);
    }

    const newProperty = j.objectProperty(j.identifier(key), createValueNode(j, value));

    configObject.properties.push(newProperty);
  });
}

function createValueNode(j: typeof jscodeshift, value: any): any {
  if (typeof value === 'string' && value.startsWith('process.env.')) {
    if (/^process\.env\.\w+$/.test(value)) {
      return j.memberExpression(
        j.memberExpression(j.identifier('process'), j.identifier('env')),
        j.identifier(value.split('.')[2])
      );
    }
    return parseExpressionNode(j, value);
  }

  if (typeof value === 'object' && value !== null) {
    return j.objectExpression(
      Object.entries(value).map(
        ([key, val]) => j.objectProperty(j.stringLiteral(key), createValueNode(j, val)) // Force stringLiteral pour garder les guillemets
      )
    );
  }

  return j.literal(value);
}

function parseExpressionNode(j: typeof jscodeshift, code: string): any {
  const statement = j(`(${code});`).find(j.ExpressionStatement).nodes()[0];
  return statement.expression;
}

// Raw expressions are swapped for placeholders before JSON.stringify and
// spliced back verbatim afterwards, so JSON escaping never mangles their
// contents (backslashes in Windows paths, quotes, ...).
function stringifyWithEnv(obj: Record<string, any>): string {
  const rawExpressions: string[] = [];
  const json = JSON.stringify(
    obj,
    (_key, value) =>
      typeof value === 'string' && value.startsWith('process.env.')
        ? `__RAW_EXPR_${rawExpressions.push(value) - 1}__`
        : value,
    2
  );
  return json.replace(/"__RAW_EXPR_(\d+)__"/g, (_match, index) => rawExpressions[Number(index)]);
}

// customServerUrl wins over the Expo config, so a project serving updates and
// receiving publishes on two different origins can point the CLI at the second
// one without touching its config.
export async function resolveServerUrl(
  config: ExpoConfig,
  customServerUrl?: string
): Promise<string> {
  const updateUrl = customServerUrl ?? getExpoConfigUpdateUrl(config);
  if (!updateUrl) {
    throw new Error(
      "Update url is not setup in your config. Please run 'eoas init' to setup the update url, or pass --serverUrl"
    );
  }
  try {
    return new URL(updateUrl).origin;
  } catch {
    throw new Error(customServerUrl ? 'Invalid --serverUrl value.' : 'Invalid update URL.');
  }
}
