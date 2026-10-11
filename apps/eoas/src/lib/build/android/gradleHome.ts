import { createHash } from 'crypto';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';

import { BuildInputs } from '../prepare';
import { lockDirectory } from '../workspace';

// Gradle reads these from GRADLE_USER_HOME only.
const userConfiguration = ['gradle.properties', 'init.d'];

// withGradleHome runs work in a home owned by one server and profile, linked to the
// user's Gradle configuration. Restoring the journal replaces Gradle's usage history,
// so one build owns this home at a time.
export async function withGradleHome<T>(
  build: Pick<BuildInputs, 'endpoint' | 'options' | 'env'>,
  work: (directory: string) => Promise<T>
): Promise<T> {
  const scope = createHash('sha256')
    .update(JSON.stringify([build.endpoint, build.options.profile]))
    .digest('hex');
  const directory = path.join(os.homedir(), '.eoas', '.gradle', scope);
  await fs.ensureDir(directory, { mode: 0o700 });
  const release = await lockDirectory(directory);
  try {
    await linkUserConfiguration(
      build.env.GRADLE_USER_HOME ?? path.join(os.homedir(), '.gradle'),
      directory
    );
    return await work(directory);
  } finally {
    await release();
  }
}

async function linkUserConfiguration(userHome: string, directory: string): Promise<void> {
  for (const name of userConfiguration) {
    const link = path.join(directory, name);
    const target = path.join(userHome, name);
    await fs.remove(link);
    if (await fs.pathExists(target)) {
      await fs.symlink(target, link);
    }
  }
}
