package cache

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/readium/cli/pkg/serve/session"
	"github.com/stretchr/testify/require"
)

func testPublication(t *testing.T) *CachedPublication {
	t.Helper()
	p := &pub.Publication{}
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"title":"Original"},"readingOrder":[]}`), &p.Manifest))
	return EncapsulatePublication(p, nil, false)
}
func manifestURL(t *testing.T, s string) url.AbsoluteURL {
	t.Helper()
	u, err := url.AbsoluteURLFromString(s)
	require.NoError(t, err)
	return u
}

func TestManifestCacheVariantsAndSessionRefresh(t *testing.T) {
	cp := testPublication(t)
	firstURL := manifestURL(t, "http://one.test/webpub/token/manifest.json")
	first, err := cp.MarshalManifest(firstURL, "")
	require.NoError(t, err)
	hit, err := cp.MarshalManifest(firstURL, "")
	require.NoError(t, err)
	require.Same(t, first, hit)
	other, err := cp.MarshalManifest(manifestURL(t, "https://two.test/webpub/other-token/manifest.json"), "  ")
	require.NoError(t, err)
	require.Contains(t, string(other.Data), "https://two.test/webpub/other-token/manifest.json")
	require.NotContains(t, string(other.Data), "one.test")
	require.Contains(t, string(other.Data), "\n  ")
	var doc session.ReadingSessionDocument
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"title":"Updated","conformsTo":["https://readium.org/webpub-manifest/profiles/audiobook"]}}`), &doc))
	cp.RefreshSession(&doc)
	updated, err := cp.MarshalManifest(firstURL, "")
	require.NoError(t, err)
	require.Contains(t, string(updated.Data), "Updated")
	require.Contains(t, updated.ContentType, "audiobook")
	require.NotEqual(t, first.ETag, updated.ETag)
	require.Contains(t, string(first.Data), "Original", "in-flight readers must keep their original immutable response")
}

func TestManifestCacheBoundedWithLargeDataAndManyHosts(t *testing.T) {
	cp := testPublication(t)
	for i := range 100 {
		_, err := cp.MarshalManifest(manifestURL(t, "http://host"+strings.Repeat("x", i)+".test/manifest.json"), "")
		require.NoError(t, err)
		require.LessOrEqual(t, len(cp.manifestResponse.key)+len(cp.manifestResponse.response.Data), maxManifestCacheBytes)
	}
	huge := testPublication(t)
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"title":"`+strings.Repeat("x", maxManifestCacheBytes)+`"},"readingOrder":[]}`), &huge.Publication.Manifest))
	response, err := huge.MarshalManifest(manifestURL(t, "http://test/manifest.json"), "")
	require.NoError(t, err)
	require.Greater(t, len(response.Data), maxManifestCacheBytes)
	require.Nil(t, huge.manifestResponse, "oversized responses must not be retained")
}

func TestManifestCacheConcurrentRefresh(t *testing.T) {
	cp := testPublication(t)
	u := manifestURL(t, "http://test/manifest.json")
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			cp.RefreshSession(&session.ReadingSessionDocument{})
		}
	})
	for range 8 {
		wg.Go(func() {
			for range 100 {
				response, err := cp.MarshalManifest(u, "")
				if err != nil {
					t.Error(err)
					return
				}
				if !json.Valid(response.Data) {
					t.Error("invalid cached JSON")
				}
			}
		})
	}
	wg.Wait()
}
