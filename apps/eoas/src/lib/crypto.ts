import { createHash } from 'crypto';
import fs from 'fs';

// Same encoding as internal/crypto.GetBase64URLEncoding: the string
// shapeManifestAsset puts on ManifestAsset.hash.
export function toBase64Url(buffer: Buffer): string {
  return buffer.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export interface FileDigest {
  // ManifestAsset.hash, and the object key under {appId}/cas/.
  hash: string;
  // ManifestAsset.key: the md5 expo-updates uses as its on-device cache key.
  key: string;
}

export async function digestFile(filePath: string): Promise<FileDigest> {
  const sha256 = createHash('sha256');
  const md5 = createHash('md5');
  await new Promise<void>((resolve, reject) => {
    fs.createReadStream(filePath)
      .on('error', reject)
      .on('data', chunk => {
        sha256.update(chunk);
        md5.update(chunk);
      })
      .on('end', resolve);
  });
  return { hash: toBase64Url(sha256.digest()), key: md5.digest('hex') };
}
