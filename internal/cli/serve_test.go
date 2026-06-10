package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nohles/go-toolkit/pkg/streamer"
	"github.com/readium/cli/pkg/serve/auth"
)

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
