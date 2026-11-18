// internal/paths/paths_test.go

package paths

import (
	"strings"
	"testing"
)

func TestLifecycleStateUsesPrivateBoundary(t *testing.T) {
	for name, path := range map[string]string{
		"layout version":  LayoutVersion,
		"install ledger":  InstallStateFile,
		"password marker": PasswordPendingMarker,
	} {
		if !strings.HasPrefix(path, PrivateDir+"/") {
			t.Errorf("%s %q is not below %q", name, path, PrivateDir)
		}
		if strings.HasPrefix(path, ConfigDir+"/") {
			t.Errorf("%s %q is below config directory", name, path)
		}
	}
	if InstallLock != RuntimeDir+"/install.lock" {
		t.Errorf("install lock %q is not below runtime dir", InstallLock)
	}
	if InstallLock == InstallStateFile {
		t.Error("stable lock and replaceable ledger share a path")
	}
	// The bootstrap file exists before the tree it announces, so it lives
	// beside that tree.
	if strings.HasPrefix(InstallBootstrap, VarLibVPN+"/") {
		t.Errorf("bootstrap path %q is below the tree it precedes", InstallBootstrap)
	}
}
