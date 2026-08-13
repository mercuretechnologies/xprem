const kv: Record<
  string,
  {
    tokens: number;
    lastRefill: number;
  }
> = {};

export class RateLimiter {
  constructor(
    private readonly key: string,
    private readonly capacity: number,
    private readonly refillRate: number
  ) {}

  async take(): Promise<void> {
    const now = Date.now();
    const bucket = kv[this.key];
    let tokens = bucket ? bucket.tokens : this.capacity;
    const lastRefill = bucket ? bucket.lastRefill : now;
    const elapsed = (now - lastRefill) / 1000;
    tokens = Math.min(this.capacity, tokens + elapsed * this.refillRate);

    if (tokens < 1) {
      const waitMs = ((1 - tokens) / this.refillRate) * 1000;
      await new Promise(res => setTimeout(res, waitMs));
      await this.take();
      return;
    }
    tokens -= 1;
    kv[this.key] = {
      tokens,
      lastRefill: now,
    };
  }
}
