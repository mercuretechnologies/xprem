import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { RateLimiter } from '../rateLimiter';

// State is shared through a module-level store keyed by name, so every test
// uses its own key.
let key: number = 0;
function limiter(capacity: number, refillRate: number): RateLimiter {
  key += 1;
  return new RateLimiter(`test-${key}`, capacity, refillRate);
}

async function isResolved(promise: Promise<unknown>): Promise<boolean> {
  let resolved = false;
  void promise.then(() => {
    resolved = true;
  });
  await vi.advanceTimersByTimeAsync(0);
  return resolved;
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe('RateLimiter', () => {
  it('serves the initial burst without waiting', async () => {
    const l = limiter(3, 10);
    expect(await isResolved(l.take())).toBe(true);
    expect(await isResolved(l.take())).toBe(true);
    expect(await isResolved(l.take())).toBe(true);
  });

  it('makes the caller wait once the bucket is empty', async () => {
    const l = limiter(2, 10);
    await l.take();
    await l.take();

    const blocked = l.take();
    expect(await isResolved(blocked)).toBe(false);

    // 10 tokens/s -> one token every 100ms.
    await vi.advanceTimersByTimeAsync(50);
    expect(await isResolved(blocked)).toBe(false);
    await vi.advanceTimersByTimeAsync(60);
    expect(await isResolved(blocked)).toBe(true);
  });

  it('supports fractional refill rates', async () => {
    const l = limiter(1, 2);
    await l.take();

    const blocked = l.take();
    // 2 tokens/s -> one token every 500ms.
    await vi.advanceTimersByTimeAsync(400);
    expect(await isResolved(blocked)).toBe(false);
    await vi.advanceTimersByTimeAsync(150);
    expect(await isResolved(blocked)).toBe(true);
  });

  it('never refills beyond capacity', async () => {
    const l = limiter(2, 10);
    await l.take();
    await l.take();

    // A long idle period must not bank more than `capacity` tokens.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await isResolved(l.take())).toBe(true);
    expect(await isResolved(l.take())).toBe(true);
    expect(await isResolved(l.take())).toBe(false);
  });

  it('sustains the configured rate over time', async () => {
    const l = limiter(1, 10);
    let done = 0;
    for (let i = 0; i < 11; i++) {
      void l.take().then(() => {
        done += 1;
      });
    }
    await vi.advanceTimersByTimeAsync(0);
    expect(done).toBe(1);

    // 10 tokens/s: after 1s, the 10 queued takes have all been served.
    await vi.advanceTimersByTimeAsync(1100);
    expect(done).toBe(11);
  });
});
