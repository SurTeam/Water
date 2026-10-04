// Package goupdate implements explicit, user initiated updates from Water's
// GitHub Releases. It never changes workspace models or runs I/O on a UI frame.
package goupdate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const releasesURL = "https://api.github.com/repos/SurTeam/Water/releases?per_page=100"
const maxArchive = 512 << 20

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type Release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Candidate struct {
	Version string `json:"version"`
	Asset   Asset  `json:"asset"`
}

// Numeric dotted versions and optional prerelease identifiers follow SemVer
// precedence; build metadata does not affect ordering.
func versionParts(s string) ([]uint64, []string, error) {
	metadata := strings.SplitN(s, "+", 2)
	if len(metadata) == 2 {
		for _, part := range strings.Split(metadata[1], ".") {
			if !validIdentifier(part) {
				return nil, nil, fmt.Errorf("invalid build metadata")
			}
		}
	}
	s = metadata[0]
	v := strings.SplitN(s, "-", 2)
	parts := strings.Split(v[0], ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("invalid version %q", s)
	}
	n := make([]uint64, 3)
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return nil, nil, fmt.Errorf("invalid version %q", s)
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil, nil, fmt.Errorf("invalid version %q", s)
			}
		}
		var err error
		n[i], err = strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, nil, err
		}
	}
	var pre []string
	if len(v) == 2 {
		pre = strings.Split(v[1], ".")
		for _, p := range pre {
			if !validIdentifier(p) || numericIdentifier(p) && len(p) > 1 && p[0] == '0' {
				return nil, nil, fmt.Errorf("invalid prerelease")
			}
		}
	}
	return n, pre, nil
}

func validIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

func numericIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func compareVersion(a, b string) (int, error) {
	x, xp, err := versionParts(a)
	if err != nil {
		return 0, err
	}
	y, yp, err := versionParts(b)
	if err != nil {
		return 0, err
	}
	for i := range x {
		if x[i] > y[i] {
			return 1, nil
		}
		if x[i] < y[i] {
			return -1, nil
		}
	}
	if len(xp) == 0 && len(yp) == 0 {
		return 0, nil
	}
	if len(xp) == 0 {
		return 1, nil
	}
	if len(yp) == 0 {
		return -1, nil
	}
	for i := 0; i < len(xp) && i < len(yp); i++ {
		if xp[i] == yp[i] {
			continue
		}
		xn, yn := numericIdentifier(xp[i]), numericIdentifier(yp[i])
		if xn && yn {
			if len(xp[i]) > len(yp[i]) || len(xp[i]) == len(yp[i]) && xp[i] > yp[i] {
				return 1, nil
			}
			return -1, nil
		}
		if xn {
			return -1, nil
		}
		if yn {
			return 1, nil
		}
		if xp[i] > yp[i] {
			return 1, nil
		}
		return -1, nil
	}
	if len(xp) > len(yp) {
		return 1, nil
	}
	if len(xp) < len(yp) {
		return -1, nil
	}
	return 0, nil
}

func names(variant string) (gui, server, app string) {
	if variant == "dev" {
		return "water-dev", "water-srv-dev", "Water Dev.app"
	}
	return "water", "water-server", "Water.app"
}

func helperName(variant string) string {
	if variant == "dev" {
		return "water-update-dev"
	}
	return "water-update"
}

func assetName(variant, version, platform, arch string) string {
	gui, _, app := names(variant)
	if platform == "darwin" {
		return strings.TrimSuffix(app, ".app") + "-" + version + "-macOS-" + arch + ".zip"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	if arch == "amd64" {
		arch = "x86_64"
	}
	return gui + "-" + version + "-" + arch + "-linux.tar.gz"
}

func validDigest(d string) bool {
	if !strings.HasPrefix(d, "sha256:") {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(d, "sha256:"))
	return err == nil && len(b) == 32
}

func selectRelease(releases []Release, current, variant, platform, arch string) (*Candidate, error) {
	if variant != "dev" && variant != "release" {
		return nil, fmt.Errorf("unsupported build variant")
	}
	if platform != "darwin" && platform != "linux" {
		return nil, fmt.Errorf("unsupported platform")
	}
	if arch != "arm64" && arch != "amd64" {
		return nil, fmt.Errorf("unsupported architecture")
	}
	if _, _, err := versionParts(current); err != nil {
		return nil, err
	}
	prefix := "v"
	if variant == "dev" {
		prefix = "dev-"
	}
	var result *Candidate
	for _, r := range releases {
		if r.Draft || variant == "release" && r.Prerelease || !strings.HasPrefix(r.Tag, prefix) {
			continue
		}
		v := strings.TrimPrefix(r.Tag, prefix)
		// Existing dev tags use dev-YYYYMMDD-HHMM. Their product version is
		// carried by the architecture-specific asset, not by the tag.
		if variant == "dev" {
			if _, _, err := versionParts(v); err != nil {
				v = ""
				for _, a := range r.Assets {
					name := strings.ReplaceAll(a.Name, " ", ".")
					start, end := "Water.Dev-", "-macOS-"+arch+".zip"
					if platform == "linux" {
						linuxArch := "x86_64"
						if arch == "arm64" {
							linuxArch = "aarch64"
						}
						start, end = "water-dev-", "-"+linuxArch+"-linux.tar.gz"
					}
					if strings.HasPrefix(name, start) && strings.HasSuffix(name, end) {
						v = strings.TrimSuffix(strings.TrimPrefix(name, start), end)
						break
					}
				}
			}
		}
		cmp, err := compareVersion(v, current)
		if err != nil || cmp <= 0 {
			continue
		}
		if result != nil {
			cmp, _ = compareVersion(v, result.Version)
			if cmp <= 0 {
				continue
			}
		}
		for _, a := range r.Assets {
			// GitHub normalizes spaces in uploaded asset names to periods.
			want := strings.ReplaceAll(assetName(variant, v, platform, arch), " ", ".")
			if strings.ReplaceAll(a.Name, " ", ".") != want {
				continue
			}
			u, e := url.Parse(a.URL)
			if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/SurTeam/Water/releases/download/"+r.Tag+"/") {
				continue
			}
			if a.Size <= 0 || a.Size > maxArchive || !validDigest(a.Digest) {
				continue
			}
			result = &Candidate{Version: v, Asset: a}
			break
		}
	}
	return result, nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("insecure redirect")
		}
		return nil
	}}
}

func fetchReleases(ctx context.Context, client *http.Client, endpoint string) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Water-Updater")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var releases []Release
	err = json.Unmarshal(data, &releases)
	return releases, err
}
