package serve

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	pathpkg "path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/nohles/go-toolkit/pkg/util/url"
	"github.com/readium/cli/pkg/serve/auth"
)

const (
	directorySelectionVersion      = 1
	directorySelectionQuery        = "readiumSelection"
	directorySelectionTTL          = 24 * time.Hour
	maxDirectorySelectionBodyBytes = 8 << 20
	maxDirectorySelectionItems     = 50_000
	maxDirectorySelections         = 1_024
)

type directorySelectionContextKey struct{}

type directorySelection struct {
	ID              string
	PublicationPath string
	ReadingOrder    []manifest.HREF
	Digest          string
	ExpiresAt       time.Time
}

type directorySelectionRegistry struct {
	mu      sync.Mutex
	entries map[string]*directorySelection
}

func newDirectorySelectionRegistry() *directorySelectionRegistry {
	return &directorySelectionRegistry{entries: make(map[string]*directorySelection)}
}

func (registry *directorySelectionRegistry) cleanupLocked(now time.Time) {
	for id, selection := range registry.entries {
		if !selection.ExpiresAt.After(now) {
			delete(registry.entries, id)
		}
	}
}

func (registry *directorySelectionRegistry) add(selection *directorySelection) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.cleanupLocked(time.Now())
	if len(registry.entries) >= maxDirectorySelections {
		return errors.New("directory selection registry is full")
	}
	registry.entries[selection.ID] = selection
	return nil
}

func (registry *directorySelectionRegistry) get(id string) (*directorySelection, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.cleanupLocked(time.Now())
	selection, ok := registry.entries[id]
	return selection, ok
}

func (registry *directorySelectionRegistry) delete(id string) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_, existed := registry.entries[id]
	delete(registry.entries, id)
	return existed
}

func normalizeDirectoryPublicationPath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.Contains(value, "//") {
		return "", errors.New("publication path must be a normalized relative path")
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", errors.New("publication path contains invalid traversal")
	}
	return cleaned, nil
}

func normalizeDirectorySelectionHREF(value string) (manifest.HREF, error) {
	href, err := manifest.NewHREFFromString(value, false)
	if err != nil {
		return manifest.HREF{}, fmt.Errorf("invalid selection HREF %q: %w", value, err)
	}
	u := href.Resolve(nil, nil)
	raw := u.Raw()
	decodedPath := u.Path()
	if raw.IsAbs() || raw.Host != "" || raw.RawQuery != "" || raw.ForceQuery || raw.Fragment != "" {
		return manifest.HREF{}, fmt.Errorf("selection HREF %q must be a relative path without query or fragment", value)
	}
	if decodedPath == "" || strings.HasPrefix(decodedPath, "/") || strings.Contains(decodedPath, "\\") || strings.Contains(decodedPath, "//") {
		return manifest.HREF{}, fmt.Errorf("selection HREF %q is not a normalized relative path", value)
	}
	cleaned := pathpkg.Clean(decodedPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != decodedPath {
		return manifest.HREF{}, fmt.Errorf("selection HREF %q contains invalid traversal", value)
	}
	canonical, err := url.URLFromDecodedPath(decodedPath)
	if err != nil {
		return manifest.HREF{}, fmt.Errorf("failed normalizing selection HREF %q: %w", value, err)
	}
	return manifest.NewHREF(canonical), nil
}

func directorySelectionDigest(readingOrder []manifest.HREF) string {
	hash := sha256.New()
	writePart := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		hash.Write(size[:])
		hash.Write([]byte(value))
	}
	writePart("directory-publication-selection-v1")
	for _, href := range readingOrder {
		writePart(href.String())
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func newDirectorySelectionID() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func isLoopbackRequest(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func writeDirectorySelectionJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) getDirectorySelectionCapabilities(w http.ResponseWriter, request *http.Request) {
	if !isLoopbackRequest(request) {
		writeDirectorySelectionJSON(w, http.StatusForbidden, map[string]string{"error": "localhost access required"})
		return
	}
	writeDirectorySelectionJSON(w, http.StatusOK, map[string]any{
		"directoryPublicationSelection": map[string]any{"versions": []int{directorySelectionVersion}},
	})
}

func (s *Server) registerDirectorySelection(w http.ResponseWriter, request *http.Request) {
	if !isLoopbackRequest(request) {
		writeDirectorySelectionJSON(w, http.StatusForbidden, map[string]string{"error": "localhost access required"})
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, maxDirectorySelectionBodyBytes)
	var body struct {
		Publication struct {
			Path string `json:"path"`
		} `json:"publication"`
		Selection struct {
			Version      int      `json:"version"`
			ReadingOrder []string `json:"readingOrder"`
		} `json:"selection"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid registration document: " + err.Error()})
		return
	}
	if body.Selection.Version != directorySelectionVersion {
		writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported directory selection version"})
		return
	}
	if len(body.Selection.ReadingOrder) == 0 || len(body.Selection.ReadingOrder) > maxDirectorySelectionItems {
		writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": "readingOrder must contain between 1 and 50000 entries"})
		return
	}
	publicationPath, err := normalizeDirectoryPublicationPath(body.Publication.Path)
	if err != nil {
		writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	readingOrder := make([]manifest.HREF, 0, len(body.Selection.ReadingOrder))
	seen := make(map[string]struct{}, len(body.Selection.ReadingOrder))
	for _, value := range body.Selection.ReadingOrder {
		href, err := normalizeDirectorySelectionHREF(value)
		if err != nil {
			writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if _, duplicate := seen[href.String()]; duplicate {
			writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("duplicate selection HREF %q", href.String())})
			return
		}
		seen[href.String()] = struct{}{}
		readingOrder = append(readingOrder, href)
	}
	id, err := newDirectorySelectionID()
	if err != nil {
		writeDirectorySelectionJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed creating registration id"})
		return
	}
	selection := &directorySelection{
		ID:              id,
		PublicationPath: publicationPath,
		ReadingOrder:    readingOrder,
		Digest:          directorySelectionDigest(readingOrder),
		ExpiresAt:       time.Now().Add(directorySelectionTTL),
	}
	ctx := context.WithValue(request.Context(), auth.ContextPathKey, publicationPath)
	ctx = context.WithValue(ctx, directorySelectionContextKey{}, selection)
	publication, err := s.getPublication(ctx)
	if err != nil {
		writeDirectorySelectionJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := validateSelectedDirectoryPublication(publication.Publication, readingOrder); err != nil {
		publication.Release()
		writeDirectorySelectionJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	publication.Release()
	if err := s.directorySelections.add(selection); err != nil {
		writeDirectorySelectionJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeDirectorySelectionJSON(w, http.StatusCreated, map[string]any{
		"id":        selection.ID,
		"version":   directorySelectionVersion,
		"digest":    selection.Digest,
		"expiresAt": selection.ExpiresAt.UnixMilli(),
	})
}

func validateSelectedDirectoryPublication(publication *pub.Publication, readingOrder []manifest.HREF) error {
	if publication == nil || len(publication.Manifest.ReadingOrder) != len(readingOrder) {
		return errors.New("selection is not a directory comic-archive publication")
	}
	for index, link := range publication.Manifest.ReadingOrder {
		if link.MediaType == nil || !link.MediaType.IsComicArchive() || link.Href.String() != readingOrder[index].String() {
			return errors.New("selection is not a directory comic-archive publication")
		}
	}
	return nil
}

func (s *Server) deleteDirectorySelection(w http.ResponseWriter, request *http.Request) {
	if !isLoopbackRequest(request) {
		writeDirectorySelectionJSON(w, http.StatusForbidden, map[string]string{"error": "localhost access required"})
		return
	}
	if !s.directorySelections.delete(mux.Vars(request)["id"]) {
		writeDirectorySelectionJSON(w, http.StatusNotFound, map[string]string{"error": "directory selection not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) withDirectorySelection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		values := request.URL.Query()
		ids := values[directorySelectionQuery]
		if len(ids) == 0 {
			next.ServeHTTP(w, request)
			return
		}
		if len(ids) != 1 || ids[0] == "" {
			writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": "exactly one directory selection id is required"})
			return
		}
		selection, ok := s.directorySelections.get(ids[0])
		if !ok {
			writeDirectorySelectionJSON(w, http.StatusGone, map[string]string{"error": "directory selection is missing or expired"})
			return
		}
		publicationPath, ok := request.Context().Value(auth.ContextPathKey).(string)
		if !ok || publicationPath != selection.PublicationPath {
			writeDirectorySelectionJSON(w, http.StatusBadRequest, map[string]string{"error": "directory selection does not belong to this publication"})
			return
		}
		values.Del(directorySelectionQuery)
		requestURL := *request.URL
		requestURL.RawQuery = values.Encode()
		request = request.Clone(context.WithValue(request.Context(), directorySelectionContextKey{}, selection))
		request.URL = &requestURL
		w.Header().Set("Readium-Directory-Selection", fmt.Sprintf("v1; digest=%s", selection.Digest))
		next.ServeHTTP(w, request)
	})
}

func directorySelectionFromContext(ctx context.Context) *directorySelection {
	selection, _ := ctx.Value(directorySelectionContextKey{}).(*directorySelection)
	return selection
}
