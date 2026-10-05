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
	"github.com/virtpriv/node/internal/update/protocol"
)

func UpdateArchive(c protocol.Component, version string) (string, map[string]string, error) {
	if !protocol.ValidVersion(c, version) {
		return "", nil, errors.New("invalid component version")
	}
	switch c {
	case protocol.Bitcoin:
		prefix := "bitcoin-" + version + "/bin/"
		return "bitcoin-" + version + "-x86_64-linux-gnu.tar.gz", map[string]string{prefix + "bitcoind": "bitcoind", prefix + "bitcoin-cli": "bitcoin-cli"}, nil
	case protocol.LND:
		prefix := "lnd-linux-amd64-v" + version + "/"
		return "lnd-linux-amd64-v" + version + ".tar.gz", map[string]string{prefix + "lnd": "lnd", prefix + "lncli": "lncli"}, nil
	case protocol.Syncthing:
		prefix := "syncthing-linux-amd64-v" + version + "/"
		return "syncthing-linux-amd64-v" + version + ".tar.gz", map[string]string{prefix + "syncthing": "syncthing"}, nil
	}
	return "", nil, errors.New("unsupported component")
}

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

func StageUpdateComponent(c protocol.Component, a protocol.Artifact, dir string) (map[string]string, error) {
	if err := files.Directory(dir, 0700); err != nil {
		return nil, err
	}
	archive, members, err := UpdateArchive(c, a.Version)
	if err != nil {
		return nil, err
	}
	if c == protocol.Bitcoin {
		// Earlier installers copied the entire Core bin directory. Keep any
		// already installed companion tools on the same release, without
		// introducing tools that are absent from this node.
		for _, name := range []string{"bitcoin-tx", "bitcoin-wallet", "bitcoin-util", "bitcoin-qt"} {
			if err := files.Check(filepath.Join("/usr/local/bin", name), false); err == nil {
				members["bitcoin-"+a.Version+"/bin/"+name] = name
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	switch c {
	case protocol.Bitcoin:
		err = DownloadBitcoinCore(a.Version, dir)
		if err == nil {
			err = VerifyBitcoinCore(dir)
		}
	case protocol.LND:
		err = DownloadLND(a.Version, dir)
		if err == nil {
			err = VerifyLND(a.Version, dir)
		}
	case protocol.Syncthing:
		err = downloadSyncthing(a.Version, dir)
		if err == nil {
			err = verifySyncthing(a.Version, dir)
		}
	}
	if err != nil {
		return nil, err
	}
	hash, err := files.Hash(filepath.Join(dir, archive))
	if err != nil {
		return nil, err
	}
	if hash != a.SHA256 {
		return nil, errors.New("upstream archive differs from the tested VPN release")
	}
	if err := ExtractUpdateArchive(filepath.Join(dir, archive), filepath.Join(dir, "bin"), members); err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for _, name := range members {
		hash, err := files.Hash(filepath.Join(dir, "bin", name))
		if err != nil {
			return nil, err
		}
		hashes[name] = hash
	}
	return hashes, nil
}

// UpdateBinaryNames is a closed destination allowlist, including companion
// tools retained by older Core installations.
func UpdateBinaryNames(c protocol.Component) []string {
	switch c {
	case protocol.Bitcoin:
		return []string{"bitcoind", "bitcoin-cli", "bitcoin-tx", "bitcoin-wallet", "bitcoin-util", "bitcoin-qt"}
	case protocol.LND:
		return []string{"lnd", "lncli"}
	case protocol.Syncthing:
		return []string{"syncthing"}
	}
	return nil
}
