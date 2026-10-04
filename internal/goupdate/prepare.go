package goupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Entry struct{ Source, Target string }
type Plan struct {
	Directory  string
	Helper     string
	Entries    []Entry
	Executable string
	Arguments  []string
	ParentPID  int
}

func download(ctx context.Context, client *http.Client, a Asset, destination string, progress func(int64)) error {
	if !validDigest(a.Digest) || a.Size <= 0 || a.Size > maxArchive {
		return fmt.Errorf("update has no valid SHA256 digest or size")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Water-Updater")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", res.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	buffer := make([]byte, 64<<10)
	reader := io.LimitReader(res.Body, a.Size+1)
	var total int64
	for {
		n, e := reader.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > a.Size {
				return fmt.Errorf("archive exceeds advertised size")
			}
			if _, err = writer.Write(buffer[:n]); err != nil {
				return err
			}
			if progress != nil {
				progress(total)
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if total != a.Size {
		return fmt.Errorf("incomplete download")
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:")) {
		return fmt.Errorf("update SHA256 mismatch")
	}
	return file.Sync()
}

// Only regular files and directories are accepted. This package contains no
// framework symlinks; refusing links keeps extraction independent of archive
// order and prevents writes outside the private staging directory.
func extractFile(root, name string, mode os.FileMode, reader io.Reader, size int64) error {
	if strings.Contains(name, "\\") || filepath.IsAbs(name) || name == "" {
		return fmt.Errorf("unsafe archive path")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe archive path")
	}
	path := filepath.Join(root, clean)
	if mode.IsDir() {
		return os.MkdirAll(path, 0755)
	}
	if !mode.IsRegular() {
		return fmt.Errorf("archive contains a link or special file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()&0755)
	if err != nil {
		return err
	}
	n, copyErr := io.CopyN(f, reader, size)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		return fmt.Errorf("incomplete archive member")
	}
	return nil
}

func unpack(archive, root, platform string) error {
	const maxExpanded = 2 << 30
	var expanded int64
	var count int
	if platform == "darwin" {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			count++
			if count > 20000 || f.UncompressedSize64 > maxExpanded {
				return fmt.Errorf("archive limits exceeded")
			}
			expanded += int64(f.UncompressedSize64)
			if expanded > maxExpanded {
				return fmt.Errorf("archive limits exceeded")
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = extractFile(root, f.Name, f.Mode(), r, int64(f.UncompressedSize64))
			r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer z.Close()
	t := tar.NewReader(z)
	for {
		h, err := t.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		count++
		expanded += h.Size
		if count > 20000 || h.Size < 0 || expanded > maxExpanded {
			return fmt.Errorf("archive limits exceeded")
		}
		mode := os.FileMode(h.Mode) & 0777
		switch h.Typeflag {
		case tar.TypeDir:
			mode |= os.ModeDir
		case tar.TypeReg, tar.TypeRegA:
		default:
			return fmt.Errorf("archive contains a link or special file")
		}
		if err := extractFile(root, h.Name, mode, t, h.Size); err != nil {
			return err
		}
	}
}

func installation(executable, variant, platform string) (string, error) {
	gui, _, app := names(variant)
	if filepath.Base(executable) != gui {
		return "", fmt.Errorf("in-app updates require a packaged Water installation")
	}
	dir := filepath.Dir(executable)
	if platform == "darwin" {
		bundle := filepath.Dir(filepath.Dir(dir))
		if filepath.Base(dir) != "MacOS" || filepath.Base(filepath.Dir(dir)) != "Contents" || filepath.Base(bundle) != app {
			return "", fmt.Errorf("in-app updates require an installed Water app bundle")
		}
		// Never replace a mounted disk image or an App Translocation copy.
		if strings.HasPrefix(bundle, "/Volumes/") || strings.Contains(bundle, "/AppTranslocation/") {
			return "", fmt.Errorf("move Water to Applications before updating")
		}
		return filepath.Dir(bundle), nil
	}
	return dir, nil
}

func verifyBundle(ctx context.Context, current, candidate, variant, version string) error {
	// Require the same Apple certificate identity and bundle identifier, rather
	// than accepting any valid (or ad-hoc) signature.
	display, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "-r-", current).CombinedOutput()
	if err != nil {
		return fmt.Errorf("current app must have a distribution signature to update")
	}
	requirement := ""
	for _, line := range strings.Split(string(display), "\n") {
		if strings.HasPrefix(line, "designated => ") {
			requirement = strings.TrimPrefix(line, "designated => ")
		}
	}
	if !strings.Contains(requirement, "anchor apple") || !strings.Contains(requirement, "certificate") {
		return fmt.Errorf("unsigned and ad-hoc apps cannot install updates")
	}
	if err = exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "-R", "="+requirement, candidate).Run(); err != nil {
		return fmt.Errorf("update app signature does not match this installation")
	}
	_, _, app := names(variant)
	id := "dev.water.terminal"
	if app == "Water Dev.app" {
		id += ".dev"
	}
	for key, want := range map[string]string{"CFBundleIdentifier": id, "CFBundleShortVersionString": version} {
		out, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", key, "raw", "-o", "-", filepath.Join(candidate, "Contents", "Info.plist")).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			return fmt.Errorf("update bundle identity or version mismatch")
		}
	}
	return nil
}

func verifyBinary(ctx context.Context, path, variant, version string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("update executable is missing or not executable")
	}
	out, err := exec.CommandContext(ctx, path, "ctl", "version").Output()
	if err != nil {
		return fmt.Errorf("update executable cannot run on this machine")
	}
	var identity struct {
		Variant string `json:"build_variant"`
		Version string `json:"client_version"`
	}
	if json.Unmarshal(out, &identity) != nil || identity.Variant != variant || identity.Version != version {
		return fmt.Errorf("update executable identity or version mismatch")
	}
	return nil
}

func prepare(ctx context.Context, client *http.Client, c Candidate, executable, variant, platform string, args []string, progress func(int64)) (_ *Plan, err error) {
	gui, server, app := names(variant)
	parent, err := installation(executable, variant, platform)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, "."+gui+"-update-")
	if err != nil {
		return nil, fmt.Errorf("installation directory is not writable: %w", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(stage)
		}
	}()
	archive := filepath.Join(stage, "archive")
	if err = download(ctx, client, c.Asset, archive, progress); err != nil {
		return nil, err
	}
	extracted := filepath.Join(stage, "extracted")
	if err = os.Mkdir(extracted, 0700); err != nil {
		return nil, err
	}
	if err = unpack(archive, extracted, platform); err != nil {
		return nil, err
	}
	plan := &Plan{Directory: stage, Helper: filepath.Join(filepath.Dir(executable), helperName(variant)), Executable: executable, Arguments: args, ParentPID: os.Getpid()}
	newGUI := filepath.Join(extracted, gui)
	if platform == "darwin" {
		current := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
		candidate := filepath.Join(extracted, app)
		if err = verifyBundle(ctx, current, candidate, variant, c.Version); err != nil {
			return nil, err
		}
		plan.Entries = []Entry{{candidate, current}}
		newGUI = filepath.Join(candidate, "Contents", "MacOS", gui)
	} else {
		for _, name := range []string{gui, server, helperName(variant)} {
			target := filepath.Join(parent, name)
			info, e := os.Lstat(target)
			if e != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("installed GUI and server must be regular files in the same directory")
			}
			plan.Entries = append(plan.Entries, Entry{filepath.Join(extracted, name), target})
		}
	}
	if err = verifyBinary(ctx, newGUI, variant, c.Version); err != nil {
		return nil, err
	}
	serverPath := filepath.Join(extracted, server)
	if platform == "darwin" {
		serverPath = filepath.Join(extracted, app, "Contents", "MacOS", server)
	}
	if info, e := os.Stat(serverPath); e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("update server is missing or not executable")
	}
	for flag, want := range map[string]string{"--build-variant": variant, "--version": "water-server " + c.Version} {
		out, e := exec.CommandContext(ctx, serverPath, flag).Output()
		if e != nil || strings.TrimSpace(string(out)) != want {
			return nil, fmt.Errorf("update server identity or version mismatch")
		}
	}
	helperPath := filepath.Join(filepath.Dir(serverPath), helperName(variant))
	out, e := exec.CommandContext(ctx, helperPath, "--version").Output()
	var helperIdentity struct {
		Variant string `json:"build_variant"`
		Version string `json:"version"`
	}
	if e != nil || json.Unmarshal(out, &helperIdentity) != nil || helperIdentity.Variant != variant || helperIdentity.Version != c.Version {
		return nil, fmt.Errorf("update installer identity or version mismatch")
	}
	os.Remove(archive)
	return plan, nil
}
