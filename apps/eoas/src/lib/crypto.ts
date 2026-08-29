import { createHash } from 'crypto';
import fs from 'fs';

// Same encoding as internal/crypto.GetBase64URLEncoding: the string
// shapeManifestAsset puts on ManifestAsset.hash.
export function toBase64Url(buffer: Buffer): string {
  return buffer.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export async function hashFile(filePath: string): Promise<string> {
  const digest = await new Promise<Buffer>((resolve, reject) => {
    const file = fs.createReadStream(filePath).on('error', reject);
    const hash = file.pipe(createHash('sha256')).on('error', reject);
    hash.on('finish', () => {
      resolve(hash.read());
    });
  });
  return toBase64Url(digest);
}
