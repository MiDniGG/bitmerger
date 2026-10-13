// Package bw runs the official Bitwarden CLI.
//
// BitMerger does not install anything system-wide: the CLI is downloaded once
// from Bitwarden's GitHub releases into the user's cache directory, verified
// against a pinned SHA-256, and only used from there.
package bw

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Version is the pinned Bitwarden CLI release (GPL "oss" build).
const Version = "2026.9.1"

// checksums of bw-oss-<platform>-<Version>.zip, from the GitHub release.
var checksums = map[string]string{
	"linux":       "b90c0fc86bcf11c0b194aa3f0a89344738af0512cb267c39ad3250c5edd71663",
	"linux-arm64": "6c2765f91d5bbba9864195c9a304a683bcc0bfa39cfd85ce9423142631e111a6",
	"macos":       "964f3da8577c5385bb1b8e591f70782a4cc933a4b68c100815cda23c5cfc092d",
	"macos-arm64": "eb2ab9821e462da6e07d2e91ccd92e54cd4d7debd5d62eea49affdf0cd1f3249",
	"windows":     "c39d346239d926ad7955d1ee7acddf557ff216f3186b5383f40acc402f1adefe",
}

func platform() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "macos-arm64", nil
	case "darwin/amd64":
		return "macos", nil
	case "linux/amd64":
		return "linux", nil
	case "linux/arm64":
		return "linux-arm64", nil
	case "windows/amd64", "windows/arm64":
		return "windows", nil
	}
	return "", fmt.Errorf("no Bitwarden CLI build for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "bw.exe"
	}
	return "bw"
}

// BaseDir is BitMerger's folder in the user cache directory.
func BaseDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "BitMerger"), nil
}

// Installed returns the path of the CLI if it is already downloaded.
// BITMERGER_BW overrides the path (for development).
func Installed() (string, bool) {
	if p := os.Getenv("BITMERGER_BW"); p != "" {
		return p, true
	}
	base, err := BaseDir()
	if err != nil {
		return "", false
	}
	p := filepath.Join(base, "bw-"+Version, binaryName())
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

// Progress reports download progress in bytes.
type Progress func(done, total int64)

// Download fetches, verifies and unpacks the CLI. It returns the binary path.
func Download(progress Progress) (string, error) {
	if p, ok := Installed(); ok {
		return p, nil
	}
	plat, err := platform()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("bw-oss-%s-%s.zip", plat, Version)
	url := fmt.Sprintf("https://github.com/bitwarden/clients/releases/download/cli-v%s/%s", Version, name)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", name, resp.Status)
	}
	var buf bytes.Buffer
	reader := io.TeeReader(resp.Body, &buf)
	chunk := make([]byte, 256*1024)
	var done int64
	for {
		n, err := reader.Read(chunk)
		done += int64(n)
		if progress != nil {
			progress(done, resp.ContentLength)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256(buf.Bytes())
	if got := hex.EncodeToString(sum[:]); got != checksums[plat] {
		return "", fmt.Errorf("checksum mismatch for %s: got %s", name, got)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return "", err
	}
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "bw-"+Version)
	tmp, err := os.MkdirTemp(base, ".bw-download-")
	if err != nil {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", err
		}
		if tmp, err = os.MkdirTemp(base, ".bw-download-"); err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(tmp)
	found := false
	for _, f := range zr.File {
		if filepath.Base(f.Name) != binaryName() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(filepath.Join(tmp, binaryName()), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return "", err
		}
		found = true
	}
	if !found {
		return "", fmt.Errorf("%s not found in %s", binaryName(), name)
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, binaryName()), nil
}
