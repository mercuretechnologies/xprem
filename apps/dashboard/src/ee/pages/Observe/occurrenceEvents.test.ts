// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { ObserveLog } from '@/lib/api';
import { occurrenceEventsHref } from './occurrenceEvents';

const occurrence = {
  eventKey: 'd2e31826-0a81-4c45-bedd-f6f216344a13',
  timestamp: '2026-10-03T12:34:56.123456789Z',
  easClientId: 'a7e865d0-045c-4ed3-a753-19cec1ecb6e1',
  updateId: '80be5c31-0b09-4b85-967a-5d7132e1e771',
  platform: 'ios',
  runtimeVersion: '56.0.0',
} as ObserveLog;

test('occurrence navigation freezes the precise event and preceding device context', () => {
  const destination = new URL(occurrenceEventsHref(occurrence), 'https://dashboard.test');
  const params = destination.searchParams;
  assert.equal(destination.pathname, '/observe/events');
  assert.equal(params.get('eventKey'), occurrence.eventKey);
  assert.equal(params.get('to'), occurrence.timestamp, 'API nanoseconds must not be truncated');
  assert.equal(params.get('from'), '2026-10-03T12:29:56.123Z');
  assert.equal(params.get('live'), '0');
  assert.equal(params.get('device'), occurrence.easClientId);
  assert.equal(params.get('update'), occurrence.updateId);
  assert.equal(params.get('platform'), occurrence.platform);
  assert.equal(params.get('runtime'), occurrence.runtimeVersion);
  assert.deepEqual([...params.keys()].sort(), [
    'device',
    'eventKey',
    'from',
    'live',
    'platform',
    'runtime',
    'to',
    'update',
  ]);
});

test('unknown device identity does not narrow the stream to the zero UUID', () => {
  for (const easClientId of ['', '00000000-0000-0000-0000-000000000000', 'not-a-device-id']) {
    const params = new URL(
      occurrenceEventsHref({ ...occurrence, easClientId }),
      'https://dashboard.test'
    ).searchParams;
    assert.equal(params.has('device'), false);
    assert.equal(params.get('eventKey'), occurrence.eventKey);
  }
});

test('embedded updates keep their context without introducing missing dimensions', () => {
  const params = new URL(
    occurrenceEventsHref({
      ...occurrence,
      updateId: '00000000-0000-0000-0000-000000000000',
      platform: '',
      runtimeVersion: '',
    }),
    'https://dashboard.test'
  ).searchParams;
  assert.equal(params.get('update'), '00000000-0000-0000-0000-000000000000');
  assert.equal(params.has('platform'), false);
  assert.equal(params.has('runtime'), false);
  assert.equal(params.has('event'), false, 'an event-name filter would hide preceding events');
  assert.equal(params.has('level'), false, 'an error-level filter would hide preceding events');
});
