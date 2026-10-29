package main

import (
	"bytes"
	"github.com/readium/cli/internal/benchfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRequestDrainsLargeBodiesWithBoundedCapture(t *testing.T) {
	data := bytes.Repeat([]byte("reader\n"), (9<<20)/7+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, bytes.NewReader(data))
	}))
	defer server.Close()
	p := &process{client: server.Client(), base: server.URL}
	for _, capture := range []bool{false, true} {
		body, sample, err := p.request("/media", "", capture)
		if err != nil {
			t.Fatal(err)
		}
		if sample.Bytes != int64(len(data)) || sample.Status != 200 {
			t.Fatalf("incomplete response: %+v", sample)
		}
		if sample.FirstByteMS <= 0 || sample.CompleteMS < sample.FirstByteMS {
			t.Fatalf("invalid timings: %+v", sample)
		}
		expected := 0
		if capture {
			expected = 8 << 20
		}
		if len(body) != expected || !bytes.Equal(body, data[:expected]) {
			t.Fatalf("capture size: got %d, want %d", len(body), expected)
		}
	}
}

func TestWorkloadRejectsErrorsAndAcceptsClampedRanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "page.png", time.Time{}, bytes.NewReader([]byte("small page")))
	}))
	defer server.Close()
	p := &process{client: server.Client(), base: server.URL}
	if _, err := p.work(1, "comic", "range", "/page", "bytes=0-65535", 17, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := p.work(1, "comic", "missing", "/missing", "", 17, 4); err == nil {
		t.Fatal("error responses must not produce a successful benchmark")
	}
}

func TestSummaryUsesNearestRankPercentiles(t *testing.T) {
	// Reversed samples catch accidental order dependence. For twenty requests,
	// the nearest-rank p50 is the 10th value and p95 is the 19th.
	var samples []measurement
	for i := 20; i > 0; i-- {
		samples = append(samples, measurement{FirstByteMS: float64(i), CompleteMS: float64(i * 2), Bytes: 100, Status: 200})
	}
	result := summarize(samples)
	if result.Count != 20 || result.FirstByteP50MS != 10 || result.FirstByteP95MS != 19 || result.CompleteP50MS != 20 || result.CompleteP95MS != 38 || result.Bytes != 100 {
		t.Fatalf("incorrect percentiles: %+v", result)
	}
}

func TestPrepareLibraryRefusesExistingData(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLibrary(dir, "", ""); err == nil {
		t.Fatal("must refuse an existing directory")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("existing data changed: %q, %v", data, err)
	}
}

func TestPreparedLibraryMatchesBaselineFixtureHashes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixtures")
	files, err := prepareLibrary(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"cb3ba68b120d2ef9f07c5a87b5fbef1c566ccce0eb5b76afebc53dd2eb3f986f",
		"6015e12ea1619f039d050e4512691395b4587b06adfc47fb1f21eddf0ca16527",
		"36eac0aa56342867765a291153a9ffa99baabcbfc1c1a738335a97684e0d58c8",
	}
	for i, name := range files {
		f, err := digest(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if f.SHA256 != expected[i] {
			t.Fatalf("%s differs from original baseline: %s", name, f.SHA256)
		}
	}
	abs, names, err := benchfixture.Existing(dir, strings.Join(files, ","))
	if err != nil || abs != dir || !slices.Equal(names, files) {
		t.Fatalf("in-place fixture selection failed: %s %v %v", abs, names, err)
	}
}
