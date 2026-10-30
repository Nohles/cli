"""Rebuild the storage comparison from committed raw results.

Requires Python 3 and matplotlib for PNG export. Measurement itself is Go-only.
Usage: python tools/servebench/compare_storage.py
"""
from pathlib import Path
from collections import defaultdict
import json
import math
import re
import statistics

ROOT = Path(__file__).resolve().parents[2]
DOCS = ROOT / 'docs/performance'
OUT = DOCS / 'storage-comparison-2026-10-29'
LABELS = ['Original local', 'Local control', 'HDD NAS · SMB']
COLORS = ['#9298a1', '#2874be', '#dd7c26']
WORKLOADS = {
    'cold_manifest_serial': 'Cold manifest, serial',
    'cold_manifest_burst': 'Cold manifest, 8-client burst',
    'warm_manifest': 'Warm manifest',
    'warm_manifest_concurrent': 'Warm manifest, concurrency 8',
    'warm_asset': 'Warm full asset',
    'warm_asset_concurrent': 'Warm full asset, concurrency 8',
    'warm_range': 'Warm prefix range',
    'cache_pressure_open': 'Cache-pressure opens',
    'cache_pressure_revisit': 'Revisit after cache pressure',
}
MEDIA = {
    'synthetic': [('book.epub', 'Synthetic EPUB'), ('comic.cbz', '240-page CBZ'), ('audio.wav', '60-second WAV')],
    'moby-m4b': [('external-0.epub', 'Moby-Dick EPUB'), ('comic.cbz', '240-page CBZ'), ('external-2.m4b', '12-chapter M4B')],
}


def load(dataset):
    return [json.loads(p.read_text()) for p in [
        DOCS / f'baseline-2026-10-29/http-{dataset}.json',
        OUT / f'local-http-{dataset}.json',
        OUT / f'nas-http-{dataset}.json',
    ]]


def grouped(report):
    result = defaultdict(list)
    for work in report['workloads']:
        result[work['media'], work['name']].extend(work['samples'])
    return result


def percentile(samples, field, p):
    values = sorted(x[field] for x in samples)
    return values[math.ceil(p * len(values)) - 1]


def memory(report, phase, field):
    return statistics.median(x[field] for x in report['memory'] if x['phase'] == phase) / 2**20


def benches(path):
    rows = defaultdict(list)
    for line in path.read_text().splitlines():
        match = re.match(r'(Benchmark\S+)\s+\d+\s+(\d+) ns/op\s+(\d+) B/op\s+(\d+) allocs/op', line)
        if match:
            rows[match[1]].append(tuple(map(int, match.groups()[1:])))
    return {name: [statistics.median(c) for c in zip(*values)] for name, values in rows.items()}


def main():
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    import numpy as np

    reports = {d: load(d) for d in MEDIA}
    for dataset, rs in reports.items():
        for r in rs[1:]:
            assert r['binary_sha256'] == rs[0]['binary_sha256'], 'different HTTP binary'
            assert r['fixtures'] == rs[0]['fixtures'], 'different input media or publication information'
        assert rs[2]['library_directory'].startswith('/mnt/Media/Reader_Testing/'), 'NAS media not served in place'
        # Preserve identical workload counts, concurrency and byte counts. Only
        # timings, process memory and filesystem location should differ.
        signature = lambda r: [(w['round'], w['media'], w['name'], w['concurrency'], len(w['samples']), w['summary']['bytes_per_response'] if w['name'] != 'cache_pressure_open' else None) for w in r['workloads']]
        assert signature(rs[0]) == signature(rs[1]) == signature(rs[2])

    groups = {d: [grouped(r) for r in rs] for d, rs in reports.items()}
    payload = {}
    for dataset, gs in groups.items():
        payload[dataset] = {}
        for kind in WORKLOADS:
            payload[dataset][kind] = []
            for name, label in MEDIA[dataset]:
                row = {'label': label, 'series': []}
                for g in gs:
                    samples = g[name, kind]
                    row['series'].append({
                        'complete_p50': percentile(samples, 'complete_ms', .5),
                        'complete_p95': percentile(samples, 'complete_ms', .95),
                        'first_p50': percentile(samples, 'first_byte_ms', .5),
                        'first_p95': percentile(samples, 'first_byte_ms', .95),
                        'count': len(samples),
                    })
                payload[dataset][kind].append(row)

    # An immutable original series plus a contemporaneous control distinguishes
    # storage effects from host/run-to-run drift.
    plt.rcParams.update({'font.family': 'DejaVu Sans', 'font.size': 10})
    fig, axes = plt.subplots(2, 2, figsize=(13, 8.5))
    x = np.arange(3)
    for ax, kind, title in zip(axes[0], ['cold_manifest_serial', 'warm_manifest'], ['Cold manifest · median complete response', 'Warm manifest · median complete response']):
        rows = payload['moby-m4b'][kind]
        for i, label in enumerate(LABELS):
            bars = ax.bar(x + (i-1)*.25, [r['series'][i]['complete_p50'] for r in rows], .24, label=label, color=COLORS[i])
            ax.bar_label(bars, fmt='%.2f', padding=3, fontsize=8)
        ax.set_xticks(x, ['Moby-Dick EPUB', '240-page CBZ', '12-chapter M4B'])
        ax.set_ylabel('Milliseconds · lower is faster')
        ax.set_title(title, loc='left', weight='bold', fontsize=11)
        ax.set_ylim(0, ax.get_ylim()[1]*1.2)
    rs = reports['moby-m4b']
    heap = [memory(r, 'external-2.m4b_post_gc', 'heap_alloc_bytes') for r in rs]
    rss = [memory(r, 'external-2.m4b_post_gc', 'linux_rss_bytes') for r in rs]
    for i, label in enumerate(LABELS):
        bars = axes[1, 0].bar(np.arange(2)+(i-1)*.25, [heap[i], rss[i]], .24, color=COLORS[i])
        axes[1, 0].bar_label(bars, fmt='%.2f', padding=3, fontsize=8)
    axes[1, 0].set_xticks([0, 1], ['Live Go heap after GC', 'Sampled Linux RSS'])
    axes[1, 0].set_ylabel('MiB · per CLI process')
    axes[1, 0].set_title('Memory after all three media workloads · median', loc='left', weight='bold', fontsize=11)
    axes[1, 0].set_ylim(0, axes[1, 0].get_ylim()[1]*1.15)
    peaks = [max(m['linux_peak_rss_bytes'] for m in r['memory'])/2**20 for r in rs]
    bars = axes[1, 1].bar(x, peaks, color=COLORS, width=.65)
    axes[1, 1].bar_label(bars, fmt='%.2f', padding=3, fontsize=9)
    axes[1, 1].set_xticks(x, LABELS)
    axes[1, 1].set_ylabel('MiB · per CLI process')
    axes[1, 1].set_title('Highest observed lifetime peak RSS', loc='left', weight='bold', fontsize=11)
    axes[1, 1].set_ylim(0, max(peaks)*1.2)
    for ax in axes.flat:
        ax.spines[['top', 'right']].set_visible(False)
        ax.grid(axis='y', alpha=.18)
        ax.set_axisbelow(True)
    fig.suptitle('Readium CLI baseline · local filesystem and HDD NAS over SMB', fontsize=16, weight='bold', x=.04, ha='left')
    handles, labels = axes[0, 0].get_legend_handles_labels()
    fig.legend(handles, labels, ncol=3, loc='upper left', bbox_to_anchor=(.035, .945), frameon=False)
    fig.text(.04, .025, 'Same binary and exact media hashes · 5 rounds · 100 requests per warm workload · concurrency 8 where specified\nApplication-cold only: client/NAS filesystem caches enabled. Linux/WSL; CLI and HTTP client local. No production optimization applied.', fontsize=9, color='#555555')
    fig.subplots_adjust(top=.86, bottom=.13, left=.07, right=.98, hspace=.42, wspace=.23)
    fig.savefig(OUT/'storage-comparison.png', dpi=170, facecolor='white')
    plt.close(fig)

    html = '''<!doctype html><html><head><meta charset="utf-8"><style>
:root{font-family:var(--font-sans,system-ui)}body{margin:0;color:var(--foreground,#222)}.controls{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0 20px}label{font-size:12px}select{display:block;margin-top:4px;padding:6px;background:var(--secondary,#eee);color:var(--foreground,#222);border:1px solid var(--border,#ccc);border-radius:6px}.group{margin:18px 0}.name{font-size:14px;font-weight:600;margin-bottom:7px}.barrow{display:flex;align-items:center;gap:8px;height:25px;font-size:12px}.barrow>span{width:105px;flex-shrink:0;color:var(--muted-foreground,#666)}.track{flex:1;background:var(--secondary,#eee);height:12px;border-radius:3px}.bar{height:12px;border-radius:3px}.v{width:65px;text-align:right;font-family:var(--font-mono,monospace);font-size:12px}.s0{background:var(--chart-3,#9298a1)}.s1{background:var(--chart-1,#2874be)}.s2{background:var(--chart-2,#dd7c26)}p{font-size:12px;line-height:1.5;color:var(--muted-foreground,#666)}#count{margin-bottom:0}.note{border-top:1px solid var(--border,#ccc);padding-top:12px}
</style></head><body><div class="controls"><label>Dataset<select id="dataset"><option value="moby-m4b">Moby-Dick / CBZ / chaptered M4B</option><option value="synthetic">Synthetic EPUB / CBZ / WAV</option></select></label><label>Workload<select id="workload">'''
    html += ''.join(f'<option value="{k}"'+(' selected' if k=='warm_manifest' else '')+f'>{v}</option>' for k,v in WORKLOADS.items())
    html += '''</select></label><label>Latency<select id="metric"><option value="complete_p50">Complete response · p50</option><option value="complete_p95">Complete response · p95</option><option value="first_p50">First byte · p50</option><option value="first_p95">First byte · p95</option></select></label></div><div id="chart" role="img" aria-label="Local and NAS response latency comparison"></div><p id="count"></p><p class="note">Milliseconds; lower is faster. Original local baseline is preserved; local control was rerun with the NAS harness. Five rounds, same binary and input SHA-256s. Cold = fresh publication cache; OS and NAS caches remain enabled. Reader proxy, remote client network and rendering are excluded. This is a storage comparison, not an optimization speedup.</p><script>const data='''+json.dumps(payload,separators=(',',':'))+''';const labels=['Original local','Local control','HDD NAS · SMB'];function render(){const d=document.getElementById('dataset').value,w=document.getElementById('workload').value,m=document.getElementById('metric').value,rows=data[d][w],max=Math.max(...rows.flatMap(r=>r.series.map(s=>s[m])))*1.05;document.getElementById('chart').innerHTML=rows.map(r=>'<div class="group"><div class="name">'+r.label+'</div>'+r.series.map((s,i)=>'<div class="barrow"><span>'+labels[i]+'</span><div class="track"><div class="bar s'+i+'" style="width:'+(100*s[m]/max)+'%"></div></div><div class="v">'+s[m].toFixed(3)+'</div></div>').join('')+'</div>').join('');document.getElementById('count').textContent=rows[0].series[0].count+' samples per medium per storage result.';}for(const e of document.querySelectorAll('select'))e.addEventListener('change',render);render();</script></body></html>'''
    (OUT/'storage-comparison.html').write_text(html)

    s = '''# HDD NAS and local storage baseline — 2026-10-29

The exact original media are now retained under `/mnt/Media/Reader_Testing/`,
in `synthetic/` and `moby-m4b/` subfolders. SHA-256s, publication reading order,
TOC counts, audio duration, workload counts/concurrency, selected-media response lengths, and
HTTP binary SHA-256 match the original baseline. No production serving or cache
behavior has changed.

![Local and HDD NAS baseline comparison](storage-comparison-2026-10-29/storage-comparison.png)

[Interactive latency comparison](storage-comparison-2026-10-29/storage-comparison.html)
includes both datasets, every workload, first-byte/complete response and p50/p95.

## Setup and interpretation

- NAS is HDD-backed, confirmed by its owner. `/mnt/Media` is mounted using
  Linux CIFS / SMB 3.1.1 with `cache=strict`, `actimeo=1`, `closetimeo=1`,
  and 4 MiB negotiated read/write sizes. Media are served **in place** from
  the NAS, including same-filesystem cache-pressure copies.
- CLI and HTTP client stay on the same Linux/WSL host. Local control uses an
  ext4 virtual disk; its physical host storage type is not inferred from
  the guest's rotational flag. This is local-filesystem versus HDD NAS over
  SMB, not an isolated SSD-versus-HDD or physical disk throughput test.
- Same original binary: CLI `d123c32`, toolkit `c070049`, matching Reader pins.
  Harness `67a84ea` adds in-place library selection and safe owned scratch
  files. Five rounds, 100 requests per warm workload, concurrency eight where
  specified, Go 1.26.0, `GOMAXPROCS=4`, `GOMEMLIMIT` unset, debug mode on.
- Run order: local synthetic HTTP, local Moby-Dick/M4B HTTP, local Go benchmarks
  and profiles, then NAS equivalents. All timed suites run sequentially.
  A matched local rerun distinguishes host/run-to-run drift from NAS effects;
  original results remain unchanged and are shown alongside it. Cache-pressure
  copies use an owned scratch namespace; its longer manifest self URL is the
  sole allowed response-length difference from the original run.
- "Cold" clears only the CLI's publication cache. Copying/hash verification
  and repeated reads can warm client and server filesystem caches. No global
  cache flush, NAS restart or cache-policy change was performed. These are the
  **same application-cold/warm workloads**, not uncached NAS platter reads.
- Memory figures are for the local CLI process, including its retained heap
  and RSS. NAS server RAM and client kernel SMB/page-cache memory are outside
  those process measurements. File transfer latency includes the mounted SMB
  path; HTTP to a remote user and the Reader proxy are still excluded.

## HTTP latency, complete response

Numbers are pooled nearest-rank **p50 / p95 milliseconds** over the same number
of requests per row: cold serial/revisit 5, cold burst 40, warm 500, pressure
opens 60. NAS/local ratios compare medians with the matched local control;
values above one mean higher NAS latency. Cold tails have few samples and
should not be treated as stable production tail estimates. Full assets are
one middle reading-order resource, not the entire publication. Range requests
are `bytes=0-65535`, clamped for shorter resources.
'''
    for dataset, gs in groups.items():
        s += f'\n### {"Moby-Dick / CBZ / chaptered M4B" if dataset=="moby-m4b" else "Synthetic EPUB / CBZ / WAV"}\n\n'
        s += '| Media | Workload | Original local | Local control | HDD NAS | NAS/local p50 |\n| --- | --- | ---: | ---: | ---: | ---: |\n'
        for name,label in MEDIA[dataset]:
            for kind,title in WORKLOADS.items():
                values = [f'{percentile(g[name,kind],"complete_ms",.5):.3f} / {percentile(g[name,kind],"complete_ms",.95):.3f}' for g in gs]
                ratio = percentile(gs[2][name,kind], 'complete_ms', .5)/percentile(gs[1][name,kind], 'complete_ms', .5)
                s += f'| {label} | {title} | '+ ' | '.join(values)+f' | {ratio:.2f}× |\n'
        s += '\nFirst-byte latency is retained in each raw JSON file and the interactive chart.\n'

    s += """
## Observations

The fresh local control and the NAS have similar warm-request latency for these
cacheable fixtures. Small differences in either direction overlap the observed
run-to-run variation; they do not show that NAS disks are faster than local
storage. The original and rerun local results are both shown for that reason.

Audio first-open contention is more visible: the M4B eight-client cold burst is
21.058 ms median / 35.263 ms p95 on the NAS versus 7.837 / 10.595 ms locally
(2.69× higher median latency). WAV cold bursts are 30.277 / 79.722 ms versus
8.997 / 13.493 ms (3.37×). After cache pressure, audio revisits also show higher
NAS latency, but each such row has only five samples and policy-dependent cache
admission; the data does not isolate a physical disk, SMB metadata or parsing
cause. These are candidates for the next optimization investigation, not a new
claim about uncached HDD throughput.

For Moby-Dick/CBZ/M4B, post-workload median live heap is 5.13 MiB on the NAS
versus 5.15 MiB locally, and process RSS is 51.70 versus 51.15 MiB. Allocation
profiles still attribute approximately 99% of sampled allocated bytes to
compression-adapter construction. The serving code remains unchanged.

Because SMB is mounted as a local filesystem path, the CLI/toolkit exercise
their file/archive paths, including file stat/open/read operations. This does
not exercise the toolkit's HTTP/S3/GCS remote-archive cache configuration.
"""

    s += '\n## Go router/cache benchmarks\n\nSynthetic fixtures, same benchmark configurations as the original. Medians of\nfive runs; allocation is B/op churn, not retained memory. No media copying or\nfixture generation occurs inside measured loops.\n\n'
    bs = [benches(DOCS/'baseline-2026-10-29/bench.txt'), benches(OUT/'local-bench.txt'), benches(OUT/'nas-bench.txt')]
    s += '| Benchmark | Original local µs / B | Local control µs / B | HDD NAS µs / B |\n| --- | ---: | ---: | ---: |\n'
    for name in bs[0]:
        s += '| '+name.replace('Benchmark','').removesuffix('-4')+' | '+' | '.join(f'{b[name][0]/1e3:,.3f} / {b[name][1]:,.0f}' for b in bs)+' |\n'

    s += '\n## Process memory\n\nGo heap and RSS are median MiB across five rounds, with GC forced before the\nheap snapshot. Rows are cumulative process state, not per-publication cost.\n"Peak" is the highest sampled lifetime high-water mark, including cache pressure.\n\n'
    for dataset,rs in reports.items():
        s += f'\n### {dataset}\n\n| Phase / metric | Original local | Local control | HDD NAS |\n| --- | ---: | ---: | ---: |\n'
        for phase in [m['phase'] for m in rs[0]['memory'] if m['round']==1 and 'post_gc' in m['phase']]:
            for field,label in [('heap_alloc_bytes','Live heap'),('heap_sys_bytes','Reserved heap'),('linux_rss_bytes','RSS')]:
                s += f'| {phase} · {label} | '+' | '.join(f'{memory(r,phase,field):.2f}' for r in rs)+' |\n'
        s += '| Peak RSS | '+' | '.join(f'{max(m["linux_peak_rss_bytes"] for m in r["memory"])/2**20:.2f}' for r in rs)+' |\n'

    s += '''
## Reproduce and validate

The original [methodology](README.md) remains applicable. In-place mode matters:
`--epub`/`--audio` alone would copy into local temporary storage and would not
measure NAS serving. For synthetic media:

```sh
GOMAXPROCS=4 go run ./tools/servebench --binary /tmp/readium-perf/readium-before --library-dir /mnt/Media/Reader_Testing/synthetic --storage-label 'HDD NAS over SMB 3.1.1' --rounds 5 --requests 100 --concurrency 8 --output nas-synthetic.json
READIUM_BENCH_LIBRARY=/mnt/Media/Reader_Testing/synthetic GOMAXPROCS=4 go test ./pkg/serve -run '^$' -bench '^(BenchmarkServing|BenchmarkPublicationCache)$' -benchmem -count=5 -benchtime=500ms
```

Repeat HTTP with `--library-dir /mnt/Media/Reader_Testing/moby-m4b` and
`--files external-0.epub,comic.cbz,external-2.m4b`. Use the corresponding local
library directories for matched controls. Profile commands from README accept
the same `READIUM_BENCH_LIBRARY` variable. The measured runs used a prebuilt
Go benchmark executable for both storage locations, avoiding builds during
measurement. These paths describe this run; the original HTTP binary must be
rebuilt from the recorded original revisions to reproduce it elsewhere.

Raw artifacts are in [storage-comparison-2026-10-29](storage-comparison-2026-10-29/):
`local-http-{synthetic,moby-m4b}.json`, `nas-http-{synthetic,moby-m4b}.json`,
`{local,nas}-bench.txt`, `{local,nas}-{allocs,cpu}-top.txt`, and profile-run
outputs. `storage-metadata.json` records the mount and run order. The original
baseline artifacts remain unchanged. The NAS folder also retains a
`fixtures.sha256` file so future runs can validate inputs without regenerating
or overwriting them.

Rebuild the charts/report with Python 3 and matplotlib:
`python tools/servebench/compare_storage.py`. This also asserts matching binary,
fixture/publication information and workload structure before comparing results.
Matplotlib is only a reporting dependency; the Go measurement tools need none.

Validation: full CLI tests, targeted race tests for the harness/fixture selector,
and `go vet` passed. Fixture preparation refuses existing directories, in-place
selection rejects traversal/duplicates/missing files, and generated fixture
hashes are locked to the original baseline. Both complete storage suites passed
HTTP length/prefix/duration validation and original-hash checks. Production
sources in Reader, CLI and toolkit remain unchanged.
'''
    (DOCS/'storage-comparison-2026-10-29.md').write_text(s)
    print('Wrote comparison PNG, interactive HTML and complete report; identity checks passed.')


if __name__ == '__main__':
    main()
