// This file is partially copied from eas-cli[https://github.com/expo/eas-cli] to ensure consistent user experience across the CLI.
import { Platform } from '@expo/config';
import fs from 'fs-extra';
import Joi from 'joi';
import path from 'path';

import { Credentials, getAuthHeaders } from './auth';
import { RequestedPlatform } from './expoConfig';
import { fetchWithRetries } from './fetch';
import Log from './log';

const fileMetadataJoi = Joi.object({
  assets: Joi.array()
    .required()
    .items(Joi.object({ path: Joi.string().required(), ext: Joi.string().required() })),
  bundle: Joi.string().required(),
}).optional();
export const MetadataJoi = Joi.object({
  version: Joi.number().required(),
  bundler: Joi.string().required(),
  fileMetadata: Joi.object({
    android: fileMetadataJoi,
    ios: fileMetadataJoi,
    web: fileMetadataJoi,
  }).required(),
}).required();

type Metadata = {
  version: number;
  bundler: 'metro';
  fileMetadata: {
    [key in Platform]: { assets: { path: string; ext: string }[]; bundle: string };
  };
};

export interface AssetToUpload {
  path: string;
  name: string;
  ext: string;
}

function loadMetadata(distRoot: string): Metadata {
  // eslint-disable-next-line
  const fileContent = fs.readFileSync(path.join(distRoot, 'metadata.json'), 'utf8');
  let metadata: Metadata;
  try {
    metadata = JSON.parse(fileContent);
  } catch (e: any) {
    Log.error(`Failed to read metadata.json: ${e.message}`);
    throw e;
  }
  const { error } = MetadataJoi.validate(metadata);
  if (error) {
    throw error;
  }
  // Check version and bundler by hand (instead of with Joi) so
  // more informative error messages can be returned.
  if (metadata.version !== 0) {
    throw new Error('Only bundles with metadata version 0 are supported');
  }
  if (metadata.bundler !== 'metro') {
    throw new Error('Only bundles created with Metro are currently supported');
  }
  const platforms = Object.keys(metadata.fileMetadata);
  if (platforms.length === 0) {
    Log.warn('No updates were exported for any platform');
  }
  Log.debug(`Loaded ${platforms.length} platform(s): ${platforms.join(', ')}`);
  return metadata;
}

export function computeFilesRequests(
  projectDir: string,
  outputDir: string,
  requestedPlatform: RequestedPlatform
): AssetToUpload[] {
  const metadata = loadMetadata(path.join(projectDir, outputDir));
  const assets: AssetToUpload[] = [
    { path: 'metadata.json', name: 'metadata.json', ext: 'json' },
    { path: 'expoConfig.json', name: 'expoConfig.json', ext: 'json' },
  ];
  for (const platform of Object.keys(metadata.fileMetadata) as Platform[]) {
    if (requestedPlatform !== RequestedPlatform.All && requestedPlatform !== platform) {
      continue;
    }
    const bundle = metadata.fileMetadata[platform].bundle;
    assets.push({ path: bundle, name: path.basename(bundle), ext: 'hbc' });
    for (const asset of metadata.fileMetadata[platform].assets) {
      assets.push({ path: asset.path, name: path.basename(asset.path), ext: asset.ext });
    }
  }
  return assets;
}

export interface RequestUploadUrlItem {
  requestUploadUrl: string;
  fileName: string;
  filePath: string;
  // Extra headers the server requires on the PUT to requestUploadUrl
  // (e.g. x-ms-blob-type for Azure Blob Storage). Absent on older servers.
  headers?: Record<string, string>;
}

export interface RequestUploadUrlsResponse {
  uploadRequests: RequestUploadUrlItem[];
  updateId: string;
  rolloutPercentage?: number;
  publishGroup?: string;
}

// The server dictates which local files the CLI opens and where their bytes are
// sent, so its answer is untrusted input: a hostile or compromised server that
// gets a forged filePath past us reads any file it wants off the developer or CI
// machine. Every field is typed here, and the paths are checked against the
// export manifest in resolveUploadRequests before anything is opened.
const uploadRequestHeadersJoi = Joi.object()
  .pattern(
    // RFC 7230 header field-name, and a value that cannot smuggle a CRLF.
    /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/,
    Joi.string()
      .allow('')
      .pattern(/^[^\r\n]*$/)
  )
  .optional();

const uploadRequestJoi = Joi.object({
  requestUploadUrl: Joi.string()
    .uri({ scheme: ['http', 'https'] })
    .required(),
  fileName: Joi.string().required(),
  filePath: Joi.string().required(),
  headers: uploadRequestHeadersJoi,
  // Unknown keys are tolerated so a newer server can add fields without
  // breaking older CLIs; nothing reads them.
}).unknown(true);

export const RequestUploadUrlsResponseJoi = Joi.object({
  // The server marshals updateId from an int64, so it arrives as a JSON number;
  // older or third-party servers may send it as a string. Normalized to a string
  // below, which is what the markUpdateAsUploaded query parameter needs.
  updateId: Joi.alternatives().try(Joi.string(), Joi.number()).required(),
  uploadRequests: Joi.array().items(uploadRequestJoi).required(),
  rolloutPercentage: Joi.number().optional(),
  publishGroup: Joi.string().optional(),
})
  .required()
  .unknown(true);

function isLoopbackHost(hostname: string): boolean {
  const host = hostname.replace(/^\[/, '').replace(/\]$/, '').toLowerCase();
  // Deliberately does not accept *.localhost: resolvers are free to answer it
  // from DNS, which would let a remote host claim the plain-HTTP exemption.
  return host === 'localhost' || host === '::1' || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host);
}

// Escape hatch for the deployments that legitimately serve upload URLs over
// plain HTTP on a non-loopback host: a MinIO or S3-compatible endpoint set
// through AWS_BASE_ENDPOINT, or a local bucket whose BASE_URL is an internal
// hostname. It only lifts the transport requirement, never a path check.
const INSECURE_UPLOAD_URLS_ENV = 'EOAS_ALLOW_INSECURE_UPLOAD_URLS';

function insecureUploadUrlsAllowed(): boolean {
  const value = process.env[INSECURE_UPLOAD_URLS_ENV];
  return value === '1' || value?.toLowerCase() === 'true';
}

function assertSafeUploadUrl(requestUploadUrl: string): void {
  let url: URL;
  try {
    url = new URL(requestUploadUrl);
  } catch {
    throw new Error(`The server returned an unusable upload URL: ${requestUploadUrl}`);
  }
  if (url.protocol === 'https:') {
    return;
  }
  if (url.protocol === 'http:' && isLoopbackHost(url.hostname)) {
    return;
  }
  if (url.protocol === 'http:' && insecureUploadUrlsAllowed()) {
    Log.warn(
      `Uploading to ${url.origin} over plain HTTP because ${INSECURE_UPLOAD_URLS_ENV} is set. Your update artifacts travel unencrypted.`
    );
    return;
  }
  throw new Error(
    `Refusing to upload to ${url.origin}: update artifacts may only be sent over HTTPS (plain HTTP is allowed for loopback addresses only). Set ${INSECURE_UPLOAD_URLS_ENV}=1 if your storage endpoint is intentionally served over HTTP.`
  );
}

function assertRelativePathShape(filePath: string): void {
  if (!filePath || filePath.includes('\0')) {
    throw new Error('The server returned an empty or malformed file path.');
  }
  // win32.isAbsolute also covers the posix cases, drive letters and UNC paths.
  if (path.isAbsolute(filePath) || path.win32.isAbsolute(filePath)) {
    throw new Error(`Refusing to upload the absolute path "${filePath}" requested by the server.`);
  }
  if (filePath.split(/[/\\]/).some(segment => segment === '..')) {
    throw new Error(
      `Refusing to upload "${filePath}": the server requested a path outside the export directory.`
    );
  }
}

export interface ResolvedUploadRequest {
  item: RequestUploadUrlItem;
  // Canonical, containment-checked absolute path. The only path the caller may open.
  absolutePath: string;
  manifestEntry: AssetToUpload;
}

/**
 * Maps every upload request of ONE server response onto a file the CLI itself
 * exported, and throws on anything it cannot account for. This runs to
 * completion before the first byte is read: a single bad entry aborts the whole
 * publish rather than uploading the files that happened to be fine.
 *
 * Call it once per response: `eoas publish --platform all` requests upload URLs
 * per runtime version, and every response legitimately names the same files.
 */
export async function resolveUploadRequests({
  uploadRequests,
  exportDir,
  manifest,
}: {
  uploadRequests: RequestUploadUrlItem[];
  exportDir: string;
  manifest: AssetToUpload[];
}): Promise<ResolvedUploadRequest[]> {
  const manifestByPath = new Map(manifest.map(entry => [entry.path, entry]));
  // Canonical root, so a symlinked export directory (or /tmp on macOS) does not
  // make every containment check fail below.
  let exportRoot: string;
  try {
    exportRoot = await fs.realpath(exportDir);
  } catch {
    throw new Error(`Export directory ${exportDir} could not be resolved.`);
  }

  const seen = new Set<string>();
  const resolved: ResolvedUploadRequest[] = [];
  for (const item of uploadRequests) {
    assertSafeUploadUrl(item.requestUploadUrl);
    assertRelativePathShape(item.filePath);

    const manifestEntry = manifestByPath.get(item.filePath);
    if (!manifestEntry) {
      throw new Error(
        `Refusing to upload "${item.filePath}": the server asked for a file that is not part of this export.`
      );
    }
    if (item.fileName !== path.basename(item.filePath)) {
      throw new Error(
        `Refusing to upload "${item.filePath}": the server returned the mismatched name "${item.fileName}".`
      );
    }
    if (seen.has(item.filePath)) {
      throw new Error(`The server requested "${item.filePath}" more than once.`);
    }
    seen.add(item.filePath);

    const absolutePath = path.resolve(exportRoot, item.filePath);
    // Unreachable on POSIX: a path with no '..' segment and no leading separator
    // cannot resolve out of the root. Kept for the Windows drive-relative case
    // ("C:file" when the export root sits on another drive) and as a backstop if
    // the checks above are ever relaxed.
    if (absolutePath !== exportRoot && !absolutePath.startsWith(exportRoot + path.sep)) {
      throw new Error(
        `Refusing to upload "${item.filePath}": it resolves outside the export directory.`
      );
    }
    let realPath: string;
    try {
      realPath = await fs.realpath(absolutePath);
    } catch {
      throw new Error(`File ${item.filePath} not found in the export directory.`);
    }
    // The root is already canonical, so any difference here means a symlink was
    // traversed, either as the file itself or as one of its parent directories.
    if (realPath !== absolutePath) {
      throw new Error(`Refusing to upload "${item.filePath}": it is or goes through a symlink.`);
    }
    if (!(await fs.lstat(absolutePath)).isFile()) {
      throw new Error(`Refusing to upload "${item.filePath}": it is not a regular file.`);
    }

    resolved.push({ item, absolutePath, manifestEntry });
  }
  return resolved;
}

export function activeRolloutConflictMessage(branch: string): string {
  return `A progressive rollout is already active for branch "${branch}" on this runtime version. End or revert it from the dashboard before publishing a new update.`;
}

export async function requestUploadUrls({
  body,
  requestUploadUrl,
  auth,
  runtimeVersion,
  platform,
  commitHash,
  message,
  rolloutPercentage,
  publishGroup,
  branch,
}: {
  body: { fileNames: string[] };
  requestUploadUrl: string;
  auth: Credentials;
  runtimeVersion: string;
  platform: string;
  commitHash?: string;
  message?: string;
  rolloutPercentage?: number;
  // One UUID minted per publish run and shared by every platform call, so the
  // server can group the resulting updates as a single publish. Control plane
  // servers echo it back; a missing echo means the rows were not grouped.
  publishGroup?: string;
  branch: string;
}): Promise<RequestUploadUrlsResponse> {
  const uploadUrl = new URL(requestUploadUrl);
  uploadUrl.searchParams.set('runtimeVersion', runtimeVersion);
  uploadUrl.searchParams.set('platform', platform);
  uploadUrl.searchParams.set('commitHash', commitHash ?? '');
  if (rolloutPercentage !== undefined) {
    uploadUrl.searchParams.set('rolloutPercentage', String(rolloutPercentage));
  }
  if (publishGroup) {
    uploadUrl.searchParams.set('publishGroup', publishGroup);
  }

  const requestBody: { fileNames: string[]; message?: string } = { ...body };
  if (message) {
    requestBody.message = message;
  }

  const response = await fetchWithRetries(uploadUrl.toString(), {
    method: 'POST',
    headers: {
      ...getAuthHeaders(auth),
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(requestBody),
  });
  if (response.status === 409) {
    throw new Error(activeRolloutConflictMessage(branch));
  }
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`Failed to request upload URL: ${text}`);
  }
  const json = await response.json();
  // Joi's sanitized value, not the raw payload: it is the object the schema
  // actually vouched for, with inherited keys such as __proto__ dropped.
  const { error, value } = RequestUploadUrlsResponseJoi.validate(json);
  if (error) {
    throw new Error(`The server returned an invalid upload response: ${error.message}`);
  }
  const validated: RequestUploadUrlsResponse = { ...value, updateId: String(value.updateId) };
  // An old server silently ignores unknown query params, so a missing echo means
  // the rollout was not applied even though the flag was set. Abort before any
  // file is uploaded: continuing would finalize a full 100% publish.
  if (rolloutPercentage !== undefined && validated.rolloutPercentage === undefined) {
    throw new Error(
      'The server ignored --rollout-percentage and would publish to 100% of devices. Update the server to a version that supports progressive rollouts, or publish without --rollout-percentage.'
    );
  }
  return validated;
}
