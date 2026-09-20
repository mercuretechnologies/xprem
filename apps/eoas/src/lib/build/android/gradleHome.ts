import { createHash } from 'crypto';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';

import { BuildInputs } from '../prepare';
import { lockDirectory } from '../workspace';

// Restoring the journal replaces Gradle's usage history, so one build owns this home at a time.
export async function withGradleHome<T>(
  build: Pick<BuildInputs, 'endpoint' | 'options'>,
  work: (directory: string) => Promise<T>
): Promise<T> {
  const scope = createHash('sha256')
    .update(JSON.stringify([build.endpoint, build.options.profile]))
    .digest('hex');
  const directory = path.join(os.homedir(), '.eoas', '.gradle', scope);
  await fs.ensureDir(directory, { mode: 0o700 });
  const release = await lockDirectory(directory);
  try {
    return await work(directory);
  } finally {
    await release();
  }
}
