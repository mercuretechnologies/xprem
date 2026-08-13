import fetchRetry from 'fetch-retry';
import originalFetch, { RequestInit, Response } from 'node-fetch';

import Log from './log';
const fetch = fetchRetry(originalFetch);

export function isRetryableStatus(status: number): boolean {
  return status === 429 || status >= 500;
}

// Retry-After is either delay-seconds or an HTTP-date.
export function retryAfterMs(header: string | null): number | null {
  if (!header) {
    return null;
  }
  const seconds = Number(header);
  if (Number.isFinite(seconds)) {
    return seconds * 1000;
  }
  const dateMs = Date.parse(header);
  if (!Number.isNaN(dateMs)) {
    return dateMs - Date.now();
  }
  return null;
}

const MAX_RETRY_DELAY_MS = 60_000;
const MAX_RETRIED_ATTEMPT = 3;

// Upload URLs carry their credentials in the query string (S3 presign, Azure
// SAS, the local bucket's upload token), so logs may only show origin and path.
export function redactUrl(url: string): string {
  try {
    const parsed = new URL(url);
    return parsed.origin + parsed.pathname;
  } catch {
    return '<unparseable url>';
  }
}

function retryDelayForResponse(attempt: number, response: Response): number {
  const serverDelay = retryAfterMs(response.headers.get('retry-after'));
  const backoff = Math.pow(2, attempt) * 2000;
  return Math.min(Math.max(serverDelay ?? 0, backoff), MAX_RETRY_DELAY_MS);
}

export async function fetchWithRetries(url: string, options: RequestInit): Promise<Response> {
  return await fetch(url, {
    ...options,
    retryDelay(attempt, _error, response) {
      if (response) {
        return retryDelayForResponse(attempt, response);
      }
      return Math.pow(2, attempt) * 500;
    },
    retryOn: (attempt, error, response) => {
      if (attempt > MAX_RETRIED_ATTEMPT) {
        return false;
      }
      if (error) {
        Log.warn(`Retry ${attempt} after network error:`, error.message);
        return true;
      }
      if (response && isRetryableStatus(response.status)) {
        Log.warn(`Retry ${attempt} after HTTP ${response.status} from ${redactUrl(url)}`);
        return true;
      }
      return false;
    },
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms));
}

// fetch-retry re-sends the same RequestInit on every attempt, which silently
// replays an already-consumed stream body as empty. Callers with a one-shot
// body (multipart uploads) use this variant: makeOptions rebuilds the body
// for each attempt. Same retry policy as fetchWithRetries.
export async function fetchWithRetriesRebuildingBody(
  url: string,
  makeOptions: () => RequestInit
): Promise<Response> {
  for (let attempt = 0; ; attempt++) {
    let response: Response;
    try {
      response = await originalFetch(url, makeOptions());
    } catch (error: any) {
      if (attempt > MAX_RETRIED_ATTEMPT) {
        throw error;
      }
      Log.warn(`Retry ${attempt} after network error:`, error.message);
      await sleep(Math.pow(2, attempt) * 500);
      continue;
    }
    if (attempt <= MAX_RETRIED_ATTEMPT && isRetryableStatus(response.status)) {
      Log.warn(`Retry ${attempt} after HTTP ${response.status} from ${redactUrl(url)}`);
      await sleep(retryDelayForResponse(attempt, response));
      continue;
    }
    return response;
  }
}
