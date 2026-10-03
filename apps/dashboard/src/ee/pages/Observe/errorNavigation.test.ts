// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { errorDetailsHref, errorsListHref, errorsListParams } from './errorNavigation';

test('opening an error uses its canonical global route and keeps list context in state', () => {
  const listParams = new URLSearchParams(
    'channel=staging&channel=preview&period=7d&live=1&errorSearch=Checkout&errorFatality=fatal&errorSort=lastSeen'
  );
  const original = listParams.toString();
  const destination = new URL(errorDetailsHref('g:checkout'), 'https://dashboard.test');
  assert.equal(decodeURIComponent(destination.pathname), '/observe/errors/g:checkout');
  assert.equal(destination.search, '');
  assert.equal(
    listParams.toString(),
    original,
    'the list URL must stay intact for return navigation'
  );
  assert.equal(
    errorsListHref(errorsListParams({ errorsSearch: original })),
    `/observe/errors?${original}`
  );
});

test('legacy list detail links remove query filters and restore them only on return', () => {
  const params = new URLSearchParams('errorId=f:checkout&device=known-device&from=now-30d&to=now');
  const destination = new URL(errorDetailsHref(params.get('errorId')!), 'https://dashboard.test');
  assert.equal(decodeURIComponent(destination.pathname), '/observe/errors/f:checkout');
  assert.equal(destination.search, '');
  assert.equal(
    errorsListHref(errorsListParams({ errorsSearch: params.toString() })),
    '/observe/errors?device=known-device&from=now-30d&to=now'
  );
});

test('old detail links canonicalize without turning URL filters into list state', () => {
  const old = new URL(
    '/observe/errors/g%3Acheckout?from=2026-09-26T12%3A00%3A00Z&to=2026-10-03T12%3A20%3A19.694Z&live=0&errorFatality=fatal',
    'https://dashboard.test'
  );
  const errorId = decodeURIComponent(old.pathname.split('/').pop()!);
  assert.equal(errorDetailsHref(errorId), '/observe/errors/g%3Acheckout');
  assert.equal(errorsListHref(errorsListParams(null)), '/observe/errors');
  assert.equal(errorsListHref(errorsListParams({})), '/observe/errors');
  assert.equal(errorsListHref(errorsListParams({ errorsSearch: 123 })), '/observe/errors');
});

test('detail paths encode identifiers and never include list query parameters', () => {
  assert.equal(
    errorDetailsHref('g:checkout/failed?channel=staging'),
    '/observe/errors/g%3Acheckout%2Ffailed%3Fchannel%3Dstaging'
  );
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
