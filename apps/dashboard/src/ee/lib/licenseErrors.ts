// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

// Human messages for the license server's error codes, shared by the License
// page (check outcome) and the grace-period banner (validation failures).
const LICENSE_ERROR_MESSAGES: Record<string, string> = {
  LICENSE_KEY_NOT_FOUND: 'This license key does not exist.',
  LICENSE_KEY_ALREADY_USED:
    'This license key has already been used. Contact support@xprem.dev to release it.',
  LICENSE_KEY_EXPIRED: 'This license key expired before it was activated.',
  INVALID_INSTANCE_URL: "This server's URL is not allowed for this license.",
  SUBSCRIPTION_INACTIVE: 'The subscription behind this license is inactive.',
  LICENSE_EXPIRED: 'The subscription behind this license has ended.',
  INVALID_ACTIVATION: 'The license server no longer recognizes this activation.',
  LICENSE_SERVER_UNREACHABLE: 'The license server could not be reached.',
  LICENSE_SERVER_REJECTED: 'The license server rejected the request.',
  PLAN_NOT_SUPPORTED:
    'This license is not on the Enterprise plan. Only Enterprise licenses are supported for now.',
};

export const licenseErrorMessage = (code?: string): string => {
  if (code && LICENSE_ERROR_MESSAGES[code]) return LICENSE_ERROR_MESSAGES[code];
  return code
    ? `The license server refused the request (${code}).`
    : 'The license server refused the request.';
};
