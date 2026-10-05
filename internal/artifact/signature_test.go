package artifact

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ── Test helpers ────────────────────────────────────────

func gpgAvailable() bool {
	_, err := exec.LookPath("gpg")
	return err == nil
}

// testGenKey generates a GPG key in the given homedir and
// returns the primary-key fingerprint. If addSigningSubkey
// is true, the primary key has certify-only usage and a
// separate signing subkey is added.
func testGenKey(
	t *testing.T, gpgHome, name string,
	addSigningSubkey bool,
) string {
	t.Helper()

	var params string
	if addSigningSubkey {
		params = fmt.Sprintf(`%%no-protection
Key-Type: RSA
Key-Length: 2048
Key-Usage: cert
Subkey-Type: RSA
Subkey-Length: 2048
Subkey-Usage: sign
Name-Real: %s
Name-Email: %s@test.local
%%commit
`, name, name)
	} else {
		params = fmt.Sprintf(`%%no-protection
Key-Type: RSA
Key-Length: 2048
Key-Usage: sign
Name-Real: %s
Name-Email: %s@test.local
%%commit
`, name, name)
	}

	paramFile := filepath.Join(gpgHome, "params-"+name)
	if err := os.WriteFile(
		paramFile, []byte(params), 0600); err != nil {
		t.Fatalf("write key params for %s: %v", name, err)
	}

	output, err := exec.Command(
		"gpg", "--homedir", gpgHome,
		"--batch", "--gen-key", paramFile,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate key %s: %v\n%s", name, err, output)
	}

	return testGetFingerprint(t, gpgHome, name)
}

// testGetFingerprint extracts the primary-key fingerprint for
// the named key from the given GPG homedir.
func testGetFingerprint(
	t *testing.T, gpgHome, name string,
) string {
	t.Helper()

	output, err := exec.Command(
		"gpg", "--homedir", gpgHome,
		"--batch", "--with-colons",
		"--list-keys", name,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("list key %s: %v\n%s", name, err, output)
	}

	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			fields := strings.Split(line, ":")
			if len(fields) >= 10 && fields[9] != "" {
				return fields[9]
			}
		}
	}
	t.Fatalf("fingerprint not found for %s in:\n%s",
		name, output)
	return ""
}

// testExportKey exports the public key for the given fingerprint
// to a file.
func testExportKey(
	t *testing.T, gpgHome, fingerprint, destFile string,
) {
	t.Helper()

	output, err := exec.Command(
		"gpg", "--homedir", gpgHome,
		"--batch", "--armor",
		"--export", fingerprint,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("export key %s: %v", fingerprint, err)
	}
	if err := os.WriteFile(destFile, output, 0600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
}

// testSign creates an armored detached signature of dataFile
// using the key identified by fingerprint.
func testSign(
	t *testing.T, gpgHome, fingerprint,
	dataFile, sigFile string,
) {
	t.Helper()

	output, err := exec.Command(
		"gpg", "--homedir", gpgHome,
		"--batch", "--armor",
		"--local-user", fingerprint,
		"--detach-sign",
		"--output", sigFile,
		dataFile,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("sign with %s: %v\n%s",
			fingerprint, err, output)
	}
}

// testClearsign creates a CLEARSIGNED file from dataFile using
// the key identified by fingerprint: data and signature in one
// file, the format Syncthing uses for sha256sum.txt.asc.
func testClearsign(
	t *testing.T, gpgHome, fingerprint,
	dataFile, outFile string,
) {
	t.Helper()

	output, err := exec.Command(
		"gpg", "--homedir", gpgHome,
		"--batch", "--armor",
		"--local-user", fingerprint,
		"--clearsign",
		"--output", outFile,
		dataFile,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("clearsign with %s: %v\n%s",
			fingerprint, err, output)
	}
}

// ── Hermetic test suite ─────────────────────────────────

func TestVerifySignature(t *testing.T) {
	if !gpgAvailable() {
		t.Skip("gpg not available — skipping")
	}

	// Set up a key-generation homedir with two distinct keys
	// and one subkey-only key.
	genHome, err := os.MkdirTemp("", "vpn-test-gen-")
	if err != nil {
		t.Fatalf("create gen home: %v", err)
	}
	defer os.RemoveAll(genHome)

	fpAlpha := testGenKey(t, genHome, "Alpha", false)
	fpBeta := testGenKey(t, genHome, "Beta", false)
	fpSubkey := testGenKey(t, genHome, "SubkeySigner", true)

	// Create test data file.
	dataDir, err := os.MkdirTemp("", "vpn-test-data-")
	if err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	defer os.RemoveAll(dataDir)

	dataFile := filepath.Join(dataDir, "testdata.txt")
	if err := os.WriteFile(
		dataFile, []byte("test data content\n"), 0600); err != nil {
		t.Fatalf("write test data: %v", err)
	}

	// Create a tampered data file (for case 2).
	tamperedFile := filepath.Join(dataDir, "tampered.txt")
	if err := os.WriteFile(
		tamperedFile,
		[]byte("tampered data content\n"), 0600); err != nil {
		t.Fatalf("write tampered data: %v", err)
	}

	// Export all keys to files (for VerifySignature to import).
	keyAlpha := filepath.Join(dataDir, "alpha.asc")
	keyBeta := filepath.Join(dataDir, "beta.asc")
	keySubkey := filepath.Join(dataDir, "subkey.asc")
	testExportKey(t, genHome, fpAlpha, keyAlpha)
	testExportKey(t, genHome, fpBeta, keyBeta)
	testExportKey(t, genHome, fpSubkey, keySubkey)

	// Sign test data with each key.
	sigAlpha := filepath.Join(dataDir, "sig-alpha.asc")
	sigBeta := filepath.Join(dataDir, "sig-beta.asc")
	sigSubkey := filepath.Join(dataDir, "sig-subkey.asc")
	testSign(t, genHome, fpAlpha, dataFile, sigAlpha)
	testSign(t, genHome, fpBeta, dataFile, sigBeta)
	testSign(t, genHome, fpSubkey, dataFile, sigSubkey)

	// Create combined signatures for multi-signer tests.
	sigAlphaData, _ := os.ReadFile(sigAlpha)
	sigBetaData, _ := os.ReadFile(sigBeta)

	// Alpha + Beta combined (for case 5).
	sigAlphaBeta := filepath.Join(dataDir, "sig-alpha-beta.asc")
	if err := os.WriteFile(
		sigAlphaBeta,
		append(sigAlphaData, sigBetaData...),
		0600); err != nil {
		t.Fatalf("write combined sig: %v", err)
	}

	// Alpha + Alpha combined (for case 4).
	sigAlphaAlpha := filepath.Join(dataDir,
		"sig-alpha-alpha.asc")
	if err := os.WriteFile(
		sigAlphaAlpha,
		append(sigAlphaData, sigAlphaData...),
		0600); err != nil {
		t.Fatalf("write double sig: %v", err)
	}

	// ── Case 1: Good sig from a pinned key → accept ────
	t.Run("pinned_key_accepts", func(t *testing.T) {
		pinned := map[string]bool{fpAlpha: true}
		result, err := VerifySignature(
			[]string{keyAlpha}, sigAlpha, dataFile, pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 1 {
			t.Fatalf("distinct = %d, want 1", distinct)
		}
	})

	// ── Case 2: Tampered file → BADSIG → reject ────────
	t.Run("tampered_file_badsig", func(t *testing.T) {
		pinned := map[string]bool{fpAlpha: true}
		result, err := VerifySignature(
			[]string{keyAlpha}, sigAlpha, tamperedFile, pinned)
		bad := result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bad {
			t.Fatal("expected BADSIG for tampered file")
		}
	})

	// ── Case 3: Good sig from UNPINNED key → reject ────
	t.Run("unpinned_key_rejects", func(t *testing.T) {
		// Alpha signed, but only Beta is pinned.
		pinned := map[string]bool{fpBeta: true}
		result, err := VerifySignature(
			[]string{keyAlpha}, sigAlpha, dataFile, pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 0 {
			t.Fatalf("distinct = %d, want 0 "+
				"(signature valid but not from pinned key)",
				distinct)
		}
	})

	// ── Case 4: Same key twice → counts as 1 ───────────
	//     → fails threshold 2
	t.Run("same_key_twice_counts_once", func(t *testing.T) {
		pinned := map[string]bool{fpAlpha: true}
		result, err := VerifySignature(
			[]string{keyAlpha},
			sigAlphaAlpha, dataFile, pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 1 {
			t.Fatalf("distinct = %d, want 1 "+
				"(same key signing twice should count once)",
				distinct)
		}
	})

	// ── Case 5: Two different pinned keys → clears 2 ───
	t.Run("two_pinned_keys_clears_threshold", func(t *testing.T) {
		pinned := map[string]bool{
			fpAlpha: true,
			fpBeta:  true,
		}
		result, err := VerifySignature(
			[]string{keyAlpha, keyBeta},
			sigAlphaBeta, dataFile, pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 2 {
			t.Fatalf("distinct = %d, want 2", distinct)
		}
	})

	// ── Case 6: Subkey-signed → matches primary FP ─────
	t.Run("subkey_signed_matches_primary", func(t *testing.T) {
		// fpSubkey is the PRIMARY fingerprint. The signing
		// subkey has a different fingerprint. VerifySignature
		// must match the VALIDSIG last field (primary), not
		// the first field (subkey).
		pinned := map[string]bool{fpSubkey: true}
		result, err := VerifySignature(
			[]string{keySubkey},
			sigSubkey, dataFile, pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 1 {
			t.Fatalf("distinct = %d, want 1 "+
				"(subkey-signed should match primary FP)",
				distinct)
		}
	})
}

// ── Clearsign test suite (Syncthing path) ───────────────
//
// VerifySignature with dataFile == "" verifies a CLEARSIGNED
// file (the format of Syncthing's sha256sum.txt.asc). These
// cases are the automated mirror of the live verification
// session of June 9 2026: a genuine v2.1.1 file produced
// VALIDSIG with the pinned primary fingerprint as the LAST
// field, and tampering with the checksum text inside the
// armor produced BADSIG with no VALIDSIG.

func TestVerifySignatureClearsign(t *testing.T) {
	if !gpgAvailable() {
		t.Skip("gpg not available — skipping")
	}

	genHome, err := os.MkdirTemp("", "vpn-test-gen-")
	if err != nil {
		t.Fatalf("create gen home: %v", err)
	}
	defer os.RemoveAll(genHome)

	fpAlpha := testGenKey(t, genHome, "ClearAlpha", false)
	fpBeta := testGenKey(t, genHome, "ClearBeta", false)

	dataDir, err := os.MkdirTemp("", "vpn-test-data-")
	if err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	defer os.RemoveAll(dataDir)

	// Checksum-list-shaped content, like sha256sum.txt.asc.
	dataFile := filepath.Join(dataDir, "sums.txt")
	if err := os.WriteFile(dataFile, []byte(
		"0123456789abcdef  syncthing-linux-amd64-v9.9.9.tar.gz\n"),
		0600); err != nil {
		t.Fatalf("write test data: %v", err)
	}

	keyAlpha := filepath.Join(dataDir, "alpha.asc")
	keyBeta := filepath.Join(dataDir, "beta.asc")
	testExportKey(t, genHome, fpAlpha, keyAlpha)
	testExportKey(t, genHome, fpBeta, keyBeta)

	clearAlpha := filepath.Join(dataDir, "sums-clear.asc")
	testClearsign(t, genHome, fpAlpha, dataFile, clearAlpha)

	// Tampered variant: modify the checksum text INSIDE the
	// clearsigned armor (the exact shape of the live June 9
	// negative test, which renamed the asset inside the file).
	clearBytes, err := os.ReadFile(clearAlpha)
	if err != nil {
		t.Fatalf("read clearsigned file: %v", err)
	}
	tampered := strings.Replace(string(clearBytes),
		"syncthing-linux-amd64", "syncthing-linux-tampered", 1)
	if tampered == string(clearBytes) {
		t.Fatal("tamper replacement did not apply")
	}
	tamperedFile := filepath.Join(dataDir, "sums-tampered.asc")
	if err := os.WriteFile(tamperedFile,
		[]byte(tampered), 0600); err != nil {
		t.Fatalf("write tampered clearsign: %v", err)
	}

	// ── Case 1: Clearsigned by pinned key → accept ─────
	t.Run("clearsign_pinned_key_accepts", func(t *testing.T) {
		pinned := map[string]bool{fpAlpha: true}
		result, err := VerifySignature(
			[]string{keyAlpha}, clearAlpha, "", pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 1 {
			t.Fatalf("distinct = %d, want 1", distinct)
		}
	})

	// ── Case 2: Tampered inside the armor → BADSIG ─────
	t.Run("clearsign_tampered_badsig", func(t *testing.T) {
		pinned := map[string]bool{fpAlpha: true}
		result, err := VerifySignature(
			[]string{keyAlpha}, tamperedFile, "", pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bad {
			t.Fatal("expected BADSIG for tampered clearsign")
		}
		if distinct != 0 {
			t.Fatalf("distinct = %d, want 0 "+
				"(tampered content must not count)", distinct)
		}
	})

	// ── Case 3: Clearsigned by UNPINNED key → reject ───
	t.Run("clearsign_unpinned_key_rejects", func(t *testing.T) {
		// Alpha clearsigned, but only Beta is pinned.
		pinned := map[string]bool{fpBeta: true}
		result, err := VerifySignature(
			[]string{keyAlpha}, clearAlpha, "", pinned)
		distinct, bad := result.Signers, result.Bad
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bad {
			t.Fatal("unexpected BADSIG")
		}
		if distinct != 0 {
			t.Fatalf("distinct = %d, want 0 "+
				"(signature valid but not from pinned key)",
				distinct)
		}
	})
}
