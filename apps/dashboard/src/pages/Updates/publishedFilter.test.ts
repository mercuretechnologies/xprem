import assert from 'node:assert/strict';
import { test } from 'node:test';
import { resolveRange } from '@/lib/timeRange';
import { resolvePublishedFilter } from './publishedFilter';

const now = Date.parse('2026-10-04T12:00:00Z');

test('a same-day legacy URL keeps both UTC date bounds and a valid full-day picker range', () => {
  const filter = resolvePublishedFilter('2026-09-27', '2026-09-27', now);
  assert.equal(filter.from, '2026-09-27');
  assert.equal(filter.to, '2026-09-27');
  assert.deepEqual(filter.range, {
    from: '2026-09-27T00:00:00Z',
    to: '2026-09-27T23:59:59.999999999Z',
  });
  assert.ok(resolveRange(filter.range!, now));
});

test('legacy date windows remain UTC and inclusive through the last nanosecond of the end day', () => {
  const filter = resolvePublishedFilter('2026-09-26', '2026-09-27', now);
  assert.equal(filter.from, '2026-09-26');
  assert.equal(filter.to, '2026-09-27');
  const picker = resolveRange(filter.range!, now)!;
  assert.equal(picker.from.toISOString(), '2026-09-26T00:00:00.000Z');
  assert.equal(picker.to.toISOString(), '2026-09-27T23:59:59.999Z');
  // Forwarding the date itself uses the API's end-of-day nanosecond contract;
  // converting it to a Date would exclude the final 999,999 nanoseconds.
  assert.equal(filter.range!.to, '2026-09-27T23:59:59.999999999Z');
});

test('one-sided date filters never invent a bound on the unselected side', () => {
  assert.equal(resolvePublishedFilter('', '2026-09-27', now).from, '');
  assert.equal(resolvePublishedFilter('', '2026-09-27', now).to, '2026-09-27');
  assert.equal(resolvePublishedFilter('2026-09-27', '', now).from, '2026-09-27');
  assert.equal(resolvePublishedFilter('2026-09-27', '', now).to, '');
  assert.equal(resolvePublishedFilter('', '1900-01-01', now).from, '');
});

test('relative ranges still resolve once against the supplied clock and can end now', () => {
  const live = resolvePublishedFilter('now-24h', 'now', now);
  assert.equal(live.from, '2026-10-03T12:00:00.000Z');
  assert.equal(live.to, '');
  assert.deepEqual(live.range, { from: 'now-24h', to: 'now' });
  assert.equal(resolvePublishedFilter('now-2d', 'now-1d', now).to, '2026-10-03T12:00:00.000Z');
});

test('full RFC3339 timestamps retain their timezone and fractional precision', () => {
  const from = '2026-09-27T00:00:00.000000001+02:00';
  const to = '2026-09-27T23:59:59.999999999+02:00';
  const filter = resolvePublishedFilter(from, to, now);
  assert.equal(filter.from, from);
  assert.equal(filter.to, to);
  assert.deepEqual(filter.range, { from, to });
});

test('new unzoned timestamp input keeps its browser-local interpretation', () => {
  const filter = resolvePublishedFilter('2026-09-27 14:00', '2026-09-27 15:00', now);
  assert.equal(filter.from, new Date(2026, 8, 27, 14, 0).toISOString());
  assert.equal(filter.to, new Date(2026, 8, 27, 15, 0).toISOString());
});

test('invalid, equal and reversed pasted bounds never silently become an unfiltered query', () => {
  const malformed = resolvePublishedFilter('not-a-date', '2026-09-27', now);
  assert.equal(malformed.from, 'not-a-date');
  assert.equal(malformed.to, '2026-09-27');
  assert.equal(resolvePublishedFilter('2026-02-31', '', now).from, '2026-02-31');
  const equal = resolvePublishedFilter('2026-09-27T12:00:00Z', '2026-09-27T12:00:00Z', now);
  assert.equal(equal.from, '2026-09-27T12:00:00Z');
  assert.equal(equal.to, '2026-09-27T12:00:00Z');
  const reversed = resolvePublishedFilter('2026-09-28', '2026-09-27', now);
  assert.equal(reversed.from, '2026-09-28');
  assert.equal(reversed.to, '2026-09-27');
});

test('all-time selection leaves both API bounds unset', () => {
  assert.deepEqual(resolvePublishedFilter('', '', now), { from: '', to: '', range: null });
});
