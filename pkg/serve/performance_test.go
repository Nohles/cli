package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/readium/cli/internal/benchfixture"
	"github.com/readium/cli/pkg/serve/auth"
	"github.com/stretchr/testify/require"
)

type discardResponse struct {
	header http.Header
	status int
}

func (w *discardResponse) Header() http.Header    { return w.header }
func (w *discardResponse) WriteHeader(status int) { w.status = status }
func (w *discardResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(p), nil
}
func (w *discardResponse) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = 200
	}
	return io.Copy(io.Discard, r)
}

// BenchmarkServing exercises the real router, auth, fingerprint, publication
// cache, serialization and asset streaming, without network/client allocation.
func BenchmarkServing(b *testing.B) {
	dir := b.TempDir()
	files, err := benchfixture.Library(dir)
	require.NoError(b, err)
	for _, file := range files {
		b.Run(file, func(b *testing.B) {
			s := NewServer(ServerConfig{ImageDimensionProbeWorkers: 4, AudioEmbeddedChapters: true, AudioParsingConcurrency: 8, AudioParsingCacheBlockSize: 256 << 10, AudioParsingCacheRetain: true}, Remote{LocalDirectory: dir})
			router := s.Routes()
			base := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte(file))
			req := httptest.NewRequest("GET", base+"/manifest.json", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(b, 200, rec.Code, rec.Body.String())
			// Close the warmed publication after the benchmark, including file handles.
			// Cache keys include the local file fingerprint.
			b.Cleanup(func() {
				// A retained publication returned here is still owned by the cache.
				cp, err := s.getPublication(authContext(file))
				if err == nil {
					cp.OnEvict()
					cp.Release()
				}
			})
			b.Run("manifest", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					w := &discardResponse{header: make(http.Header)}
					router.ServeHTTP(w, req)
					if w.status != 200 {
						b.Fatalf("status %d", w.status)
					}
				}
			})
			// Resolve fixture reading order rather than depending on an EPUB layout.
			var m struct{ ReadingOrder []struct{ Href string } }
			require.NoError(b, json.Unmarshal(rec.Body.Bytes(), &m))
			require.NotEmpty(b, m.ReadingOrder)
			idx := len(m.ReadingOrder) / 2
			req = httptest.NewRequest("GET", base+"/"+m.ReadingOrder[idx].Href, nil)
			if file == "audio.wav" {
				req.Header.Set("Range", "bytes=0-65535")
			}
			b.Run("asset", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					w := &discardResponse{header: make(http.Header)}
					router.ServeHTTP(w, req)
					if w.status != 200 && w.status != 206 {
						b.Fatalf("status %d", w.status)
					}
				}
			})
		})
	}
}

func authContext(file string) context.Context {
	return context.WithValue(context.Background(), auth.ContextPathKey, file)
}

// BenchmarkPublicationCache bypasses HTTP middleware, distinguishing the
// toolkit's publication-open cost from warm-cache lookup and route overhead.
func BenchmarkPublicationCache(b *testing.B) {
	dir := b.TempDir()
	files, err := benchfixture.Library(dir)
	require.NoError(b, err)
	for _, file := range files {
		b.Run(file, func(b *testing.B) {
			s := NewServer(ServerConfig{ImageDimensionProbeWorkers: 4, AudioEmbeddedChapters: true, AudioParsingConcurrency: 8, AudioParsingCacheBlockSize: 256 << 10, AudioParsingCacheRetain: true}, Remote{LocalDirectory: dir})
			ctx := authContext(file)
			u, err := url.AbsoluteURLFromString("file:///" + file)
			require.NoError(b, err)
			key := appendFingerprint(u.String(), u, dir)
			b.Cleanup(func() { s.lfu.Del(key) })
			cp, err := s.getPublication(ctx)
			require.NoError(b, err)
			cp.Release()
			b.Run("hit", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					cp, err := s.getPublication(ctx)
					if err != nil {
						b.Fatal(err)
					}
					cp.Release()
				}
			})
			b.Run("open_close", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					s.lfu.Del(key)
					cp, err := s.getPublication(ctx)
					if err != nil {
						b.Fatal(err)
					}
					cp.Release()
				}
			})
		})
	}
}
