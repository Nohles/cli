package serve

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func compressionRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "book.epub"), buildMiniEPUB(t, "reader benchmark text", "more content"), 0600))
	s := NewServer(ServerConfig{}, Remote{LocalDirectory: dir})
	t.Cleanup(func() {
		cp, err := s.getPublication(authContext("book.epub"))
		if err == nil {
			cp.OnEvict()
			cp.Release()
		}
	})
	return s.Routes(), "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte("book.epub"))
}

func TestPublicationCompressionConcurrentNegotiation(t *testing.T) {
	router, base := compressionRouter(t)
	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, httptest.NewRequest("GET", base+"/manifest.json", nil))
	require.Equal(t, 200, plain.Code)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			encoding := "gzip"
			if i%2 == 0 {
				encoding = "gzip;q=0, identity"
			}
			req := httptest.NewRequest("GET", base+"/manifest.json", nil)
			req.Header.Set("Accept-Encoding", encoding)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("status %d", rec.Code)
				return
			}
			body := rec.Body.Bytes()
			if i%2 != 0 {
				if rec.Header().Get("Content-Encoding") != "gzip" {
					t.Errorf("gzip not negotiated: %v", rec.Header())
					return
				}
				reader, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Error(err)
					return
				}
				body, err = io.ReadAll(reader)
				reader.Close()
				if err != nil {
					t.Error(err)
					return
				}
			} else if rec.Header().Get("Content-Encoding") != "" {
				t.Error("q=0 was ignored")
			}
			if !bytes.Equal(body, plain.Body.Bytes()) {
				t.Error("negotiation changed publication bytes")
			}
		})
	}
	wg.Wait()
}

func TestPublicationRangeWithCompressionAccepted(t *testing.T) {
	router, base := compressionRouter(t)
	// A valid prefix of the EPUB's chapter; Range must stay effective even when
	// a client offers compression. The range refers to uncompressed bytes.
	req := httptest.NewRequest("GET", base+"/EPUB/ch01.xhtml", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.Len(t, rec.Body.Bytes(), 10)
	require.Equal(t, []byte("<?xml vers"), rec.Body.Bytes())
	head := httptest.NewRequest("HEAD", base+"/EPUB/ch01.xhtml", nil)
	head.Header = req.Header.Clone()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, head)
	require.Equal(t, 206, response.Code)
	require.Equal(t, rec.Header().Get("Content-Range"), response.Header().Get("Content-Range"))
	require.Empty(t, response.Body.Bytes())
}
