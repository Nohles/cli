# Reader serving baseline

This directory tracks baseline and optimized Reader / Readium CLI / Go toolkit
serving results. See the [final release qualification](release-qualification-2026-10-29.md),
[previous combined candidate](optimization-2026-10-29.md),
[original Linux baseline](baseline-2026-10-29.md), and
[matched HDD NAS comparison](storage-comparison-2026-10-29.md). Original
measurement artifacts remain unchanged; optimization commits add new results
and update the PR's current comparison. Commands below measure the selected
checkout, so use each report's recorded source revisions to reproduce before
and after; a binary filename alone does not establish its version.

## Run on Linux, macOS or Windows

Use the Go version declared in `go.mod`. Check out CLI and toolkit as sibling
folders. The release pins its reviewed toolkit revision and needs no local replacement.
For optional source development, use the paired checkout as a local-only override:

```sh
go mod edit -replace=github.com/nohles/go-toolkit=../go-toolkit
go build -o readium-baseline ./cmd
go run ./tools/servebench --binary ./readium-baseline --output baseline.json
```

On Windows, use `readium-baseline.exe` for the output binary and binary argument.
No shell script, FFmpeg, private library, network service or database is required
for the generated fixture run. Every output file is created exclusively, so the
harness refuses to overwrite earlier measurements. Temporary libraries and
child processes are removed when the run finishes or returns an error. An
explicit `--library-dir` is retained, along with its media.

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

## Compare a mounted library, including an HDD NAS

`--epub` and `--audio` copy files into a local temporary library. To measure a
NAS, use **`--library-dir`** so all parsing, fingerprinting and resource reads
use the mounted files in place. The server binary and request client still run
locally. These paths are examples; preparation refuses an existing directory:

```sh
go run ./tools/servebench --prepare-only --library-dir /path/new-fixtures
# Copy those exact files to a new directory on the target storage, then verify hashes.
go run ./tools/servebench --binary ./readium-baseline --library-dir /mnt/Media/Reader_Testing/synthetic --storage-label 'HDD NAS over SMB 3.1.1' --rounds 5 --requests 100 --output nas-synthetic.json
```

`--prepare-only` optionally accepts `--epub` and `--audio` to copy real media
into the newly-created fixture library. In-place runs use `--files` to select
three basenames in EPUB, comic, audio order; for the Moby-Dick/M4B baseline use
`--files external-0.epub,comic.cbz,external-2.m4b`. In-place mode refuses
`--epub`/`--audio` copies to prevent accidentally measuring temporary storage.
Existing media are never regenerated or overwritten. Cache-pressure copies are
created in a unique scratch subdirectory on the **same selected filesystem**;
only that owned scratch directory is deleted after the run. Results record the
absolute library path and the supplied storage label.

Go route/cache benchmarks can use the same files without generating a temporary
library. Run sequentially after the HTTP measurements:

```sh
READIUM_BENCH_LIBRARY=/mnt/Media/Reader_Testing/synthetic GOMAXPROCS=4 go test ./pkg/serve -run '^$' -bench '^(BenchmarkServing|BenchmarkPublicationCache)$' -benchmem -count=5 -benchtime=500ms
```

`READIUM_BENCH_FILES` can override the default three fixture basenames. Default
benchmark behavior remains the original generated temporary library. Profiles
accept the same environment variables. See the [HDD NAS storage comparison](storage-comparison-2026-10-29.md)
for exact input hashes, matched local-control results and complete NAS results.
These runs measure application-cold and warm serving, not uncached physical HDD
throughput; neither client filesystem caches nor NAS caches are flushed.

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

## Directory-of-CBZ publications

The [100-CBZ directory comparison](directory-comics-2026-10-29.md) covers 100
24-page chapters, both plain folder and Reader selection v1. Registration itself
parses/caches the selected parent, so use registration plus first GET as the
selected first-open metric; its subsequent first GET/burst is already warm.

```sh
go build -o servebench ./tools/servebench
./servebench --directory-comics --prepare-only --library-dir /path/new-directory-library
GOMAXPROCS=4 ./servebench --directory-comics --binary /path/measured-cli --library-dir /mnt/Media/Reader_Testing/directory-comics --directory-toolkit-revision FULL_TOOLKIT_SHA --rounds 5 --requests 100 --output /path/new-directory-results.json
READIUM_DIRECTORY_LIBRARY=/mnt/Media/Reader_Testing/directory-comics GOMAXPROCS=4 go test ./pkg/serve -run '^$' -bench '^BenchmarkDirectoryComics$' -benchmem -count=5 -benchtime=500ms
```

Copy the generated library to the desired storage once and verify hashes before
measurement. `--directory-toolkit-revision` explicitly records the source used
for an external binary rather than assuming the harness checkout matches it.
The fixture, complete normalized parent manifest, chapter order, TOC, positions,
selection acknowledgement, sampled CBZ/range bytes and child page dimensions are
validated. The parent enumerates chapter archives rather than flattening all
image pages. Cache pressure opens twelve individual chapter publications and
revisits the parent without making directory copies. Original media/results and
production serving behavior are unchanged.
