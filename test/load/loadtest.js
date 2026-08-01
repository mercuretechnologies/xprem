import http from 'k6/http';
import { check } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

// expo-open-ota capacity test.
//
// Three phases:
//   1. fleet      - the update-check traffic of a 1M MAU app, under a typical
//                   AND a deliberately pessimistic traffic model.
//   2. probe      - a slow climb through the saturation knee: locate the ceiling.
//   3. push_storm - a rollout pushed to the whole 1M fleet: every opener is
//                   outdated, downloads the full manifest and follows the asset
//                   URLs it returns. The expensive path, at the worst moment.
//
// Traffic models (checks happen once per app open):
//   typical:     DAU = 20% of MAU, 2.5 sessions/day, peak = 3x avg  => ~20 req/s per 1M MAU
//   pessimistic: DAU = 50% of MAU, 5 sessions/day,   peak = 4x avg  => ~115 req/s per 1M MAU
//
// Usage:
//   k6 run \
//     -e BASE_URL=http://<server>:3000 \
//     -e APP_ID=<app-id> \
//     -e IOS_UPDATE_ID=<ios-manifest-id> \
//     -e ANDROID_UPDATE_ID=<android-manifest-id> \
//     test/load/loadtest.js
//
// Smoke-check the script before a real run:
//   k6 run --vus 1 --iterations 3 -e BASE_URL=... -e APP_ID=... \
//     -e IOS_UPDATE_ID=... -e ANDROID_UPDATE_ID=... test/load/loadtest.js

const BASE = __ENV.BASE_URL;
const APP_ID = __ENV.APP_ID;
const UPDATE_IDS = { ios: __ENV.IOS_UPDATE_ID, android: __ENV.ANDROID_UPDATE_ID };

// A finite fleet with stable device IDs, like real installs. An infinite
// stream of fresh IDs would simulate a fleet no app has, and artificially
// inflate device registrations server-side.
const DEVICE_POOL = Array.from({ length: 100000 }, () => uuidv4());

export const options = {
  cloud: {
    distribution: { paris: { loadZone: 'amazon:fr:paris', percent: 100 } },
  },
  scenarios: {
    // Phase 1 (0:00 -> 6:00) - a 1M MAU fleet at peak hour.
    fleet: {
      executor: 'ramping-arrival-rate',
      exec: 'updateCheck',
      startRate: 5, timeUnit: '1s',
      preAllocatedVUs: 20, maxVUs: 100,
      stages: [
        { target: 20,  duration: '30s' },  // 1M MAU, typical model
        { target: 20,  duration: '90s' },
        { target: 115, duration: '30s' },  // 1M MAU, pessimistic model
        { target: 115, duration: '90s' },
        { target: 230, duration: '30s' },  // 2M MAU, pessimistic model
        { target: 230, duration: '90s' },
      ],
    },

    // Phase 2 (6:00 -> 10:00) - capacity probe. The slow ramp makes the
    // saturation knee visible instead of jumping over it.
    probe: {
      executor: 'ramping-arrival-rate',
      exec: 'updateCheck',
      startTime: '6m',
      startRate: 200, timeUnit: '1s',
      preAllocatedVUs: 50, maxVUs: 200,
      stages: [
        { target: 650, duration: '2m' },
        { target: 650, duration: '90s' },
        { target: 0,   duration: '30s' },
      ],
    },

    // Phase 3 (10:30 -> 16:30) - rollout push storm on the 1M fleet.
    // A push lands on every device; ~25% open within minutes, front-loaded.
    // Rates below are APP OPENS per second; every open is an outdated device:
    // full manifest + its asset requests. "Handling it" means zero errors,
    // bounded queueing, and full drain once the wave decays.
    // NOTE: needs k6 OSS or a paid plan (maxVUs > 100).
    push_storm: {
      executor: 'ramping-arrival-rate',
      exec: 'rolloutOpen',
      startTime: '10m30s',
      startRate: 20, timeUnit: '1s',
      preAllocatedVUs: 200, maxVUs: 1000,
      stages: [
        { target: 1000, duration: '30s' },  // the push lands
        { target: 400,  duration: '2m' },   // long tail of opens
        { target: 100,  duration: '2m' },   // decay
        { target: 20,   duration: '90s' },  // back to baseline; the queue must drain here
      ],
    },
  },

  thresholds: {
    // Real fleet traffic must be served in milliseconds.
    'http_req_duration{scenario:fleet}': ['p(95)<100', 'p(99)<250'],
    // The probe is allowed to queue - finding the ceiling is its job.
    'http_req_duration{scenario:probe}': ['p(99)<2000'],
    // The storm must be absorbed: deep queueing is expected, failures are not.
    'http_req_failed{scenario:push_storm}': ['rate<0.001'],
    http_req_failed: ['rate<0.001'],
    // If the generator drops iterations, the run is invalid - publish this metric.
    dropped_iterations: ['count<1'],
  },
};

function checkHeaders(outdated) {
  const platform = Math.random() < 0.6 ? 'ios' : 'android';
  const headers = {
    'accept': 'multipart/mixed',
    'expo-protocol-version': '1',
    'expo-app-id': APP_ID,
    'expo-platform': platform,
    'expo-runtime-version': '3.0.0',
    'expo-channel-name': 'production',
    'eas-client-id': DEVICE_POOL[Math.floor(Math.random() * DEVICE_POOL.length)],
    // Every real expo-updates client with code signing asks for a signature.
    'expo-expect-signature': 'sig, keyid="main", alg="rsa-v1_5-sha256"',
  };
  // An up-to-date device advertises its current update and gets a signed
  // "no update available" directive - the everyday path.
  if (!outdated) headers['expo-current-update-id'] = UPDATE_IDS[platform];
  return headers;
}

// Phase 1 & 2: a routine update check. 95% of a real fleet is up to date.
export function updateCheck() {
  const res = http.get(`${BASE}/manifest`, { headers: checkHeaders(Math.random() >= 0.95) });
  check(res, { 'manifest ok': (r) => r.status === 200 });
}

// Phase 3: an outdated device opening because of the push. It downloads the
// full manifest, then requests the assets the manifest points at. Only asset
// URLs served by the server itself are followed (redirects excluded: bundle
// bytes are the storage/CDN's job, resolving and signing them is the server's).
export function rolloutOpen() {
  const res = http.get(`${BASE}/manifest`, {
    headers: checkHeaders(true),
    timeout: '120s',
  });
  check(res, { 'manifest ok': (r) => r.status === 200 });
  if (res.status !== 200 || !res.body) return;

  const assetUrls = (String(res.body).match(/https?:\/\/[^"\s]+\/assets\?[^"\s]+/g) || []);
  for (const url of assetUrls.slice(0, 4)) {
    if (!url.startsWith(BASE)) continue;  // CDN-direct URLs never hit the server
    const asset = http.get(url.replace(/&amp;/g, '&'), { redirects: 0, timeout: '120s' });
    check(asset, { 'asset resolved': (r) => r.status === 200 || (r.status >= 301 && r.status <= 303) });
  }
}
