package serve

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	toolkitURL "github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/readium/cli/internal/benchfixture"
)

type directoryDiscardResponse struct {
	header http.Header
	status int
}

func (w *directoryDiscardResponse) Header() http.Header    { return w.header }
func (w *directoryDiscardResponse) WriteHeader(status int) { w.status = status }
func (w *directoryDiscardResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(p), nil
}
func (w *directoryDiscardResponse) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = 200
	}
	return io.Copy(io.Discard, r)
}

// BenchmarkDirectoryComics measures the 100-chapter parent, not flattened image
// pages. READIUM_DIRECTORY_LIBRARY reads the prepared local/NAS fixture in place.
func BenchmarkDirectoryComics(b *testing.B) {
	root := os.Getenv("READIUM_DIRECTORY_LIBRARY")
	if root == "" {
		root = filepath.Join(b.TempDir(), "library")
		if _, err := benchfixture.DirectoryComics(root); err != nil {
			b.Fatal(err)
		}
	}
	names := make([]string, 100)
	for i := range names {
		names[i] = url.PathEscape(fmt.Sprintf("Chapter %03d.cbz", i+1))
	}
	for _, mode := range []string{"plain", "selected"} {
		b.Run(mode, func(b *testing.B) {
			s := NewServer(ServerConfig{ImageDimensionProbeWorkers: 4}, Remote{LocalDirectory: root})
			router := s.Routes()
			target := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte("Series")) + "/manifest.json"
			u, err := toolkitURL.AbsoluteURLFromString("file:///Series")
			if err != nil {
				b.Fatal(err)
			}
			key := u.String()
			if mode == "selected" {
				payload, err := json.Marshal(map[string]any{"publication": map[string]string{"path": "Series"}, "selection": map[string]any{"version": 1, "readingOrder": names}})
				if err != nil {
					b.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/_readium/directory-selections", bytes.NewReader(payload))
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != 201 {
					b.Fatalf("registration %d: %s", rec.Code, rec.Body.String())
				}
				var registration struct {
					ID     string `json:"id"`
					Digest string `json:"digest"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &registration); err != nil {
					b.Fatal(err)
				}
				target += "?readiumSelection=" + url.QueryEscape(registration.ID)
				key += "\x00directory-selection=" + registration.Digest
			}
			key = appendFingerprint(key, u, root)
			b.Cleanup(func() { s.lfu.Del(key) })
			req := httptest.NewRequest("GET", target, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != 200 {
				b.Fatalf("manifest %d: %s", rec.Code, rec.Body.String())
			}
			var m struct {
				ReadingOrder []struct {
					Href string `json:"href"`
				} `json:"readingOrder"`
				TOC []any `json:"toc"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
				b.Fatal(err)
			}
			if len(m.ReadingOrder) != 100 || len(m.TOC) != 100 {
				b.Fatal("directory manifest lost chapters")
			}
			for i, link := range m.ReadingOrder {
				if link.Href != names[i] {
					b.Fatal("chapter order changed")
				}
			}
			for _, name := range []string{"warm_manifest", "cold_open_close"} {
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if name == "cold_open_close" {
							s.lfu.Del(key)
						}
						response := &directoryDiscardResponse{header: make(http.Header)}
						router.ServeHTTP(response, req)
						if response.status != 200 {
							b.Fatalf("status %d", response.status)
						}
					}
				})
			}
		})
	}
}
