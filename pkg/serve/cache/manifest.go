package cache

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/zeebo/xxh3"
)

// Only one self-URL/indent variant is retained per publication. The combined
// key+body cap bounds this cache to about 1.25 MiB across ten publications, and
// prevents arbitrary Host/token variants or very large manifests growing RAM.
const maxManifestCacheBytes = 128 << 10

// ManifestResponse is immutable after creation; callers must not mutate Data.
// A response remains valid for in-flight readers after a session refresh.
type ManifestResponse struct {
	Data        []byte
	ETag        string
	ContentType string
	CachedAt    time.Time
}
type manifestResponseCache struct {
	key      string
	response *ManifestResponse
}

func (cp *CachedPublication) MarshalManifest(selfURL url.AbsoluteURL, indent string) (*ManifestResponse, error) {
	key := selfURL.String() + "\x00" + indent
	cp.Mu.RLock()
	if cached := cp.manifestResponse; cached != nil && cached.key == key {
		response := cached.response
		cp.Mu.RUnlock()
		return response, nil
	}
	cp.Mu.RUnlock()
	cp.Mu.Lock()
	defer cp.Mu.Unlock()
	if cached := cp.manifestResponse; cached != nil && cached.key == key {
		return cached.response, nil
	}
	mime := mediatype.ReadiumWebpubManifest
	for _, profile := range cp.Publication.Manifest.Metadata.ConformsTo {
		if profile == manifest.ProfileDivina {
			mime = mediatype.ReadiumDivinaManifest
			break
		}
		if profile == manifest.ProfileAudiobook {
			mime = mediatype.ReadiumAudiobookManifest
			break
		}
	}
	self := &manifest.Link{Rels: manifest.Strings{"self"}, MediaType: &mime, Href: manifest.NewHREF(selfURL)}
	var data []byte
	var err error
	if indent == "" {
		data, err = json.Marshal(cp.Publication.Manifest.ToMap(self))
	} else {
		data, err = json.MarshalIndent(cp.Publication.Manifest.ToMap(self), "", indent)
	}
	if err != nil {
		return nil, err
	}
	response := &ManifestResponse{Data: data, ETag: `"` + strconv.FormatUint(xxh3.Hash(data), 36) + `"`, ContentType: mime.String() + "; charset=utf-8", CachedAt: cp.CachedAt}
	if len(key)+len(data) <= maxManifestCacheBytes {
		cp.manifestResponse = &manifestResponseCache{key: key, response: response}
	}
	return response, nil
}
