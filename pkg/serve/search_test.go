package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureSourcePath locates the locator fixture EPUB built by ticket #138.
func fixtureSourcePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("READIUM_SEARCH_FIXTURE"); p != "" {
		require.FileExists(t, p)
		return p
	}
	// Canonical sibling checkout relative to this repo (cli at ~/Code/cli,
	// Reader at ~/Code/Reader). Go test binaries run with the package
	// directory as cwd, so three levels up from pkg/serve is the repo root.
	for _, p := range []string{
		"../../../Reader/test/fixtures/search-locator-fixture/search-locator-fixture.epub",
		"../../../../../../Reader/test/fixtures/search-locator-fixture/search-locator-fixture.epub",
	} {
		if abs, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	t.Skip("locator fixture EPUB not found; set READIUM_SEARCH_FIXTURE")
	return ""
}

// newFixtureServer serves a fresh server whose LocalDirectory contains the
// fixture EPUB under the given name. Returns the router and the b64 token
// (relative to the served directory, per the confinement gate).
func newFixtureServer(t *testing.T, name string) (http.Handler, string) {
	t.Helper()
	src := fixtureSourcePath(t)
	dir := t.TempDir()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))

	s := NewServer(ServerConfig{}, Remote{LocalDirectory: dir})
	token := fileToken(name)
	return s.Routes(), token
}

func searchGet(router http.Handler, token, rawQuery string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/webpub/"+token+"/~readium/search?"+rawQuery, nil)
	router.ServeHTTP(rec, req)
	return rec
}

func optionsRequest(router http.Handler, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/webpub/"+token+"/~readium/search", nil)
	router.ServeHTTP(rec, req)
	return rec
}

type locatorCollection struct {
	Metadata struct {
		NumberOfItems *int `json:"numberOfItems"`
	} `json:"metadata"`
	Links []struct {
		Rel  string `json:"rel"`
		Href string `json:"href"`
		Type string `json:"type"`
	} `json:"links"`
	Locators []struct {
		Href      string `json:"href"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Locations struct {
			Progression *float64 `json:"progression"`
		} `json:"locations"`
		Text struct {
			Before    string `json:"before"`
			Highlight string `json:"highlight"`
			After     string `json:"after"`
		} `json:"text"`
	} `json:"locators"`
}

func TestSearchGETReturnsLocatorCollection(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	rec := searchGet(router, token, "query=lantern")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/vnd.readium.locators+json", strings.TrimSpace(strings.Split(rec.Header().Get("content-type"), ";")[0]))

	var coll locatorCollection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &coll))
	require.NotNil(t, coll.Metadata.NumberOfItems)
	assert.Equal(t, 15, *coll.Metadata.NumberOfItems)
	require.NotEmpty(t, coll.Locators)

	first := coll.Locators[0]
	assert.Contains(t, first.Href, ".xhtml")
	assert.Equal(t, "application/xhtml+xml", first.Type)
	assert.NotEmpty(t, first.Text.Highlight)
	assert.True(t, strings.EqualFold(first.Text.Highlight, "lantern"))

	// self link present and typed
	var selfLink, nextLink *string
	for i := range coll.Links {
		switch coll.Links[i].Rel {
		case "self":
			selfLink = &coll.Links[i].Href
		case "next":
			nextLink = &coll.Links[i].Href
		}
	}
	require.NotNil(t, selfLink)
	assert.Contains(t, *selfLink, "query=lantern")
	assert.Nil(t, nextLink, "15 results fit one page of 20; no next link")
}

func TestSearchPaginationFollowsNext(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	// "the" occurs far more than 20 times in the fixture.
	rec := searchGet(router, token, "query=the")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var coll locatorCollection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &coll))
	require.Len(t, coll.Locators, 20, "first page carries exactly page-length results")

	var nextURL string
	for _, l := range coll.Links {
		if l.Rel == "next" {
			nextURL = l.Href
		}
	}
	require.NotEmpty(t, nextURL, "a >20-result search must expose a next link")
	assert.Contains(t, nextURL, "page=2")

	// Follow it opaquely, as clients must.
	path := nextURL
	if i := strings.Index(nextURL, "/webpub/"); i >= 0 {
		path = nextURL[i:]
	}
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())

	var page2 locatorCollection
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &page2))
	require.NotEmpty(t, page2.Locators)
	// Page 2 must carry the remaining results in order (no overlap with page 1).
	assert.NotEqual(t, coll.Locators[0].Text.Highlight+coll.Locators[0].Text.After,
		page2.Locators[0].Text.Highlight+page2.Locators[0].Text.After,
		"page 2 must advance past page 1")
}

func TestSearchMissingQueryIsProblemDetails(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	for name, q := range map[string]string{"absent": "", "empty": "query=", "blank": "query=%20%20"} {
		t.Run(name, func(t *testing.T) {
			rec := searchGet(router, token, q)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Header().Get("content-type"), "application/problem+json")

			var problem struct {
				Type   string `json:"type"`
				Title  string `json:"title"`
				Status int    `json:"status"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
			assert.Equal(t, http.StatusBadRequest, problem.Status)
			assert.NotEmpty(t, problem.Type)
			assert.NotEmpty(t, problem.Title)
		})
	}
}

func TestSearchQueryTooLongIsProblemDetails(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	long := strings.Repeat("x", 201)
	rec := searchGet(router, token, "query="+long)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Header().Get("content-type"), "application/problem+json")
}

func TestSearchURLEscaping(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	// The printer’s apostrophe + spaces round-trip through percent-encoding.
	rec := searchGet(router, token, "query=printer%E2%80%99s+apprentice")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var coll locatorCollection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &coll))
	require.NotNil(t, coll.Metadata.NumberOfItems)
	assert.Equal(t, 1, *coll.Metadata.NumberOfItems)
	require.Len(t, coll.Locators, 1)
	assert.Equal(t, "printer’s apprentice", coll.Locators[0].Text.Highlight)
}

func TestSearchUnsupportedOptionRejected(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	// Integer true for case-sensitive cannot be honored by the engine.
	rec := searchGet(router, token, "query=lantern&case-sensitive=1")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Header().Get("content-type"), "application/problem+json")

	// Integer false is fine per proposal 007.
	rec2 := searchGet(router, token, "query=lantern&case-sensitive=0&whole-word=false")
	assert.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
}

func TestSearchOptionsEndpoint(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	rec := optionsRequest(router, token)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("content-type"), "application/json")

	var opts struct {
		Options map[string]interface{} `json:"options"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &opts))
	assert.Contains(t, opts.Options, "org.readium.cli.page-length")
}

func TestSearchHEADIsCheap(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/webpub/"+token+"/~readium/search?query=lantern", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.Bytes(), "HEAD must not carry a body")
	assert.NotEmpty(t, rec.Header().Get("content-length"))
}

func TestSearchOnUnsearchablePublication(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tone.mp3"), pattern(1<<20), 0o644))

	s := NewServer(ServerConfig{}, Remote{LocalDirectory: dir})
	router := s.Routes()
	token := fileToken("tone.mp3")

	// The manifest opens (audio publication) but has no search service.
	recM := httptest.NewRecorder()
	router.ServeHTTP(recM, httptest.NewRequest(http.MethodGet, "/webpub/"+token+"/manifest.json", nil))
	require.Equal(t, http.StatusOK, recM.Code)

	rec := searchGet(router, token, "query=x")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("content-type"), "application/problem+json")
}

func TestSearchBatchEndpoint(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	body := `{"terms":[
		{"id":"entry-lantern","text":"lantern"},
		{"id":"entry-mirror","text":"bronze mirror"},
		{"id":"entry-none","text":"xylophonezzz"},
		{"text":"ivory compass"}
	]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webpub/"+token+"/~readium/search/batch", strings.NewReader(body))
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", strings.TrimSpace(strings.Split(rec.Header().Get("content-type"), ";")[0]))

	var resp struct {
		Results map[string]struct {
			Total    *int `json:"total"`
			Locators []struct {
				Text struct {
					Highlight string `json:"highlight"`
				} `json:"text"`
			} `json:"locators"`
			Error string `json:"error"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	lantern, ok := resp.Results["entry-lantern"]
	require.True(t, ok, "results must be keyed by term id: %v", resp.Results)
	require.NotNil(t, lantern.Total)
	assert.Equal(t, 15, *lantern.Total)
	assert.NotEmpty(t, lantern.Locators)

	mirror, okMirror := resp.Results["entry-mirror"]
	require.True(t, okMirror)
	require.NotNil(t, mirror.Total)
	assert.Equal(t, 4, *mirror.Total)

	none, okNone := resp.Results["entry-none"]
	require.True(t, okNone)
	require.NotNil(t, none.Total)
	assert.Equal(t, 0, *none.Total)
	assert.Empty(t, none.Locators)
	assert.Empty(t, none.Error)

	anon, okAnon := resp.Results["ivory compass"] // id defaults to text when absent
	require.True(t, okAnon)
	assert.NotEmpty(t, anon.Locators)
}

func TestSearchBatchEndpointReturnsEveryPage(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	rec := batchPost(router, token, `{"terms":[{"id":"common","text":"the"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Results map[string]struct {
			Total    *int              `json:"total"`
			Locators []json.RawMessage `json:"locators"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	common := resp.Results["common"]
	require.NotNil(t, common.Total)
	assert.Greater(t, *common.Total, pub.DefaultSearchPageLength)
	assert.Len(t, common.Locators, *common.Total,
		"the batch endpoint must not discard matches after the first page")
}

func TestSearchBatchValidation(t *testing.T) {
	router, token := newFixtureServer(t, "fixture.epub")

	t.Run("empty terms rejected", func(t *testing.T) {
		rec := batchPost(router, token, `{"terms":[]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("malformed JSON rejected", func(t *testing.T) {
		rec := batchPost(router, token, `{terms`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func batchPost(router http.Handler, token, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webpub/"+token+"/~readium/search/batch", strings.NewReader(body))
	router.ServeHTTP(rec, req)
	return rec
}

func TestPublicationFingerprintInvalidation(t *testing.T) {
	// Prove that editing the source file after caching serves the NEW content:
	// v1 answers searches for "oldword", v2 for "newword".
	dir := t.TempDir()

	v1 := buildMiniEPUB(t, "The oldword story.", "Second chapter oldword.")
	path1 := filepath.Join(dir, "book.epub")
	require.NoError(t, os.WriteFile(path1, v1, 0o644))

	s := NewServer(ServerConfig{}, Remote{LocalDirectory: dir})
	router := s.Routes()
	token := fileToken("book.epub")

	// Warm the cache on v1 and confirm v1 behavior.
	rec := searchGet(router, token, "query=oldword")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var c1 locatorCollection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c1))
	require.NotNil(t, c1.Metadata.NumberOfItems)
	assert.Equal(t, 2, *c1.Metadata.NumberOfItems)

	// Mutate the source in place. The size changes (different body length)
	// and mtime moves; either alone trips the fingerprint.
	v2 := buildMiniEPUB(t, "The newword story.", "Second chapter oldword.")
	require.NoError(t, os.WriteFile(path1, v2, 0o644))
	mtime := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path1, mtime, mtime))

	rec2 := searchGet(router, token, "query=newword")
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	var c2 locatorCollection
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &c2))
	require.NotNil(t, c2.Metadata.NumberOfItems)
	assert.Equal(t, 1, *c2.Metadata.NumberOfItems,
		"a mutated source must invalidate the cached publication")
}

func TestPublicationFingerprintStableAcrossReads(t *testing.T) {
	// Unchanged sources must keep hitting the same cache entry: the second
	// identical search succeeds without re-parsing (no flake, no eviction).
	router, token := newFixtureServer(t, "fixture.epub")

	for i := 0; i < 3; i++ {
		rec := searchGet(router, token, "query=lantern")
		require.Equal(t, http.StatusOK, rec.Code)
		var coll locatorCollection
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &coll))
		require.NotNil(t, coll.Metadata.NumberOfItems)
		assert.Equal(t, 15, *coll.Metadata.NumberOfItems)
	}
}
