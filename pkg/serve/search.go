package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/airmrcr/go-problem"
	"github.com/gorilla/mux"
	"github.com/nohles/go-toolkit/pkg/manifest"
	"github.com/nohles/go-toolkit/pkg/mediatype"
	"github.com/nohles/go-toolkit/pkg/pub"
	"github.com/readium/cli/pkg/serve/problems"
)

// searchTimeout bounds every search operation, matching the approved
// standing decision. It covers corpus extraction on first use as well as
// matching.
const searchTimeout = 5 * time.Second

// maxBatchTerms bounds the internal batch endpoint. Dictionary lookups send
// one term per entry title/alias; this cap keeps a single request bounded.
const maxBatchTerms = 64

// maxBatchBodyBytes bounds the batch request body size.
const maxBatchBodyBytes = 1 << 20

// searchPublication serves the publication's Readium search service:
//
//   - OPTIONS returns the supported search options as JSON,
//   - GET ?query=… executes a search and returns the first requested page
//     as a LocatorCollection (application/vnd.readium.locators+json),
//   - HEAD mirrors GET's headers without the body.
//
// Invalid queries and options are rejected with RFC 9457 Problem Details
// (400). Failures surface as errors — never as an empty success masquerading
// as one.
func (s *Server) searchPublication(w http.ResponseWriter, req *http.Request) {
	cp, err := s.getPublication(req.Context())
	if err != nil {
		problems.Write(err, w, req)
		return
	}
	defer cp.Release()

	cp.Mu.RLock()
	searchSvc, searchable := cp.Publication.SearchService()
	cp.Mu.RUnlock()
	if !searchable {
		problems.Write(problems.NotFound.Build().
			Detail("this publication does not provide a search service").Problem(), w, req)
		return
	}

	switch req.Method {
	case http.MethodOptions:
		s.handleSearchOptions(w, req, searchSvc)
	case http.MethodGet, http.MethodHead:
		s.handleSearchQuery(w, req, searchSvc)
	default:
		w.Header().Set("allow", "GET, HEAD, OPTIONS")
		problems.Write(problems.MethodNotAllowed.Build().
			Detail("only GET, HEAD, and OPTIONS are supported on this resource").Problem(), w, req)
	}
}

// handleSearchOptions implements the proposal 007 OPTIONS response: the
// supported search options as a JSON object. The corpus engine exposes no
// toggleable matchers; the page length is advertised under a reverse-DNS
// extension key as sanctioned by the proposal.
func (s *Server) handleSearchOptions(w http.ResponseWriter, req *http.Request, svc pub.SearchService) {
	opts := svc.Options()
	body, err := json.Marshal(map[string]interface{}{
		"options": map[string]interface{}{
			"org.readium.cli.page-length": opts.PageLength,
		},
	})
	if err != nil {
		problems.Write(problems.Internal("failed marshalling search options", err), w, req)
		return
	}
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("content-length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// handleSearchQuery executes a GET/HEAD search: validates query and options,
// seeks to the requested page, and renders a LocatorCollection with self/next
// links.
func (s *Server) handleSearchQuery(w http.ResponseWriter, req *http.Request, svc pub.SearchService) {
	params := req.URL.Query()

	query := params.Get("query")
	if strings.TrimSpace(query) == "" {
		problems.Write(problems.SearchInvalidQuery.Build().
			Detail("the query parameter is required and must not be empty").Problem(), w, req)
		return
	}
	if verr := validateSearchOptions(params); verr != nil {
		problems.Write(verr, w, req)
		return
	}
	page := 1
	if raw := params.Get("page"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 1 {
			problems.Write(problems.SearchInvalidQuery.Build().
				Detail("the page parameter must be a positive integer").Problem(), w, req)
			return
		}
		page = n
	}

	ctx, cancel := context.WithTimeout(req.Context(), searchTimeout)
	defer cancel()

	it, serr := svc.Search(ctx, query)
	if serr != nil {
		problems.Write(searchErrorToProblem(serr), w, req)
		return
	}
	defer it.Close()

	var (
		total    int
		totalOK  bool
		collPage pub.LocatorCollectionPage
	)
	// The total is available before iteration begins.
	total, totalOK = it.Total()
	for n := 1; ; n++ {
		p, ierr := it.Next(ctx)
		if ierr != nil {
			if errors.Is(ierr, pub.ErrIteratorDone) {
				break
			}
			problems.Write(searchErrorToProblem(ierr), w, req)
			return
		}
		if n == page {
			collPage = p
			break
		}
	}
	if collPage.Number == 0 {
		// The requested page lies past the end of the result set; render an
		// empty page for that number rather than erroring.
		collPage = pub.LocatorCollectionPage{Number: page}
	}

	body, err := marshalLocatorCollection(req, searchRoutePath(s.router, req), query, page, collPage, total, totalOK)
	if err != nil {
		problems.Write(problems.Internal("failed marshalling search results", err), w, req)
		return
	}

	w.Header().Set("content-type", mediatype.ReadiumLocatorsJSON.String()+"; charset=utf-8")
	w.Header().Set("cache-control", "private, max-age=300")
	w.Header().Set("content-length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// validateSearchOptions rejects option values the engine cannot honor.
// Boolean options whose false reading matches the engine's fixed behavior
// (case-insensitive, diacritic-insensitive, substring) accept their integer
// representations per proposal 007; requesting the opposite behavior is an
// invalid-option 400 rather than silently wrong results. Unsupported
// matcher families (regex, language) are likewise rejected. Reverse-DNS
// extension parameters are ignored.
func validateSearchOptions(params url.Values) error {
	for _, name := range []string{"case-sensitive", "diacritic-sensitive", "whole-word", "exact"} {
		values, ok := params[name]
		if !ok || len(values) == 0 {
			continue
		}
		switch strings.ToLower(values[0]) {
		case "", "0", "false", "f", "no":
			// Matches engine behavior.
		default:
			return problems.SearchUnsupportedOption.Build().
				Detailf("the %s option is not supported by this search service", name).Problem()
		}
	}
	if _, ok := params["regex"]; ok {
		return problems.SearchUnsupportedOption.Build().
			Detail("regular-expression queries are not supported").Problem()
	}
	if _, ok := params["language"]; ok {
		return problems.SearchUnsupportedOption.Build().
			Detail("language-sensitive matching is not supported").Problem()
	}
	return nil
}

// searchErrorToProblem maps search failures onto Problem Details types.
func searchErrorToProblem(err error) *problem.Problem {
	switch {
	case errors.Is(err, pub.ErrEmptyQuery):
		return problems.SearchInvalidQuery.Build().
			Detail("the query parameter is required and must not be empty").Problem()
	case errors.Is(err, pub.ErrQueryTooLong):
		return problems.SearchInvalidQuery.Build().
			Detailf("the query exceeds the maximum of %d characters", pub.DefaultMaxQueryRunes).Problem()
	case errors.Is(err, context.DeadlineExceeded):
		return problems.ServiceUnavailable.Build().
			Wrap(err).Detail("the search did not complete in time").Problem()
	default:
		return problems.Internal("", err)
	}
}

// collectionLink is one entry of a LocatorCollection's links array.
type collectionLink struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
	Type string `json:"type"`
}

// marshalLocatorCollection renders one page as a proposal 007
// LocatorCollection: optional numberOfItems metadata, self/next links
// preserving validated query parameters, and the page's locators.
func marshalLocatorCollection(req *http.Request, path, query string, page int, collPage pub.LocatorCollectionPage, total int, totalOK bool) ([]byte, error) {
	self := searchPageURL(req, path, query, page)
	links := []collectionLink{{
		Rel:  "self",
		Href: self,
		Type: mediatype.ReadiumLocatorsJSON.String(),
	}}
	if collPage.HasNext {
		links = append(links, collectionLink{
			Rel:  "next",
			Href: searchPageURL(req, path, query, page+1),
			Type: mediatype.ReadiumLocatorsJSON.String(),
		})
	}

	collection := struct {
		Metadata struct {
			NumberOfItems *int `json:"numberOfItems,omitempty"`
		} `json:"metadata"`
		Links    []collectionLink   `json:"links"`
		Locators []manifest.Locator `json:"locators"`
	}{Links: links}
	if totalOK {
		collection.Metadata.NumberOfItems = &total
	}
	collection.Locators = collPage.Locators
	if collection.Locators == nil {
		collection.Locators = []manifest.Locator{}
	}
	return json.Marshal(collection)
}

// searchPageURL builds an absolute self/next URL carrying the given page,
// preserving the validated query parameters.
func searchPageURL(req *http.Request, path, query string, page int) string {
	scheme := "http://"
	if req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https://"
	}
	q := url.Values{}
	q.Set("query", query)
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return scheme + req.Host + path + "?" + q.Encode()
}

// searchBasePath rebuilds the /webpub/{path}/~readium/search base of the
// current request. The token segment is base64url (no padding), so it needs
// no escaping.
func searchRoutePath(router *mux.Router, req *http.Request) string {
	token := mux.Vars(req)["path"]
	if token == "" {
		return ""
	}
	return "/webpub/" + token + "/~readium/search"
}

// ---------------------------------------------------------------------------
// Batch endpoint (internal; deliberately not advertised as rel=search)
// ---------------------------------------------------------------------------

type batchTerm struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type batchRequest struct {
	Terms []batchTerm `json:"terms"`
}

type batchTermResult struct {
	Total    *int               `json:"total,omitempty"`
	Locators []manifest.Locator `json:"locators,omitempty"`
	Error    string             `json:"error,omitempty"`
}

// batchSearch runs many terms (dictionary titles + aliases) against one
// publication in a single bounded request. All terms share the publication's
// warm search corpus, and every iterator page is returned up to the search
// service's per-query result cap. Per-term failures are reported in-band
// rather than failing the whole batch.
func (s *Server) batchSearch(w http.ResponseWriter, req *http.Request) {
	cp, err := s.getPublication(req.Context())
	if err != nil {
		problems.Write(err, w, req)
		return
	}
	defer cp.Release()

	cp.Mu.RLock()
	searchSvc, searchable := cp.Publication.SearchService()
	cp.Mu.RUnlock()
	if !searchable {
		problems.Write(problems.NotFound.Build().
			Detail("this publication does not provide a search service").Problem(), w, req)
		return
	}

	req.Body = http.MaxBytesReader(w, req.Body, maxBatchBodyBytes)
	var payload batchRequest
	if derr := json.NewDecoder(req.Body).Decode(&payload); derr != nil {
		problems.Write(problems.BadRequest.Build().Wrap(derr).
			Detail("failed parsing batch request body").Problem(), w, req)
		return
	}
	if len(payload.Terms) == 0 {
		problems.Write(problems.SearchInvalidQuery.Build().
			Detail("the terms array must contain at least one term").Problem(), w, req)
		return
	}
	if len(payload.Terms) > maxBatchTerms {
		problems.Write(problems.SearchInvalidQuery.Build().
			Detailf("the terms array exceeds the maximum of %d terms", maxBatchTerms).Problem(), w, req)
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), searchTimeout)
	defer cancel()

	results := make(map[string]batchTermResult, len(payload.Terms))
	for _, term := range payload.Terms {
		id := term.ID
		if id == "" {
			id = term.Text
		}
		it, serr := searchSvc.Search(ctx, term.Text)
		if serr != nil {
			results[id] = batchTermResult{Error: batchErrorText(serr)}
			continue
		}
		entry := batchTermResult{}
		if t, ok := it.Total(); ok {
			total := t
			entry.Total = &total
		}
		for {
			page, ierr := it.Next(ctx)
			if ierr != nil {
				if !errors.Is(ierr, pub.ErrIteratorDone) {
					entry.Error = batchErrorText(ierr)
				}
				break
			}
			entry.Locators = append(entry.Locators, page.Locators...)
		}
		it.Close()
		if entry.Locators == nil {
			entry.Locators = []manifest.Locator{}
		}
		results[id] = entry
	}

	body, merr := json.Marshal(map[string]interface{}{"results": results})
	if merr != nil {
		problems.Write(problems.Internal("failed marshalling batch results", merr), w, req)
		return
	}
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// batchErrorText reduces a search error to a stable in-band error tag.
func batchErrorText(err error) string {
	switch {
	case errors.Is(err, pub.ErrEmptyQuery):
		return "empty-query"
	case errors.Is(err, pub.ErrQueryTooLong):
		return "query-too-long"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	default:
		return "internal-error"
	}
}
