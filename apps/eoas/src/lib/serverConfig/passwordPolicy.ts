// Mirror of the server-side password policy (internal/crypto/password.go),
// same as the dashboard mirror (apps/dashboard/src/lib/password-policy.ts).
// Keep the three in sync. Classes are unicode to match Go's
// unicode.IsUpper/IsLower/IsDigit, and length counts code points to match the
// server's rune count.

export const PASSWORD_MIN_LENGTH = 8;

type PasswordRule = {
  label: string;
  test: (password: string) => boolean;
};

const PASSWORD_RULES: PasswordRule[] = [
  {
    label: `at least ${PASSWORD_MIN_LENGTH} characters`,
    test: password => [...password].length >= PASSWORD_MIN_LENGTH,
  },
  {
    label: 'an uppercase letter',
    test: password => /\p{Lu}/u.test(password),
  },
  {
    label: 'a lowercase letter',
    test: password => /\p{Ll}/u.test(password),
  },
  {
    label: 'a digit',
    test: password => /\p{Nd}/u.test(password),
  },
  {
    label: 'a special character',
    test: password => /[^\p{Lu}\p{Ll}\p{Nd}]/u.test(password),
  },
];

/** Returns the labels of every failing rule, empty when the password passes. */
export function missingPasswordRules(password: string): string[] {
  return PASSWORD_RULES.filter(rule => !rule.test(password)).map(rule => rule.label);
}
