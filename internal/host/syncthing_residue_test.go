package host

import (
	"testing"

	"github.com/virtpriv/node/internal/paths"
)

func TestSyncthingResidueIncludesStagedCredentials(t *testing.T) {
	seen := make(map[string]bool)
	for _, path := range syncthingResiduePaths {
		seen[path] = true
	}
	for _, path := range []string{
		paths.StateSyncthingAPIKey,
		paths.StateSyncthingWebPassword,
	} {
		if !seen[path] {
			t.Errorf("staged credential %s is not install residue", path)
		}
	}
}
