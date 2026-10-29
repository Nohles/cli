package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/readium/cli/internal/benchfixture"
)

type directoryOptions struct {
	binary, library, output, storage, toolkit, toolkitRevision, reader string
	rounds, requests, concurrency                                      int
}
type directorySnapshot struct {
	ReadingOrderCount        int    `json:"reading_order_count"`
	TOCCount                 int    `json:"toc_count"`
	PositionsCount           int    `json:"positions_count"`
	NormalizedManifestSHA256 string `json:"normalized_manifest_sha256"`
	SelectionDigest          string `json:"selection_digest,omitempty"`
	SampledArchiveCount      int    `json:"sampled_archive_count"`
	ChildPageCount           int    `json:"child_page_count"`
}
type directoryReport struct {
	report
	Chapters        int                          `json:"chapters"`
	PagesPerChapter int                          `json:"pages_per_chapter"`
	Snapshot        map[string]directorySnapshot `json:"directory_validation"`
}
type directoryManifest struct {
	ReadingOrder []struct {
		Href   string `json:"href"`
		Width  uint   `json:"width"`
		Height uint   `json:"height"`
	} `json:"readingOrder"`
	TOC []struct {
		Href string `json:"href"`
	} `json:"toc"`
}
type directoryRegistration struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func directoryFiles(library string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(library, "Series"))
	if err != nil {
		return nil, err
	}
	if len(entries) != benchfixture.DirectoryComicChapters {
		return nil, fmt.Errorf("expected 100 chapter files, got %d", len(entries))
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		name := fmt.Sprintf("Chapter %03d.cbz", i+1)
		if entry.Name() != name || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected chapter: %s", entry.Name())
		}
		names[i] = name
	}
	return names, nil
}
func directoryBase() string {
	return "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte("Series")) + "/"
}
func selectionQuery(id string) string {
	if id == "" {
		return ""
	}
	return "?readiumSelection=" + url.QueryEscape(id)
}

func (p *process) registerDirectory(names []string) (directoryRegistration, measurement, error) {
	hrefs := make([]string, len(names))
	for i, name := range names {
		hrefs[i] = url.PathEscape(name)
	}
	payload, err := json.Marshal(map[string]any{"publication": map[string]string{"path": "Series"}, "selection": map[string]any{"version": 1, "readingOrder": hrefs}})
	if err != nil {
		return directoryRegistration{}, measurement{}, err
	}
	started := time.Now()
	var first time.Duration
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { first = time.Since(started) }}
	req, err := http.NewRequest("POST", p.base+"/_readium/directory-selections", bytes.NewReader(payload))
	if err != nil {
		return directoryRegistration{}, measurement{}, err
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	req.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return directoryRegistration{}, measurement{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	sample := measurement{FirstByteMS: float64(first) / float64(time.Millisecond), CompleteMS: float64(time.Since(started)) / float64(time.Millisecond), Bytes: int64(len(body)), Status: response.StatusCode}
	if err != nil {
		return directoryRegistration{}, sample, err
	}
	if response.StatusCode != 201 {
		return directoryRegistration{}, sample, fmt.Errorf("directory registration: %d: %.300s", response.StatusCode, body)
	}
	var registration directoryRegistration
	if err = json.Unmarshal(body, &registration); err != nil {
		return registration, sample, err
	}
	if registration.ID == "" || registration.Digest == "" {
		return registration, sample, fmt.Errorf("missing selection id/digest")
	}
	return registration, sample, nil
}
func (p *process) directoryResponse(path string, registration directoryRegistration) ([]byte, error) {
	res, err := p.client.Get(p.base + path)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("directory response %d: %.200s", res.StatusCode, body)
	}
	if registration.ID != "" && res.Header.Get("Readium-Directory-Selection") != "v1; digest="+registration.Digest {
		return nil, fmt.Errorf("selection was not acknowledged")
	}
	return body, nil
}
func (p *process) validateDirectory(names []string, registration directoryRegistration) (directorySnapshot, error) {
	query := selectionQuery(registration.ID)
	body, err := p.directoryResponse(directoryBase()+"manifest.json"+query, registration)
	if err != nil {
		return directorySnapshot{}, err
	}
	var m directoryManifest
	if err = json.Unmarshal(body, &m); err != nil {
		return directorySnapshot{}, err
	}
	if len(m.ReadingOrder) != len(names) || len(m.TOC) != len(names) {
		return directorySnapshot{}, fmt.Errorf("directory lost chapters: reading order %d, TOC %d", len(m.ReadingOrder), len(m.TOC))
	}
	for i, name := range names {
		expected := url.PathEscape(name)
		if m.ReadingOrder[i].Href != expected || m.TOC[i].Href != expected {
			return directorySnapshot{}, fmt.Errorf("chapter %d order differs: %q %q", i, m.ReadingOrder[i].Href, m.TOC[i].Href)
		}
	}
	// Only the self URL's ephemeral loopback port is normalized. All remaining
	// manifest metadata/links must compare byte-for-byte after canonical JSON.
	var raw any
	if err = json.Unmarshal(bytes.ReplaceAll(body, []byte(p.base), []byte("http://publication.test")), &raw); err != nil {
		return directorySnapshot{}, err
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return directorySnapshot{}, err
	}
	hash := sha256.Sum256(normalized)
	positions, _, err := p.get(directoryBase()+"~readium/positions.json"+query, "")
	if err != nil {
		return directorySnapshot{}, err
	}
	var locators struct {
		Total     int `json:"total"`
		Positions []struct {
			Href string `json:"href"`
		} `json:"positions"`
	}
	if err = json.Unmarshal(positions, &locators); err != nil {
		return directorySnapshot{}, err
	}
	if locators.Total != len(names) || len(locators.Positions) != len(names) {
		return directorySnapshot{}, fmt.Errorf("positions lost chapters")
	}
	for i, locator := range locators.Positions {
		if locator.Href != url.PathEscape(names[i]) {
			return directorySnapshot{}, fmt.Errorf("position order differs at %d", i)
		}
	}
	return directorySnapshot{ReadingOrderCount: len(names), TOCCount: len(m.TOC), PositionsCount: locators.Total, NormalizedManifestSHA256: hex.EncodeToString(hash[:]), SelectionDigest: registration.Digest}, nil
}

func runDirectoryComics(opts directoryOptions) error {
	if opts.binary == "" || opts.rounds < 1 || opts.requests < 1 || opts.concurrency < 1 {
		return fmt.Errorf("binary and positive rounds/requests/concurrency required")
	}
	if _, err := os.Stat(opts.output); err == nil {
		return fmt.Errorf("output exists: %s", opts.output)
	} else if !os.IsNotExist(err) {
		return err
	}
	library, err := filepath.Abs(opts.library)
	if err != nil {
		return err
	}
	binary, err := filepath.Abs(opts.binary)
	if err != nil {
		return err
	}
	names, err := directoryFiles(library)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "readium-directory-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	r := directoryReport{report: report{Schema: 3, At: time.Now().UTC().Format(time.RFC3339), LibraryDirectory: library, StorageLabel: opts.storage, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU(), GOMAXPROCS: os.Getenv("GOMAXPROCS"), GOMEMLIMIT: os.Getenv("GOMEMLIMIT"), ReaderRoot: opts.reader}, Chapters: len(names), PagesPerChapter: benchfixture.DirectoryComicPages, Snapshot: map[string]directorySnapshot{}}
	r.Notes = []string{"The parent manifest enumerates CBZ chapter resources; it does not flatten all image pages.", "Plain and Reader-style selected directory variants run in separate fresh CLI processes, including separate serial-cold and burst processes.", "Selection registration opens and caches the parent. Post-registration first GET/burst are cache hits; cold_selected_open_total includes registration plus first manifest, excluding CLI startup.", "Same loopback identity-encoded HTTP, GOMAXPROCS and filesystem cache policy as existing baselines; client/NAS caches are not flushed.", "Cache pressure opens twelve individual chapter publications, then revisits the parent. Media are read in place and never copied for timing."}
	bin, err := digest(binary)
	if err != nil {
		return err
	}
	r.BinarySHA256 = bin.SHA256
	build, err := exec.Command("go", "version", "-m", binary).CombinedOutput()
	if err != nil {
		return err
	}
	r.BinaryBuildInfo = string(build)
	if opts.toolkit != "" {
		rev, err := exec.Command("git", "-C", opts.toolkit, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			return err
		}
		r.ToolkitRevision = strings.TrimSpace(string(rev))
		status, err := exec.Command("git", "-C", opts.toolkit, "status", "--porcelain").CombinedOutput()
		if err != nil {
			return err
		}
		r.ToolkitStatus = string(status)
		if opts.toolkitRevision != "" {
			declared, err := exec.Command("git", "-C", opts.toolkit, "rev-parse", "--verify", opts.toolkitRevision+"^{commit}").CombinedOutput()
			if err != nil {
				return fmt.Errorf("invalid declared toolkit revision: %w", err)
			}
			r.ToolkitRevision = strings.TrimSpace(string(declared))
			r.ToolkitStatus = ""
			r.Notes = append(r.Notes, "Toolkit revision was explicitly declared for the measured binary; the harness checkout may differ. Binary build information and SHA-256 are recorded separately.")
		}
	}
	if opts.reader != "" {
		r.ReaderSources, err = os.ReadFile(filepath.Join(opts.reader, "docker", "readium-sources.json"))
		if err != nil {
			return err
		}
	}
	for _, name := range names {
		f, err := digest(filepath.Join(library, "Series", name))
		if err != nil {
			return err
		}
		f.Name = "Series/" + name
		r.Fixtures = append(r.Fixtures, f)
	}
	for round := 1; round <= opts.rounds; round++ {
		fmt.Fprintf(os.Stderr, "directory round %d/%d\n", round, opts.rounds)
		for _, mode := range []string{"plain", "selected"} {
			cold, err := start(binary, library, filepath.Join(scratch, fmt.Sprintf("%s-cold-%d", mode, round)))
			if err != nil {
				return err
			}
			err = func() error {
				defer cold.close()
				var registration directoryRegistration
				began := time.Now()
				if mode == "selected" {
					var sample measurement
					registration, sample, err = cold.registerDirectory(names)
					if err != nil {
						return err
					}
					r.Workloads = append(r.Workloads, workload{Round: round, Media: mode, Name: "selection_register", Concurrency: 1, WallMS: sample.CompleteMS, Summary: summarize([]measurement{sample}), Samples: []measurement{sample}})
				}
				offset := float64(time.Since(began)) / float64(time.Millisecond)
				w, err := cold.work(round, mode, "cold_manifest_serial", directoryBase()+"manifest.json"+selectionQuery(registration.ID), "", 1, 1)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
				if mode == "selected" {
					sample := w.Samples[0]
					sample.FirstByteMS += offset
					sample.CompleteMS += offset
					r.Workloads = append(r.Workloads, workload{Round: round, Media: mode, Name: "cold_selected_open_total", Concurrency: 1, WallMS: sample.CompleteMS, Summary: summarize([]measurement{sample}), Samples: []measurement{sample}})
				}
				_, err = cold.validateDirectory(names, registration)
				return err
			}()
			if err != nil {
				return err
			}
			p, err := start(binary, library, filepath.Join(scratch, fmt.Sprintf("%s-burst-%d", mode, round)))
			if err != nil {
				return err
			}
			err = func() error {
				defer p.close()
				snap := func(phase string, gc bool) error {
					m, err := p.memory(round, mode+"_"+phase, gc)
					if err == nil {
						r.Memory = append(r.Memory, m)
					}
					return err
				}
				if err := snap("idle_post_gc", true); err != nil {
					return err
				}
				var registration directoryRegistration
				if mode == "selected" {
					registration, _, err = p.registerDirectory(names)
					if err != nil {
						return err
					}
				}
				query := selectionQuery(registration.ID)
				path := directoryBase() + "manifest.json" + query
				w, err := p.work(round, mode, "cold_manifest_burst", path, "", opts.concurrency, opts.concurrency)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
				snapshot, err := p.validateDirectory(names, registration)
				if err != nil {
					return err
				}
				if previous, ok := r.Snapshot[mode]; ok && previous.NormalizedManifestSHA256 != snapshot.NormalizedManifestSHA256 {
					return fmt.Errorf("manifest changed between rounds")
				}
				if err := snap("parent_post_gc", true); err != nil {
					return err
				}
				for _, item := range []struct {
					name, path, rng string
					c               int
				}{
					{"warm_manifest", path, "", 1}, {"warm_manifest_concurrent", path, "", opts.concurrency},
					{"warm_positions", directoryBase() + "~readium/positions.json" + query, "", 1},
					{"warm_chapter_archive", directoryBase() + url.PathEscape(names[49]) + query, "", 1},
					{"warm_chapter_archive_concurrent", directoryBase() + url.PathEscape(names[49]) + query, "", opts.concurrency},
					{"warm_chapter_range", directoryBase() + url.PathEscape(names[49]) + query, "bytes=0-65535", 1},
				} {
					if _, _, err := p.get(item.path, item.rng); err != nil {
						return err
					}
					w, err := p.work(round, mode, item.name, item.path, item.rng, opts.requests, item.c)
					if err != nil {
						return err
					}
					r.Workloads = append(r.Workloads, w)
				}
				for _, i := range []int{0, 49, 99} {
					body, err := p.directoryResponse(directoryBase()+url.PathEscape(names[i])+query, registration)
					if err != nil {
						return err
					}
					hash := sha256.Sum256(body)
					if hex.EncodeToString(hash[:]) != r.Fixtures[i].SHA256 {
						return fmt.Errorf("chapter %d bytes changed", i+1)
					}
					prefix, _, err := p.get(directoryBase()+url.PathEscape(names[i])+query, "bytes=0-65535")
					if err != nil {
						return err
					}
					if !bytes.Equal(prefix, body[:min(len(body), 65536)]) {
						return fmt.Errorf("chapter range mismatch")
					}
					snapshot.SampledArchiveCount++
				}
				childPath := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte("Series/"+names[49])) + "/manifest.json"
				child, _, err := p.get(childPath, "")
				if err != nil {
					return err
				}
				var m directoryManifest
				if err = json.Unmarshal(child, &m); err != nil {
					return err
				}
				if len(m.ReadingOrder) != benchfixture.DirectoryComicPages {
					return fmt.Errorf("child page count changed")
				}
				for _, page := range m.ReadingOrder {
					if page.Width != 256 || page.Height != 384 {
						return fmt.Errorf("child image dimensions changed")
					}
				}
				snapshot.ChildPageCount = len(m.ReadingOrder)
				w, err = p.work(round, mode, "warm_child_manifest", childPath, "", opts.requests, 1)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
				if err := snap("workload_post_gc", true); err != nil {
					return err
				}
				for _, name := range names[:12] {
					childPath := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte("Series/"+name)) + "/manifest.json"
					w, err := p.work(round, mode, "chapter_cache_pressure_open", childPath, "", 1, 1)
					if err != nil {
						return err
					}
					r.Workloads = append(r.Workloads, w)
				}
				w, err = p.work(round, mode, "parent_revisit_after_12_chapters", path, "", 1, 1)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
				r.Snapshot[mode] = snapshot
				return snap("cache_pressure_post_gc", true)
			}()
			if err != nil {
				return err
			}
		}
	}
	out, err := os.OpenFile(opts.output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
