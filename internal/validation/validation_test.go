package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestName_AcceptsValid(t *testing.T) {
	for _, v := range []string{"staging", "branch-1", "feature_x", "1.0.0", "prod.eu", strings.Repeat("a", maxNameLen)} {
		assert.NoError(t, Name("branchName", v), "expected %q to be valid", v)
	}
}

func TestName_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":    "",
		"too long": strings.Repeat("a", maxNameLen+1),
		// Reserved as the wildcard of API key access rules, so a branch can
		// never be named something a rule pattern already means.
		"wildcard":        "pr-*",
		"bare wildcard":   "*",
		"slash":           "feature/x",
		"backslash":       "feature\\x",
		"dot":             ".",
		"dotdot":          "..",
		"control char":    "bad\x01name",
		"null byte":       "bad\x00name",
		"newline":         "bad\nname",
		"tab":             "bad\tname",
		"carriage return": "bad\rname",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			err := Name("branchName", value)
			require.Error(t, err)
			// Must be a *validation.Error so handlers map it to 400.
			assert.True(t, IsValidationError(err))
			assert.Contains(t, err.Error(), "branchName")
		})
	}
}

func TestNamePattern_AcceptsWildcards(t *testing.T) {
	// Everything Name accepts, plus the wildcard forms and the wider length
	// cap that lets a rule name a legacy branch longer than maxNameLen.
	for _, v := range []string{"staging", "*", "pr-*", "*-eu", "pr-*-eu", "a*b*c", strings.Repeat("a", maxPatternLen)} {
		assert.NoError(t, NamePattern("pattern", v), "expected %q to be valid", v)
	}
}

func TestNamePattern_RejectsWhatNameRejects(t *testing.T) {
	for name, value := range map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("a", maxPatternLen+1),
		"slash":        "feature/*",
		"dotdot":       "..",
		"control char": "bad\nname",
	} {
		t.Run(name, func(t *testing.T) {
			err := NamePattern("pattern", value)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
		})
	}
}

func TestDisplayName_AcceptsValid(t *testing.T) {
	for _, v := range []string{"Production", "My App", "Café EU", "app (staging)", "tabbed\tname"} {
		assert.NoError(t, DisplayName("name", v), "expected %q to be valid", v)
	}
}

func TestDisplayName_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"whitespace only": "   ",
		"too long":        strings.Repeat("a", maxDisplayNameLen+1),
		"control char":    "bad\x01name",
		"null byte":       "bad\x00name",
		"newline":         "line\nbreak",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			err := DisplayName("name", value)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
		})
	}
}

func TestGitURL_NormalizesRepositoryURLs(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		want  string
	}{
		"github":         {"https://github.com/acme/mobile", "https://github.com/acme/mobile"},
		"trailing slash": {"https://gitlab.com/acme/mobile/", "https://gitlab.com/acme/mobile"},
		"git suffix":     {"https://bitbucket.org/acme/mobile.git", "https://bitbucket.org/acme/mobile"},
		"self hosted":    {"http://git.internal:8080/team/mobile", "http://git.internal:8080/team/mobile"},
		"clear setting":  {"   ", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := GitURL("gitUrl", tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGitURL_RejectsUnsafeOrNonWebURLs(t *testing.T) {
	for name, value := range map[string]string{
		"ssh remote":      "git@github.com:acme/mobile.git",
		"file URL":        "file:///tmp/mobile",
		"missing host":    "https:///acme/mobile",
		"missing repo":    "https://github.com",
		"credentials":     "https://user:token@github.com/acme/mobile",
		"query":           "https://github.com/acme/mobile?token=secret",
		"fragment":        "https://github.com/acme/mobile#readme",
		"control char":    "https://github.com/acme/mobile\nother",
		"over max length": "https://github.com/acme/" + strings.Repeat("a", maxGitURLLen),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := GitURL("gitUrl", value)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
			assert.Contains(t, err.Error(), "gitUrl")
		})
	}
}

func TestNumericID(t *testing.T) {
	for _, v := range []string{"1", "42", "9223372036854775807"} {
		assert.NoError(t, NumericID("apiKeyId", v), "expected %q to be valid", v)
	}
	for name, v := range map[string]string{
		"empty":       "",
		"non-numeric": "branch-2-id",
		"zero":        "0",
		"negative":    "-5",
		"float":       "1.5",
		"overflow":    "99999999999999999999999",
	} {
		t.Run(name, func(t *testing.T) {
			err := NumericID("apiKeyId", v)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
		})
	}
}

func TestRolloutPercentage(t *testing.T) {
	for _, v := range []string{"1", "50", "99", "100"} {
		n, err := RolloutPercentage("rolloutPercentage", v)
		assert.NoError(t, err, "expected %q to be valid", v)
		assert.Positive(t, n)
	}
	for name, v := range map[string]string{
		"empty":       "",
		"zero":        "0",
		"negative":    "-5",
		"over 100":    "101",
		"non-numeric": "abc",
		"float":       "20.5",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RolloutPercentage("rolloutPercentage", v)
			require.Error(t, err)
			assert.True(t, IsValidationError(err))
			assert.Contains(t, err.Error(), "rolloutPercentage")
		})
	}
}

func TestError_IsDetectableAcrossWrapping(t *testing.T) {
	// errors.As must see through fmt.Errorf("%w") wrapping so a service that
	// wraps a validation error still maps to 400.
	base := Errorf("keysConfig", "mode is required")
	wrapped := errors.Join(errors.New("context"), base)
	assert.True(t, IsValidationError(wrapped))

	var ve *Error
	require.True(t, errors.As(wrapped, &ve))
	assert.Equal(t, "keysConfig", ve.Field)
}
