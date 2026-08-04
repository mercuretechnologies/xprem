package auditlog

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

const catalogPath = "../../apps/dashboard/src/ee/lib/auditCatalog.ts"

var (
	goActionPattern = regexp.MustCompile(`Action\w+\s+Action\s*=\s*"([^"]+)"`)
	tsActionPattern = regexp.MustCompile(`'([a-z_]+\.[a-z_]+)'`)
)

// An action the dashboard does not list is emitted but cannot be filtered for,
// so it is invisible to anyone looking for it. Nothing in the type system links
// the two lists, and they have drifted before.
func TestEveryAuditActionIsFilterableInTheDashboard(t *testing.T) {
	catalog, err := os.ReadFile(filepath.Clean(catalogPath))
	if err != nil {
		t.Skipf("dashboard sources not available: %v", err)
	}
	source, err := os.ReadFile("auditlog.go")
	if err != nil {
		t.Fatalf("reading actions: %v", err)
	}

	listed := map[string]bool{}
	for _, match := range tsActionPattern.FindAllStringSubmatch(string(catalog), -1) {
		listed[match[1]] = true
	}

	emitted := goActionPattern.FindAllStringSubmatch(string(source), -1)
	assert.NotEmpty(t, emitted, "no actions found — the pattern stopped matching, not the drift")
	for _, match := range emitted {
		assert.True(t, listed[match[1]],
			"%s is emitted by the server but missing from auditCatalog.ts, so it cannot be filtered for", match[1])
	}
}
