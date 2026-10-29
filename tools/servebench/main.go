// servebench measures an unmodified CLI binary over loopback HTTP. It never
// launches Reader or changes the user's installed readium executable.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readium/cli/internal/benchfixture"
)

type measurement struct {
	FirstByteMS float64 `json:"first_byte_ms"`
	CompleteMS  float64 `json:"complete_ms"`
	Bytes       int64   `json:"bytes"`
	Status      int     `json:"status"`
}
type summary struct {
	Count          int     `json:"count"`
	FirstByteP50MS float64 `json:"first_byte_p50_ms"`
	FirstByteP95MS float64 `json:"first_byte_p95_ms"`
	CompleteP50MS  float64 `json:"complete_p50_ms"`
	CompleteP95MS  float64 `json:"complete_p95_ms"`
	Bytes          int64   `json:"bytes_per_response"`
}
type workload struct {
	Round       int           `json:"round"`
	Media       string        `json:"media"`
	Name        string        `json:"name"`
	Concurrency int           `json:"concurrency"`
	WallMS      float64       `json:"wall_ms"`
	Summary     summary       `json:"summary"`
	Samples     []measurement `json:"samples"`
}
type memory struct {
	Round int    `json:"round"`
	Phase string `json:"phase"`
	// These are Go heap metrics from the CLI's own pprof endpoint, not the
	// benchmark client's heap. HeapSys is reserved heap, not resident memory.
	HeapAllocBytes    uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes      uint64  `json:"heap_sys_bytes"`
	TotalAllocBytes   uint64  `json:"total_alloc_bytes"`
	Mallocs           uint64  `json:"mallocs"`
	NumGC             uint64  `json:"num_gc"`
	LinuxRSSBytes     *uint64 `json:"linux_rss_bytes,omitempty"`
	LinuxPeakRSSBytes *uint64 `json:"linux_peak_rss_bytes,omitempty"`
}
type fixture struct {
	Name              string  `json:"name"`
	Bytes             int64   `json:"bytes"`
	SHA256            string  `json:"sha256"`
	ReadingOrderCount int     `json:"reading_order_count"`
	TopLevelTOCCount  int     `json:"top_level_toc_count"`
	DurationSeconds   float64 `json:"duration_seconds,omitempty"`
}
type report struct {
	Schema          int             `json:"schema"`
	At              string          `json:"at_utc"`
	GOOS            string          `json:"goos"`
	GOARCH          string          `json:"goarch"`
	GoVersion       string          `json:"harness_go_version"`
	CPUs            int             `json:"logical_cpus"`
	GOMAXPROCS      string          `json:"gomaxprocs_env"`
	GOMEMLIMIT      string          `json:"gomemlimit_env"`
	BinarySHA256    string          `json:"binary_sha256"`
	BinaryBuildInfo string          `json:"binary_build_info"`
	ToolkitRevision string          `json:"toolkit_revision,omitempty"`
	ToolkitStatus   string          `json:"toolkit_status,omitempty"`
	ReaderRoot      string          `json:"reader_root,omitempty"`
	ReaderSources   json.RawMessage `json:"reader_pinned_sources,omitempty"`
	Fixtures        []fixture       `json:"fixtures"`
	Workloads       []workload      `json:"workloads"`
	Memory          []memory        `json:"memory"`
	Notes           []string        `json:"notes"`
}
type process struct {
	cmd    *exec.Cmd
	client *http.Client
	base   string
	log    *os.File
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	binary := flag.String("binary", "", "CLI binary to measure (required)")
	output := flag.String("output", "baseline.json", "JSON output; existing files are not overwritten")
	rounds := flag.Int("rounds", 3, "Fresh CLI processes; each round opens all media types")
	requests := flag.Int("requests", 100, "Requests per warm workload")
	concurrency := flag.Int("concurrency", 8, "Concurrent requests per burst/workload")
	epub := flag.String("epub", "", "Optional real EPUB, copied into the isolated fixture library")
	audio := flag.String("audio", "", "Optional real audio (M4B, MP3, etc.), copied into the isolated fixture library")
	toolkit := flag.String("toolkit-root", "../go-toolkit", "Toolkit checkout used to build the CLI; records revision and status")
	reader := flag.String("reader-root", "", "Reader checkout to record docker/readium-sources.json")
	flag.Parse()
	if *binary == "" || *rounds < 1 || *requests < 1 || *concurrency < 1 {
		return fmt.Errorf("binary, positive rounds, requests and concurrency are required")
	}
	abs, err := filepath.Abs(*binary)
	if err != nil {
		return err
	}
	if _, err = os.Stat(*output); err == nil {
		return fmt.Errorf("output already exists: %s", *output)
	} else if !os.IsNotExist(err) {
		return err
	}
	dir, err := os.MkdirTemp("", "readium-servebench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	library := filepath.Join(dir, "library")
	files, err := benchfixture.Library(library)
	if err != nil {
		return err
	}
	for i, source := range map[int]string{0: *epub, 2: *audio} {
		if source == "" {
			continue
		}
		name := fmt.Sprintf("external-%d%s", i, strings.ToLower(filepath.Ext(source)))
		if err := copyFile(source, filepath.Join(library, name)); err != nil {
			return err
		}
		files[i] = name
	}
	r := report{Schema: 1, At: time.Now().UTC().Format(time.RFC3339), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU(), GOMAXPROCS: os.Getenv("GOMAXPROCS"), GOMEMLIMIT: os.Getenv("GOMEMLIMIT"), ReaderRoot: *reader,
		Notes: []string{"Loopback HTTP to the CLI; excludes Reader proxy, browser rendering and network/Wi-Fi latency.", "Cold means fresh CLI publication cache, with OS filesystem cache uncontrolled (not a cold-disk benchmark).", "Warm HTTP requests use identity encoding, persistent connections, and fully consume bodies; debug mode is enabled for memory snapshots.", "Heap snapshots labelled post_gc force GC and are taken outside timed workloads. RSS/peak RSS are sampled only on Linux; peak RSS covers process lifetime.", "Fixtures are synthetic unless external EPUB/audio overrides are specified. Generated WAV does not cover M4B chapter probing; CBZ repeats one PNG page.", "No 2x/5x improvement is claimed by this baseline. Production cache behavior is unchanged."}}
	bin, err := digest(abs)
	if err != nil {
		return err
	}
	r.BinarySHA256 = bin.SHA256
	buildInfo, err := exec.Command("go", "version", "-m", abs).CombinedOutput()
	if err != nil {
		return fmt.Errorf("binary build info: %w: %s", err, buildInfo)
	}
	r.BinaryBuildInfo = string(buildInfo)
	if *toolkit != "" {
		sha, err := exec.Command("git", "-C", *toolkit, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			return fmt.Errorf("toolkit revision: %w: %s", err, sha)
		}
		r.ToolkitRevision = strings.TrimSpace(string(sha))
		status, err := exec.Command("git", "-C", *toolkit, "status", "--porcelain").CombinedOutput()
		if err != nil {
			return fmt.Errorf("toolkit status: %w: %s", err, status)
		}
		r.ToolkitStatus = string(status)
	}

	if *reader != "" {
		r.ReaderSources, err = os.ReadFile(filepath.Join(*reader, "docker", "readium-sources.json"))
		if err != nil {
			return err
		}
	}
	for _, name := range files {
		f, err := digest(filepath.Join(library, name))
		if err != nil {
			return err
		}
		f.Name = name
		r.Fixtures = append(r.Fixtures, f)
	}
	for round := 1; round <= *rounds; round++ {
		fmt.Fprintf(os.Stderr, "round %d/%d\n", round, *rounds)
		// Serial cold opens get a separate process from the synchronized cold
		// burst. Otherwise one measurement would warm the other one's cache.
		cold, err := start(abs, library, filepath.Join(dir, fmt.Sprintf("cold-%d", round)))
		if err != nil {
			return err
		}
		err = func() error {
			defer cold.close()
			for _, name := range files {
				path := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte(name)) + "/manifest.json"
				w, err := cold.work(round, name, "cold_manifest_serial", path, "", 1, 1)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
			}
			return nil
		}()
		if err != nil {
			return err
		}
		p, err := start(abs, library, filepath.Join(dir, fmt.Sprintf("run-%d", round)))
		if err != nil {
			return err
		}
		err = func() error {
			defer p.close()
			snap := func(phase string, gc bool) error {
				m, err := p.memory(round, phase, gc)
				if err == nil {
					r.Memory = append(r.Memory, m)
				}
				return err
			}
			if err := snap("idle_post_gc", true); err != nil {
				return err
			}
			for _, name := range files {
				manifestPath := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte(name)) + "/manifest.json"
				// Synchronized concurrent first opens capture cache-miss amplification.
				w, err := p.work(round, name, "cold_manifest_burst", manifestPath, "", *concurrency, *concurrency)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
				body, _, err := p.get(manifestPath, "")
				if err != nil {
					return err
				}
				var manifest struct {
					ReadingOrder []struct{ Href string }
					TOC          []json.RawMessage
					Metadata     struct{ Duration float64 }
				}
				if err := json.Unmarshal(body, &manifest); err != nil {
					return err
				}
				if len(manifest.ReadingOrder) == 0 {
					return fmt.Errorf("empty reading order for %s", name)
				}
				if name == files[2] && manifest.Metadata.Duration <= 0 {
					return fmt.Errorf("audio duration missing for %s", name)
				}
				if round == 1 {
					for i := range r.Fixtures {
						if r.Fixtures[i].Name == name {
							r.Fixtures[i].ReadingOrderCount = len(manifest.ReadingOrder)
							r.Fixtures[i].TopLevelTOCCount = len(manifest.TOC)
							r.Fixtures[i].DurationSeconds = manifest.Metadata.Duration
						}
					}
				}
				href := strings.Split(manifest.ReadingOrder[len(manifest.ReadingOrder)/2].Href, "#")[0]
				assetPath := strings.TrimSuffix(manifestPath, "manifest.json") + strings.TrimPrefix(href, "/")
				// Validate that range responses contain the actual prefix of
				// the full resource, including the clamped short-page case.
				full, _, err := p.get(assetPath, "")
				if err != nil {
					return err
				}
				prefix, _, err := p.get(assetPath, "bytes=0-65535")
				if err != nil {
					return err
				}
				if !bytes.Equal(prefix, full[:min(len(full), 65536)]) {
					return fmt.Errorf("incorrect range prefix for %s", name)
				}

				for _, item := range []struct {
					name, path, rng string
					c               int
				}{
					{"warm_manifest", manifestPath, "", 1}, {"warm_manifest_concurrent", manifestPath, "", *concurrency},
					{"warm_asset", assetPath, "", 1}, {"warm_asset_concurrent", assetPath, "", *concurrency},
					{"warm_range", assetPath, "bytes=0-65535", 1},
				} {
					// Warm once before timing so lazy service/resource initialization is excluded.
					if _, _, err := p.get(item.path, item.rng); err != nil {
						return err
					}
					w, err := p.work(round, name, item.name, item.path, item.rng, *requests, item.c)
					if err != nil {
						return err
					}
					r.Workloads = append(r.Workloads, w)
				}
				if err := snap(name+"_post_workload", false); err != nil {
					return err
				}
				if err := snap(name+"_post_gc", true); err != nil {
					return err
				}
			}
			// Cross the ten-publication cache capacity, then revisit an early
			// publication. Copy identical bytes under different identities so
			// this measures cache pressure without changing media complexity.
			for _, name := range files {
				source := filepath.Join(library, name)
				for i := range 12 {
					copyName := fmt.Sprintf("churn-%02d-%s", i, name)
					dest := filepath.Join(library, copyName)
					if err := copyFile(source, dest); err != nil {
						return err
					}
					path := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte(copyName)) + "/manifest.json"
					w, err := p.work(round, name, "cache_pressure_open", path, "", 1, 1)
					if err != nil {
						return err
					}
					r.Workloads = append(r.Workloads, w)
				}
				path := "/webpub/" + base64.RawURLEncoding.EncodeToString([]byte(name)) + "/manifest.json"
				w, err := p.work(round, name, "cache_pressure_revisit", path, "", 1, 1)
				if err != nil {
					return err
				}
				r.Workloads = append(r.Workloads, w)
				if err := snap(name+"_cache_pressure_post_gc", true); err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	out, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
func digest(path string) (fixture, error) {
	f, err := os.Open(path)
	if err != nil {
		return fixture{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return fixture{Name: filepath.Base(path), Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		return err
	}
	return cerr
}
func start(binary, library, dir string) (*process, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	log, err := os.Create(filepath.Join(dir, "cli.log"))
	if err != nil {
		return nil, err
	}
	portFile := filepath.Join(dir, "port")
	cmd := exec.Command(binary, "serve", "--address", "127.0.0.1", "--port", "0", "--port-file", portFile, "--file-directory", library, "--debug")
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	transport := &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 32}
	p := &process{cmd: cmd, log: log, client: &http.Client{Transport: transport, Timeout: 60 * time.Second}}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(portFile); err == nil {
			port, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && port > 0 {
				p.base = fmt.Sprintf("http://127.0.0.1:%d", port)
				if _, _, err = p.get("/health", ""); err == nil {
					return p, nil
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.close()
	data, _ := os.ReadFile(filepath.Join(dir, "cli.log"))
	return nil, fmt.Errorf("CLI failed to start: %s", data)
}
func (p *process) close() {
	p.client.CloseIdleConnections()
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	_ = p.log.Close()
}
func (p *process) get(path, rng string) ([]byte, measurement, error) {
	return p.request(path, rng, true)
}
func (p *process) request(path, rng string, capture bool) ([]byte, measurement, error) {
	start := time.Now()
	var first time.Duration
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { first = time.Since(start) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), "GET", p.base+path, nil)
	if err != nil {
		return nil, measurement{}, err
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return nil, measurement{}, err
	}
	defer res.Body.Close()
	var body []byte
	var n int64
	if capture {
		// Metadata and validation reads retain at most 8 MiB in the harness. Full
		// media transfers are drained so real, large audiobooks remain safe to use.
		body, err = io.ReadAll(io.LimitReader(res.Body, 8<<20))
		n = int64(len(body))
	}
	if err == nil {
		var drained int64
		drained, err = io.Copy(io.Discard, res.Body)
		n += drained
	}
	m := measurement{float64(first) / float64(time.Millisecond), float64(time.Since(start)) / float64(time.Millisecond), n, res.StatusCode}
	if err != nil {
		return nil, m, err
	}
	expected := 200
	if rng != "" {
		expected = 206
	}
	if res.StatusCode != expected {
		return nil, m, fmt.Errorf("GET %s: status %d: %.300s", path, res.StatusCode, body)
	}
	if res.ContentLength >= 0 && res.ContentLength != n {
		return nil, m, fmt.Errorf("body length mismatch for %s", path)
	}
	return body, m, nil
}
func (p *process) work(round int, media, name, path, rng string, n, c int) (workload, error) {
	samples := make([]measurement, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	jobs := make(chan int, n)
	for i := range n {
		jobs <- i
	}
	close(jobs)
	for range c {
		wg.Go(func() {
			<-gate
			for i := range jobs {
				_, samples[i], errs[i] = p.request(path, rng, false)
			}
		})
	}
	start := time.Now()
	close(gate)
	wg.Wait()
	wall := time.Since(start)
	for _, err := range errs {
		if err != nil {
			return workload{}, err
		}
	}
	return workload{round, media, name, c, float64(wall) / float64(time.Millisecond), summarize(samples), samples}, nil
}
func summarize(samples []measurement) summary {
	first, full := make([]float64, len(samples)), make([]float64, len(samples))
	for i, s := range samples {
		first[i] = s.FirstByteMS
		full[i] = s.CompleteMS
	}
	sort.Float64s(first)
	sort.Float64s(full)
	pct := func(v []float64, p float64) float64 { i := int(mathCeil(p*float64(len(v)))) - 1; return v[max(0, i)] }
	return summary{len(samples), pct(first, .5), pct(first, .95), pct(full, .5), pct(full, .95), samples[0].Bytes}
}
func mathCeil(v float64) int {
	n := int(v)
	if float64(n) < v {
		return n + 1
	}
	return n
}
func (p *process) memory(round int, phase string, gc bool) (memory, error) {
	path := "/debug/pprof/heap?debug=1"
	if gc {
		path += "&gc=1"
	}
	body, _, err := p.get(path, "")
	if err != nil {
		return memory{}, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 4 && parts[0] == "#" && parts[2] == "=" {
			v, err := strconv.ParseUint(parts[3], 10, 64)
			if err == nil {
				values[parts[1]] = v
			}
		}
	}
	if _, ok := values["HeapAlloc"]; !ok {
		return memory{}, fmt.Errorf("pprof omitted HeapAlloc")
	}
	m := memory{Round: round, Phase: phase, HeapAllocBytes: values["HeapAlloc"], HeapSysBytes: values["HeapSys"], TotalAllocBytes: values["TotalAlloc"], Mallocs: values["Mallocs"], NumGC: values["NumGC"]}
	if runtime.GOOS == "linux" {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.cmd.Process.Pid))
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 3 {
				v, e := strconv.ParseUint(parts[1], 10, 64)
				if e == nil {
					v *= 1024
					if parts[0] == "VmRSS:" {
						m.LinuxRSSBytes = &v
					}
					if parts[0] == "VmHWM:" {
						m.LinuxPeakRSSBytes = &v
					}
				}
			}
		}
	}
	return m, nil
}
