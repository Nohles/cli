package cli

import (
	"archive/zip"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nohles/go-toolkit/pkg/streamer"
	"github.com/readium/cli/pkg/serve/auth"
)

func TestWriteBoundPortFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "runtime", "readium.port")
	address := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43125}

	if err := writeBoundPortFile(filePath, address); err != nil {
		t.Fatalf("writeBoundPortFile returned error: %v", err)
	}

	contents, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed reading port file: %v", err)
	}
	if string(contents) != "43125\n" {
		t.Fatalf("port file contents: got %q, want %q", contents, "43125\n")
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed stating port file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("port file mode: got %o, want 600", info.Mode().Perm())
	}
}

func TestNewManifestListIsLazy(t *testing.T) {
	called := 0
	handler, err := newManifestListWithBuilder("0.0.0.0:15080", t.TempDir(), auth.NewB64EncodedAuthProvider(), func(host, directory string) (*streamer.ManifestList, error) {
		called++
		return streamer.NewManifestList(), nil
	})
	if err != nil {
		t.Fatalf("newManifestListWithBuilder returned error: %v", err)
	}
	if handler == nil {
		t.Fatal("expected manifest list handler")
	}
	if called != 0 {
		t.Fatalf("manifest list builder was called before serving a request")
	}

	req := httptest.NewRequest(http.MethodGet, streamer.ManifestListPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, body %q", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Fatalf("manifest list builder calls: got %d, want 1", called)
	}
}

func TestLazyManifestListCachesBuildError(t *testing.T) {
	buildErr := errors.New("slow volume unavailable")
	called := 0
	handler := &lazyManifestList{
		build: func() (*streamer.ManifestList, error) {
			called++
			return nil, buildErr
		},
	}

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, streamer.ManifestListPath, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("unexpected status: got %d", rec.Code)
		}
	}
	if called != 1 {
		t.Fatalf("manifest list builder calls: got %d, want 1", called)
	}
}

func TestBuildManifestListIncludesRelativeDirectoryPublications(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	series := filepath.Join(library, "A Cadet Becomes a Prophet")
	if err := os.MkdirAll(series, 0o755); err != nil {
		t.Fatalf("failed creating series directory: %v", err)
	}
	writeTestCBZ(t, filepath.Join(series, "Chapter 1.cbz"))

	previousCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed reading cwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("failed changing cwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousCwd); err != nil {
			t.Fatalf("failed restoring cwd: %v", err)
		}
	})

	list, err := buildManifestList("localhost:15080", "library")
	if err != nil {
		t.Fatalf("buildManifestList returned error: %v", err)
	}

	items := list.Items()
	if len(items) != 1 {
		t.Fatalf("manifest list length: got %d (%#v), want 1", len(items), items)
	}
	if items[0].ManifestType != streamer.ManifestSourceDirectory {
		t.Fatalf("manifest type: got %q, want %q", items[0].ManifestType, streamer.ManifestSourceDirectory)
	}
	if items[0].DirFile != "A Cadet Becomes a Prophet" {
		t.Fatalf("Dir_File: got %q", items[0].DirFile)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("A Cadet Becomes a Prophet"))
	wantManifest := "http://localhost:15080/webpub/" + token + "/manifest.json"
	if items[0].Manifest != wantManifest {
		t.Fatalf("manifest URL: got %q, want %q", items[0].Manifest, wantManifest)
	}
}

func writeTestCBZ(t *testing.T, filePath string) {
	t.Helper()
	file, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("failed creating test cbz: %v", err)
	}
	defer file.Close()

	archive := zip.NewWriter(file)
	defer archive.Close()

	image, err := archive.Create("001.jpg")
	if err != nil {
		t.Fatalf("failed creating test image entry: %v", err)
	}
	if _, err := image.Write([]byte("fake image bytes")); err != nil {
		t.Fatalf("failed writing test image entry: %v", err)
	}
}
