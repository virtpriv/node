package host

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func archiveFixture(t *testing.T, headers []tar.Header) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tr := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tr.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tr.Write(make([]byte, h.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpdateExtractionDoesNotFollowArchivePathsOrLinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "escape")
	archive := archiveFixture(t, []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Size: 3}, {Name: "vpn", Typeflag: tar.TypeReg, Size: 3}})
	if err := ExtractUpdateArchive(archive, dir, map[string]string{"vpn": "vpn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("archive wrote outside staging")
	}
	if _, err := os.Stat(filepath.Join(dir, "vpn")); err != nil {
		t.Fatal(err)
	}
	// Establish successful extraction before testing rejected archive members.
	for name, headers := range map[string][]tar.Header{
		"missing":   {{Name: "unrelated", Typeflag: tar.TypeReg, Size: 3}},
		"duplicate": {{Name: "vpn", Typeflag: tar.TypeReg, Size: 3}, {Name: "vpn", Typeflag: tar.TypeReg, Size: 3}},
		"symlink":   {{Name: "vpn", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		"hardlink":  {{Name: "vpn", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}},
	} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(dir, name)
			if err := os.Mkdir(dest, 0700); err != nil {
				t.Fatal(err)
			}
			if err := ExtractUpdateArchive(archiveFixture(t, headers), dest, map[string]string{"vpn": "vpn"}); err == nil {
				t.Fatal("accepted invalid executable")
			}
		})
	}
}
