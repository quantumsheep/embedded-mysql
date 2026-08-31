// Package download fetches the official MySQL binary tarball and extracts it into the cache.
package download

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ulikunitz/xz"
)

func tarballName(version string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		architecture := "arm64"
		if runtime.GOARCH == "amd64" {
			architecture = "x86_64"
		}

		return fmt.Sprintf("mysql-%s-macos15-%s.tar.gz", version, architecture), nil
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return fmt.Sprintf("mysql-%s-linux-glibc2.28-x86_64-minimal.tar.xz", version), nil
		case "arm64":
			return fmt.Sprintf("mysql-%s-linux-glibc2.28-aarch64.tar.xz", version), nil
		}
	}

	return "", fmt.Errorf("embedded-mysql: unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
}

// The "Downloads" host serves only the most recent release of a series. The "archives" host serves all releases.
func downloadURLs(version, name string) []string {
	parts := strings.SplitN(version, ".", 3)

	series := parts[0]
	if len(parts) > 1 {
		series = parts[0] + "." + parts[1]
	}

	return []string{
		fmt.Sprintf("https://cdn.mysql.com/Downloads/MySQL-%s/%s", series, name),
		fmt.Sprintf("https://cdn.mysql.com/archives/mysql-%s/%s", series, name),
	}
}

// Options selects the tarball to fetch and the cache to fill.
type Options struct {
	Version   string
	CachePath string
	BinaryURL string
	Logger    io.Writer
}

// EnsureBinaries downloads and extracts the MySQL binaries when the cache does not have them. It returns the base directory that contains bin/mysqld.
func EnsureBinaries(options Options) (string, error) {
	cacheDir := options.CachePath

	if cacheDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}

		cacheDir = filepath.Join(homeDir, ".embedded-mysql")
	}

	name := filepath.Base(options.BinaryURL)
	urls := []string{options.BinaryURL}

	if options.BinaryURL == "" {
		var err error

		name, err = tarballName(options.Version)
		if err != nil {
			return "", err
		}

		urls = downloadURLs(options.Version, name)
	}

	base := filepath.Join(cacheDir, strings.TrimSuffix(strings.TrimSuffix(name, ".tar.gz"), ".tar.xz"))
	marker := filepath.Join(base, ".embedded-mysql-ok")

	_, err := os.Stat(marker)
	if err == nil {
		return base, nil
	}

	err = os.MkdirAll(cacheDir, 0o755)
	if err != nil {
		return "", err
	}

	tarballPath := filepath.Join(cacheDir, name)

	_, err = os.Stat(tarballPath)
	if err != nil {
		err = downloadToFile(urls, cacheDir, tarballPath, options.Logger)
		if err != nil {
			return "", err
		}
	}

	archiveFile, err := os.Open(tarballPath)
	if err != nil {
		return "", err
	}

	defer func() { _ = archiveFile.Close() }()

	err = os.RemoveAll(base)
	if err != nil {
		return "", err
	}

	err = extract(archiveFile, strings.HasSuffix(name, ".xz"), base)
	if err != nil {
		return "", err
	}

	err = os.WriteFile(marker, nil, 0o644)
	if err != nil {
		return "", err
	}

	return base, nil
}

func downloadToFile(urls []string, cacheDir string, tarballPath string, logger io.Writer) error {
	temporaryFile, err := os.CreateTemp(cacheDir, "download-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(temporaryFile.Name()) }()

	err = download(urls, temporaryFile, logger)
	if err != nil {
		_ = temporaryFile.Close()

		return err
	}

	err = temporaryFile.Close()
	if err != nil {
		return err
	}

	return os.Rename(temporaryFile.Name(), tarballPath)
}

func download(urls []string, destination io.Writer, logger io.Writer) error {
	var lastError error

	for _, url := range urls {
		fmt.Fprintf(logger, "embedded-mysql: downloading %s\n", url)

		response, err := http.Get(url)
		if err != nil {
			lastError = err

			continue
		}

		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			lastError = fmt.Errorf("embedded-mysql: %s returned status %s", url, response.Status)

			continue
		}

		_, err = io.Copy(destination, response.Body)
		_ = response.Body.Close()

		return err
	}

	return lastError
}

// extract writes bin/mysqld, the dylibs of bin/, lib/ and share/ from the tarball into base. It strips the top-level directory and skips the rest of the archive to save disk space.
func extract(archive io.Reader, isXZ bool, base string) error {
	var reader io.Reader

	var err error

	if isXZ {
		reader, err = xz.NewReader(archive)
	} else {
		reader, err = gzip.NewReader(archive)
	}

	if err != nil {
		return err
	}

	tarReader := tar.NewReader(reader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return err
		}

		_, relativePath, found := strings.Cut(filepath.ToSlash(header.Name), "/")
		if !found || relativePath == "" {
			continue
		}

		// mysqld on macOS loads dylibs from bin/ via @loader_path.
		keep := relativePath == "bin/mysqld" ||
			(strings.HasPrefix(relativePath, "bin/") && strings.Contains(relativePath, ".dylib")) ||
			strings.HasPrefix(relativePath, "lib/") ||
			strings.HasPrefix(relativePath, "share/")
		if !keep {
			continue
		}

		if strings.Contains(relativePath, "..") {
			return fmt.Errorf("embedded-mysql: unsafe path in archive: %s", header.Name)
		}

		destinationPath := filepath.Join(base, filepath.FromSlash(relativePath))

		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(destinationPath, 0o755)
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			err = os.MkdirAll(filepath.Dir(destinationPath), 0o755)
			if err != nil {
				return err
			}

			err = os.Symlink(header.Linkname, destinationPath)
			if err != nil && !os.IsExist(err) {
				return err
			}
		case tar.TypeReg:
			err = os.MkdirAll(filepath.Dir(destinationPath), 0o755)
			if err != nil {
				return err
			}

			err = writeFile(destinationPath, tarReader, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
		}
	}
}

func writeFile(path string, content io.Reader, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}

	_, err = io.Copy(file, content)
	if err != nil {
		_ = file.Close()

		return err
	}

	return file.Close()
}
