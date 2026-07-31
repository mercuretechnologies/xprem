import { Args, Command } from '@oclif/core';
import chalk from 'chalk';
import fs from 'fs-extra';
import path from 'path';

import Log from '../../lib/log';
import {
  type ValidationIssue,
  parseEnvFile,
  validateEnvMap,
} from '../../lib/serverConfig/envCatalog';
import {
  extractSecretEnv,
  looksLikeChartValues,
  parseYamlFile,
  validateHelmPair,
} from '../../lib/serverConfig/helmValues';

type HelmDocs = {
  values?: Record<string, unknown>;
  secretEnv?: Record<string, string>;
};

/** A single document can carry either half of the pair, or both when merged. */
function classifyInto(docs: HelmDocs, doc: Record<string, unknown>): void {
  if (!docs.values && looksLikeChartValues(doc)) {
    docs.values = doc;
  }
  if (!docs.secretEnv) {
    docs.secretEnv = extractSecretEnv(doc);
  }
}

/** Picks the Helm pair out of a directory, canonical file names first. */
async function collectHelmDocs(dir: string, docs: HelmDocs, skip?: string): Promise<void> {
  const names = (await fs.readdir(dir))
    .filter(name => /\.ya?ml$/i.test(name))
    .sort((a, b) => {
      const canonical = (name: string): number =>
        name === 'values.yaml' || name === 'secrets.yaml' ? 0 : 1;
      return canonical(a) - canonical(b) || a.localeCompare(b);
    });
  for (const name of names) {
    const filePath = path.join(dir, name);
    if (filePath === skip || !(await fs.stat(filePath)).isFile()) {
      continue;
    }
    try {
      classifyInto(docs, parseYamlFile(await fs.readFile(filePath, 'utf8')));
    } catch {
      // Not a single YAML mapping (multi-doc, template output...): not ours.
    }
  }
}

export default class ServerValidate extends Command {
  static override args = {
    file: Args.string({
      description:
        'Path to a server .env file, a Helm values or secrets YAML, or the directory holding the Helm pair',
      required: true,
    }),
  };
  static override description =
    'Check a server configuration (.env file or Helm values/secrets pair) for missing or inconsistent variables';
  static override examples = [
    '<%= config.bin %> <%= command.id %> .env.xprem',
    '<%= config.bin %> <%= command.id %> xprem-helm',
    '<%= config.bin %> <%= command.id %> xprem-helm/values.yaml',
  ];
  static override flags = {};

  public async run(): Promise<void> {
    const { args } = await this.parse(ServerValidate);
    const target = path.resolve(process.cwd(), args.file);
    if (!(await fs.pathExists(target))) {
      Log.error(`File not found: ${target}`);
      process.exit(1);
    }

    let issues: ValidationIssue[];
    if ((await fs.stat(target)).isDirectory()) {
      const docs: HelmDocs = {};
      await collectHelmDocs(target, docs);
      if (!docs.values && !docs.secretEnv) {
        Log.error(`No Helm values or secrets YAML found in ${target}`);
        process.exit(1);
      }
      issues = validateHelmPair(docs.values, docs.secretEnv);
    } else if (/\.ya?ml$/i.test(target)) {
      let doc: Record<string, unknown>;
      try {
        doc = parseYamlFile(await fs.readFile(target, 'utf8'));
      } catch (e) {
        Log.error(`Could not parse ${args.file} as YAML: ${e instanceof Error ? e.message : e}`);
        process.exit(1);
        return;
      }
      const docs: HelmDocs = {};
      classifyInto(docs, doc);
      if (!docs.values && !docs.secretEnv) {
        Log.error(
          `${args.file} looks like neither a values file for the xprem Helm chart nor a secrets overlay (secretEnv map).`
        );
        process.exit(1);
      }
      // The other half of the pair is picked up from the same directory.
      await collectHelmDocs(path.dirname(target), docs, target);
      issues = validateHelmPair(docs.values, docs.secretEnv);
    } else {
      issues = validateEnvMap(parseEnvFile(await fs.readFile(target, 'utf8')));
    }

    const errors = issues.filter(issue => issue.level === 'error');
    const warnings = issues.filter(issue => issue.level === 'warning');
    for (const issue of errors) {
      Log.log(`${chalk.red('✖')} ${issue.message}`);
    }
    for (const issue of warnings) {
      Log.log(`${chalk.yellow('⚠')} ${issue.message}`);
    }
    Log.newLine();
    if (errors.length > 0) {
      Log.fail(
        `${args.file}: ${errors.length} error(s), ${warnings.length} warning(s). The server would not boot with this configuration.`
      );
      process.exit(1);
    }
    if (warnings.length > 0) {
      Log.succeed(`${args.file}: no errors, ${warnings.length} warning(s).`);
      return;
    }
    Log.succeed(`${args.file}: everything looks good.`);
  }
}
