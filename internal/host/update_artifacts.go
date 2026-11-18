package host

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/virtpriv/node/internal/update/files"
)

// ExtractUpdateArchive only copies explicitly selected regular members. Tar
// paths, symlinks, ownership and permissions never select host destinations.
func ExtractUpdateArchive(archive, dest string, members map[string]string) error {
	if err := files.Directory(dest, 0700); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		total += h.Size
		if h.Size < 0 || total > 2<<30 {
			return errors.New("release archive exceeds extraction limit")
		}
		name, wanted := members[h.Name]
		if !wanted {
			continue
		}
		if seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 512<<20 || filepath.Base(name) != name {
			return fmt.Errorf("invalid or duplicate archive member %q", h.Name)
		}
		seen[h.Name] = true
		if err := files.Replace(filepath.Join(dest, name), 0700, func(w io.Writer) error { _, err := io.CopyN(w, tr, h.Size); return err }); err != nil {
			return err
		}
	}
	if len(seen) != len(members) {
		return errors.New("release archive is missing a required file")
	}
	return nil
}
