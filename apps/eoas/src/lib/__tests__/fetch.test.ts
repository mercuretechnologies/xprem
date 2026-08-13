import { Response } from 'node-fetch';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchWithRetries, isRetryableStatus, retryAfterMs } from '../fetch';

vi.mock('node-fetch', async importOriginal => {
  const actual = await importOriginal<typeof import('node-fetch')>();
  return { ...actual, default: vi.fn() };
});

const mockFetch = vi.mocked((await import('node-fetch')).default);

function response(status: number, headers: Record<string, string> = {}): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: {
      get: (name: string) => headers[name.toLowerCase()] ?? null,
    },
  } as unknown as Response;
}

beforeEach(() => {
  vi.useFakeTimers();
  mockFetch.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

async function settled<T>(promise: Promise<T>): Promise<T> {
  // Walks through every backoff timer fetch-retry schedules along the way.
  await vi.advanceTimersByTimeAsync(120_000);
  return await promise;
}

describe('#isRetryableStatus', () => {
  it('retries 429 and 5xx only', () => {
    expect(isRetryableStatus(429)).toBe(true);
    expect(isRetryableStatus(500)).toBe(true);
    expect(isRetryableStatus(503)).toBe(true);
    expect(isRetryableStatus(200)).toBe(false);
    expect(isRetryableStatus(400)).toBe(false);
    expect(isRetryableStatus(404)).toBe(false);
  });
});

describe('#retryAfterMs', () => {
  it('parses delay-seconds', () => {
    expect(retryAfterMs('2')).toBe(2000);
    expect(retryAfterMs('0')).toBe(0);
  });

  it('parses an HTTP-date relative to now', () => {
    vi.setSystemTime(new Date('2026-08-13T00:00:00Z'));
    expect(retryAfterMs('Thu, 13 Aug 2026 00:00:05 GMT')).toBe(5000);
  });

  it('returns null on missing or garbage input', () => {
    expect(retryAfterMs(null)).toBe(null);
    expect(retryAfterMs('')).toBe(null);
    expect(retryAfterMs('soon')).toBe(null);
  });
});

describe('#fetchWithRetries', () => {
  it('returns a successful response without retrying', async () => {
    mockFetch.mockResolvedValueOnce(response(200));
    const res = await settled(fetchWithRetries('https://example.com', {}));
    expect(res.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it('does not retry non-retryable statuses', async () => {
    mockFetch.mockResolvedValueOnce(response(400));
    const res = await settled(fetchWithRetries('https://example.com', {}));
    expect(res.status).toBe(400);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it('retries a 503 until it succeeds', async () => {
    mockFetch
      .mockResolvedValueOnce(response(503))
      .mockResolvedValueOnce(response(503))
      .mockResolvedValueOnce(response(200));
    const res = await settled(fetchWithRetries('https://example.com', {}));
    expect(res.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(3);
  });

  it('retries a network error until it succeeds', async () => {
    mockFetch
      .mockRejectedValueOnce(new Error('socket hang up'))
      .mockResolvedValueOnce(response(200));
    const res = await settled(fetchWithRetries('https://example.com', {}));
    expect(res.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it('gives up after the retry budget and returns the last response', async () => {
    mockFetch.mockResolvedValue(response(503));
    const res = await settled(fetchWithRetries('https://example.com', {}));
    expect(res.status).toBe(503);
    // attempt > 3 stops the loop: initial request + 4 retries.
    expect(mockFetch).toHaveBeenCalledTimes(5);
  });

  it('waits at least Retry-After before retrying', async () => {
    mockFetch
      .mockResolvedValueOnce(response(429, { 'retry-after': '10' }))
      .mockResolvedValueOnce(response(200));

    const promise = fetchWithRetries('https://example.com', {});
    await vi.advanceTimersByTimeAsync(9_000);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_100);
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect((await promise).status).toBe(200);
  });
});
