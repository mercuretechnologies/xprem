// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { errorDetailsHref, errorsListHref } from './errorNavigation';

test('opening a live error uses a dedicated route and freezes the response window', () => {
  const listParams = new URLSearchParams(
    'channel=staging&channel=preview&period=7d&live=1&errorSearch=Checkout&errorFatality=fatal&errorSort=lastSeen'
  );
  const original = listParams.toString();
  const responseWindow = { from: '2026-09-26T11:00:00Z', to: '2026-10-03T11:04:08.123456789Z' };
  const destination = new URL(
    errorDetailsHref('g:checkout', listParams, responseWindow),
    'https://dashboard.test'
  );
  assert.equal(decodeURIComponent(destination.pathname), '/observe/errors/g:checkout');
  assert.equal(destination.searchParams.get('from'), responseWindow.from);
  assert.equal(destination.searchParams.get('to'), responseWindow.to);
  assert.equal(destination.searchParams.get('live'), '0');
  assert.equal(destination.searchParams.has('period'), false);
  assert.equal(destination.searchParams.has('errorId'), false);
  assert.deepEqual(destination.searchParams.getAll('channel'), ['staging', 'preview']);
  assert.equal(destination.searchParams.get('errorFatality'), 'fatal');
  assert.equal(
    listParams.toString(),
    original,
    'the list URL must stay intact for return navigation'
  );
  assert.equal(errorsListHref(listParams), `/observe/errors?${original}`);
});

test('legacy detail links keep their context and remove the old query identifier', () => {
  const params = new URLSearchParams('errorId=f:checkout&device=known-device&from=now-30d&to=now');
  const destination = new URL(
    errorDetailsHref(params.get('errorId')!, params),
    'https://dashboard.test'
  );
  assert.equal(decodeURIComponent(destination.pathname), '/observe/errors/f:checkout');
  assert.equal(destination.searchParams.has('errorId'), false);
  assert.equal(destination.searchParams.get('device'), 'known-device');
  assert.equal(destination.searchParams.get('from'), 'now-30d');
  assert.equal(destination.searchParams.get('live'), '0');
  assert.equal(errorsListHref(params), '/observe/errors?device=known-device&from=now-30d&to=now');
});

test('breakdown navigation filters the list while preserving its other filters', () => {
  const params = new URLSearchParams('channel=staging&os=Android&osVersion=15&errorSort=lastSeen');
  const destination = new URL(
    errorsListHref(params, { osName: 'iOS', osVersion: '26.5' }),
    'https://dashboard.test'
  );
  assert.equal(destination.pathname, '/observe/errors');
  assert.deepEqual(destination.searchParams.getAll('os'), ['iOS']);
  assert.deepEqual(destination.searchParams.getAll('osVersion'), ['26.5']);
  assert.equal(destination.searchParams.get('channel'), 'staging');
  assert.equal(destination.searchParams.get('errorSort'), 'lastSeen');
  assert.equal(params.get('os'), 'Android', 'the detail context must not be mutated');
  assert.equal(errorsListHref(new URLSearchParams()), '/observe/errors');
});

test('return navigation preserves context when URLSearchParams.size is unavailable', () => {
  const sizeDescriptor = Object.getOwnPropertyDescriptor(URLSearchParams.prototype, 'size');
  Reflect.deleteProperty(URLSearchParams.prototype, 'size');
  try {
    const params = new URLSearchParams(
      'errorId=g:checkout&channel=staging&channel=preview&from=2026-10-02T00:00:00.123456789Z&to=2026-10-03T00:00:00Z&live=0&errorSearch=Checkout&errorFatality=fatal&errorSort=lastSeen'
    );
    const original = params.toString();
    const expected = new URLSearchParams(params);
    expected.delete('errorId');
    assert.equal(errorsListHref(params), `/observe/errors?${expected}`);
    assert.equal(
      errorsListHref(params, { runtimeVersion: '1.2.3' }),
      `/observe/errors?${expected}&runtime=1.2.3`
    );
    assert.equal(params.toString(), original, 'the detail context must not be mutated');
    assert.equal(errorsListHref(new URLSearchParams()), '/observe/errors');
    assert.equal(errorsListHref(new URLSearchParams('errorId=g:checkout')), '/observe/errors');
  } finally {
    if (sizeDescriptor) {
      Object.defineProperty(URLSearchParams.prototype, 'size', sizeDescriptor);
    }
  }
});
