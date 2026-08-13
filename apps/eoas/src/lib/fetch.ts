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

export async function fetchWithRetries(url: string, options: RequestInit): Promise<Response> {
  return await fetch(url, {
    ...options,
    retryDelay(attempt, _error, response) {
      if (response) {
        const serverDelay = retryAfterMs(response.headers.get('retry-after'));
        const backoff = Math.pow(2, attempt) * 2000;
        return Math.min(Math.max(serverDelay ?? 0, backoff), MAX_RETRY_DELAY_MS);
      }
      return Math.pow(2, attempt) * 500;
    },
    retryOn: (attempt, error, response) => {
      if (attempt > 3) {
        return false;
      }
      if (error) {
        Log.warn(`Retry ${attempt} after network error:`, error.message);
        return true;
      }
      if (response && isRetryableStatus(response.status)) {
        Log.warn(`Retry ${attempt} after HTTP ${response.status} from ${url}`);
        return true;
      }
      return false;
    },
  });
}
