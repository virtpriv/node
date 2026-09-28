package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableRecordPreservesMigrationBoundary(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := workflowFixture()
	j.Review.Manifest.Version = "0.7.1-rc.1"
	j.Started["lnd"] = true
	j.MayHaveRun["lnd"] = true
	j.Phase = "starting"
	if err := saveJob(root, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadJob(root)
	if err != nil {
		t.Fatal("read saved update record:", err)
	}
	if loaded == nil || loaded.Review.Manifest.Version != "0.7.1-rc.1" || !loaded.MayHaveRun["lnd"] || !loaded.Started["lnd"] {
		t.Fatal("lost possible database migration")
	}
	// Change only the schema so another missing field cannot mask the refusal.
	j.Schema = 99
	if err := saveJob(root, j); err != nil {
		t.Fatal(err)
	}
	if _, err := loadJob(root); err == nil {
		t.Fatal("unknown record became an empty job")
	}
}

func TestApprovalBindsSourceConfigurationAndRelease(t *testing.T) {
	j := workflowFixture()
	p := prepared{Review: j.Review, WorkerHash: j.WorkerHash, ConfigHash: j.ConfigHash}
	first, err := approvalToken(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Review.Token = first
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Each token has its own record; changing the data under an old token must
	// be rejected, even when two terminals reviewed the same release archive.
	if err := os.Mkdir(filepath.Join(root, "reviews"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "reviews", first+".json")
	if err := writeJSON(path, p); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrepared(root, first); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*prepared){
		"configuration": func(p *prepared) { p.ConfigHash = strings.Repeat("b", 64) },
		"source":        func(p *prepared) { p.Review.Source.LND = "0.21.0-beta" },
		"archive":       func(p *prepared) { p.Review.Digest = strings.Repeat("b", 64) },
		"worker":        func(p *prepared) { p.WorkerHash = strings.Repeat("b", 64) },
		"network":       func(p *prepared) { p.Review.Network = "mainnet" },
		"recovery":      func(p *prepared) { p.Previous = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := p
			change(&changed)
			if err := writeJSON(path, changed); err != nil {
				t.Fatal(err)
			}
			if _, err := readPrepared(root, first); err == nil {
				t.Fatal("accepted changed review under old token")
			}
		})
	}
}
