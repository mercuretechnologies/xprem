import fs from 'fs-extra';
import path from 'path';

export const UPDATE_METADATA_FILE = 'xprem-update-metadata.json';

// PublishedUpdateMetadata is one platform's entry in UPDATE_METADATA_FILE. id is
// the manifest id, the value expo-updates exposes as Updates.updateId.
export interface PublishedUpdateMetadata {
  id: string;
  platform: string;
  runtimeVersion: string;
  branch: string;
  group?: string;
  message?: string;
}

// readUpdateUUID reads the manifest id from a markUpdateAsUploaded response.
// Undefined when the server is too old to send one.
export async function readUpdateUUID(response: {
  text(): Promise<string>;
}): Promise<string | undefined> {
  try {
    const { updateUUID } = JSON.parse(await response.text());
    return typeof updateUUID === 'string' && updateUUID ? updateUUID : undefined;
  } catch {
    return undefined;
  }
}

export async function writeUpdateMetadata(
  exportDir: string,
  updates: PublishedUpdateMetadata[]
): Promise<void> {
  await fs.writeJson(path.join(exportDir, UPDATE_METADATA_FILE), updates, { spaces: 2 });
}
