package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nohles/go-toolkit/pkg/util/url"
)

// publicationFingerprint contributes a content-aware component to the
// publication cache key so a cached *pub.Publication can never outlive the
// bytes it was parsed from.
//
//   - Local files: size + nanosecond modification time. Any write to the file
//     (in-place edit, replace via rename) changes at least one of them, so a
//     stale cached publication is bypassed on the next request and the old
//     entry simply expires from the TinyLFU.
//   - Remote publications: the URL itself stays the only key component —
//     statting remote objects on every request would defeat caching entirely,
//     and presigned URLs already rotate their query strings.
//
// A fingerprint failure never fails the request; it degrades to no
// component, which preserves the pre-existing URL-keyed behavior.
func publicationFingerprint(u url.AbsoluteURL, localDir string) string {
	if !u.IsFile() {
		return ""
	}
	// u is resolved against file:/// (root-relative), so u.Path() alone is
	// not the on-disk path; anchor it under the served directory like
	// getPublication does when handing paths to the streamer.
	path := filepath.Join(localDir, filepath.FromSlash(strings.TrimPrefix(u.Path(), "/")))
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:8])
}

// appendFingerprint appends the content-aware key component for u to cacheKey.
func appendFingerprint(cacheKey string, u url.AbsoluteURL, localDir string) string {
	fp := publicationFingerprint(u, localDir)
	if fp == "" {
		return cacheKey
	}
	if strings.Contains(cacheKey, "\x00fp=") {
		// Already fingerprinted (e.g. re-entry); leave untouched.
		return cacheKey
	}
	return cacheKey + "\x00fp=" + fp
}
