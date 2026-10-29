# Reader serving baseline

This is the baseline phase of the Reader / Readium CLI / Go toolkit performance
work. Serving code, cache policies, cache capacities and memory limits are
unchanged. See the [recorded Linux baseline](baseline-2026-10-29.md).
Subsequent optimization commits should update the same PR and add a
new result file rather than replace the original measurements.

## Run on Linux, macOS or Windows

Use the Go version declared in `go.mod`. Check out CLI and toolkit as sibling
folders. The CLI currently has a development-only absolute toolkit replacement;
point it at your local paired checkout before building (keep this local change
out of performance commits):

```sh
go mod edit -replace=github.com/nohles/go-toolkit=../go-toolkit
go build -o readium-baseline ./cmd
go run ./tools/servebench --binary ./readium-baseline --output baseline.json
```

On Windows, use `readium-baseline.exe` for the output binary and binary argument.
No shell script, FFmpeg, private library, network service or database is required
for the generated fixture run. Every output file is created exclusively, so the
harness refuses to overwrite earlier measurements. Temporary libraries and
child processes are removed when the run finishes or returns an error.

For a more representative EPUB and chaptered audiobook, supply files explicitly:

```sh
go run ./tools/servebench --binary ./readium-baseline --epub /path/book.epub --audio /path/book.m4b --reader-root /path/Reader --output real-media-baseline.json
```

The optional Reader path records `docker/readium-sources.json` for comparison
with the measured CLI's build information and paired toolkit commit. It does
not start Reader. The binary SHA-256, Go build information, fixture SHA-256s,
OS/architecture, environment settings, raw latency samples and memory snapshots
are recorded in JSON. `--toolkit-root` defaults to `../go-toolkit`; it records
that checkout's revision/status, so supply the checkout used for the build.
This metadata is declarative: the harness cannot prove an arbitrary external
binary was built with that checkout.

For comparable runs, hold the binary's Go version, machine, power mode, media,
Go environment, request count, concurrency and background load constant. Run
measurements sequentially, without simultaneous tests or builds. This Linux
baseline used `GOMAXPROCS=4`, unset `GOMEMLIMIT`, five rounds and 100 requests per
warm workload. macOS/Linux can set `GOMAXPROCS=4` before commands; PowerShell
uses `$env:GOMAXPROCS = "4"`. Do not set a production memory limit based on
these small fixtures alone.

## What gets measured

- A serial first manifest request in a fresh CLI process, separately from an
  eight-request synchronized first-open burst in another fresh process.
- Warm manifest, full middle reading-order asset and prefix range requests;
  warm manifests and full assets also run at concurrency eight. Responses are
  fully consumed. Range bytes are checked against the actual asset prefix.
- Twelve additional identities per media type apply pressure to the current
  ten-publication TinyLFU cache. Revisits measure its resulting behavior;
  admission/eviction is policy-dependent, so they are not assumed to miss.
- CLI Go heap allocation and reservation, cumulative allocation count/bytes
  and GC count. Forced-GC snapshots distinguish retained Go heap from churn.
  Linux additionally records current RSS and lifetime peak RSS. Other platforms
  omit RSS fields instead of reporting zero. RSS includes runtime/code/stack
  memory and is not interchangeable with Go heap allocation.

A fresh process clears application caches, **not** the OS filesystem cache.
These are application-cold opens, not cold-disk results. Each round uses a fresh
process for serial opens and a separate process for burst/warm/cache-pressure
work; media types within each process share heap/cache state. Post-media memory
snapshots are cumulative process state, not the incremental cost of that media.
pprof memory collection runs outside measured workloads but adds overhead to
cumulative allocation counters. Debug mode is enabled for these snapshots.

HTTP samples use loopback, persistent connections and identity encoding, matching
Reader's explicit `accept-encoding: identity` upstream policy. They exclude the
Reader proxy, client device, Wi-Fi, browser decoding and rendering. Timed bodies
are streamed into a discard sink; untimed metadata/validation captures retain at
most 8 MiB in the harness even with a large real audiobook.

The synthetic EPUB has 120 text chapters, the CBZ has 240 stored copies of a
valid 256×384 PNG, and the WAV contains 60 seconds of valid mono PCM audio. They
are stable fixtures, not representative distributions of a private library.
WAV does not exercise M4B chapter extraction; the recorded additional run uses a
real EPUB fixture and an FFmpeg-generated M4B with twelve chapters. Large
multifile audiobooks, co-located selected comic directories, remote HTTP/S3/GCS,
24-hour sessions, multiple Reader library processes, and OS-specific filesystem
behavior still need separate qualification.

## Separate cache and route costs

```sh
go test ./pkg/serve -run '^$' -bench '^(BenchmarkServing|BenchmarkPublicationCache)$' -benchmem -count=5 -benchtime=500ms
```

`BenchmarkServing` traverses the real router, auth, source fingerprint, cache,
manifest serialization and asset streaming without network/client overhead.
`BenchmarkPublicationCache/hit` measures lookup plus retain/release directly;
`open_close` evicts before each toolkit parse and includes closing the previous
publication. Fixture construction and warm-up are outside timed loops. Set
`GOMAXPROCS` as above. Allocated B/op is churn, not retained memory.

To reproduce the allocation/CPU attribution:

```sh
go test ./pkg/serve -run '^$' -bench '^BenchmarkServing/audio.wav/manifest$' -benchtime=3s -memprofile allocs.pprof -cpuprofile cpu.pprof -o serve.test
go tool pprof -top -alloc_space serve.test allocs.pprof
go tool pprof -top serve.test cpu.pprof
```

Go memory profiles also include setup allocations; use call-stack attribution,
not their whole-process total as a per-request figure. Run `go test ./...` for
correctness separately from performance measurement. The harness's tests cover
complete large-body draining, bounded capture, HTTP errors, clamped ranges and
percentile calculation.

## Maintaining before/after results

Keep the baseline artifact immutable. For each optimization commit, run the same
commands and fixture hashes on the same platform; add new JSON/benchmark files.
Update the PR's current-results table with baseline and latest p50/p95 latency,
allocated B/op, retained heap and RSS, along with the exact measured source SHAs.
Compute each speedup as baseline latency / current latency for the same workload;
do not extrapolate a manifest speedup to page delivery or audio playback.
Include cold and warm regressions, not just favorable rows. Compare equal work
and reading-order/TOC/duration data; cache growth must be justified with retained
heap/RSS, not request allocation counts alone.
