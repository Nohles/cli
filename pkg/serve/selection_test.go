package serve

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type registeredDirectorySelection struct {
	ID      string `json:"id"`
	Digest  string `json:"digest"`
	Version int    `json:"version"`
}

func loopbackRequest(method, target string, body []byte) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:43120"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func directorySelectionRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "Series")
	require.NoError(t, os.Mkdir(directory, 0o755))
	for _, name := range []string{"A Chapter 1.cbz", "A Chapter 2.cbz", "B Chapter 1.cbz", "B Chapter 2.cbz"} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), nil, 0o644))
	}
	server := NewServer(ServerConfig{}, Remote{LocalDirectory: root})
	return server.Routes(), base64.RawURLEncoding.EncodeToString([]byte("Series"))
}

func registerSelection(t *testing.T, router http.Handler, hrefs ...string) registeredDirectorySelection {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"publication": map[string]string{"path": "Series"},
		"selection":   map[string]any{"version": 1, "readingOrder": hrefs},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, loopbackRequest(http.MethodPost, "/_readium/directory-selections", body))
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var registration registeredDirectorySelection
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &registration))
	require.NotEmpty(t, registration.ID)
	require.NotEmpty(t, registration.Digest)
	return registration
}

func selectedReadingOrder(t *testing.T, router http.Handler, token string, registration registeredDirectorySelection) []string {
	t.Helper()
	recorder := httptest.NewRecorder()
	target := "/webpub/" + token + "/manifest.json?readiumSelection=" + registration.ID
	router.ServeHTTP(recorder, loopbackRequest(http.MethodGet, target, nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "v1; digest="+registration.Digest, recorder.Header().Get("Readium-Directory-Selection"))
	var publication struct {
		ReadingOrder []struct {
			Href string `json:"href"`
		} `json:"readingOrder"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &publication))
	hrefs := make([]string, len(publication.ReadingOrder))
	for index, link := range publication.ReadingOrder {
		hrefs[index] = link.Href
	}
	return hrefs
}

func TestDirectorySelectionCapabilitiesRequireLoopback(t *testing.T) {
	router, _ := directorySelectionRouter(t)
	remote := httptest.NewRecorder()
	router.ServeHTTP(remote, httptest.NewRequest(http.MethodGet, "/_readium/capabilities", nil))
	assert.Equal(t, http.StatusForbidden, remote.Code)

	local := httptest.NewRecorder()
	router.ServeHTTP(local, loopbackRequest(http.MethodGet, "/_readium/capabilities", nil))
	require.Equal(t, http.StatusOK, local.Code)
	assert.Contains(t, local.Body.String(), "directoryPublicationSelection")
}

func TestDirectorySelectionAcceptsLibraryRootPublicationPath(t *testing.T) {
	path, err := normalizeDirectoryPublicationPath(".")
	require.NoError(t, err)
	assert.Equal(t, ".", path)
}

func TestDirectorySelectionsIsolatePublicationsSharingOneDirectory(t *testing.T) {
	router, token := directorySelectionRouter(t)
	a := registerSelection(t, router, "A%20Chapter%201.cbz", "A%20Chapter%202.cbz")
	b := registerSelection(t, router, "B%20Chapter%201.cbz", "B%20Chapter%202.cbz")

	assert.NotEqual(t, a.Digest, b.Digest)
	assert.Equal(t, []string{"A%20Chapter%201.cbz", "A%20Chapter%202.cbz"}, selectedReadingOrder(t, router, token, a))
	assert.Equal(t, []string{"B%20Chapter%201.cbz", "B%20Chapter%202.cbz"}, selectedReadingOrder(t, router, token, b))
}

func TestDirectorySelectionRegistrationValidatesAndDeletesHandles(t *testing.T) {
	router, token := directorySelectionRouter(t)
	invalidBody := []byte(`{"publication":{"path":"Series"},"selection":{"version":1,"readingOrder":["Missing.cbz"]}}`)
	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, loopbackRequest(http.MethodPost, "/_readium/directory-selections", invalidBody))
	assert.Equal(t, http.StatusUnprocessableEntity, invalid.Code)

	registration := registerSelection(t, router, "A%20Chapter%201.cbz")
	deleted := httptest.NewRecorder()
	router.ServeHTTP(deleted, loopbackRequest(http.MethodDelete, "/_readium/directory-selections/"+registration.ID, nil))
	assert.Equal(t, http.StatusNoContent, deleted.Code)

	expired := httptest.NewRecorder()
	target := "/webpub/" + token + "/manifest.json?readiumSelection=" + registration.ID
	router.ServeHTTP(expired, loopbackRequest(http.MethodGet, target, nil))
	assert.Equal(t, http.StatusGone, expired.Code)
}

func TestDirectorySelectionCannotBeUsedForAnotherPublication(t *testing.T) {
	router, _ := directorySelectionRouter(t)
	registration := registerSelection(t, router, "A%20Chapter%201.cbz")
	otherToken := base64.RawURLEncoding.EncodeToString([]byte("Other"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, loopbackRequest(
		http.MethodGet,
		"/webpub/"+otherToken+"/manifest.json?readiumSelection="+registration.ID,
		nil,
	))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
