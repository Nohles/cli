"""Rebuild before/after results from preserved fixtures and raw measurements.
Run with Python 3 + matplotlib: python tools/servebench/compare_optimization.py
"""
from pathlib import Path
import json
import statistics
import sys
sys.dont_write_bytecode = True
from compare_storage import grouped, percentile, memory, benches, MEDIA, WORKLOADS

DOCS = Path(__file__).resolve().parents[2] / 'docs/performance'
OUT = DOCS / 'optimization-2026-10-29'
BASE = DOCS / 'storage-comparison-2026-10-29'


def load(storage, dataset, current=True):
    return json.loads(((OUT / f'current-{storage}-http-{dataset}.json') if current else (BASE / f'{storage}-http-{dataset}.json')).read_text())


def main():
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    import numpy as np

    table = {}
    for storage in ['local', 'nas']:
        for dataset in MEDIA:
            before, after = load(storage, dataset, False), load(storage, dataset)
            assert before['fixtures'] == after['fixtures'], 'publication or fixture information changed'
            sig = lambda r: [(w['round'], w['media'], w['name'], w['concurrency'], len(w['samples']), w['summary']['bytes_per_response'] if w['name'] != 'cache_pressure_open' else None) for w in r['workloads']]
            assert sig(before) == sig(after), 'workload structure/body lengths changed'
            table[storage, dataset] = (before, after, grouped(before), grouped(after))

    colors = ['#9298a1', '#2874be', '#16866b']
    fig, axes = plt.subplots(2, 3, figsize=(15, 9))
    for row, storage in enumerate(['local', 'nas']):
        before, after, bg, ag = table[storage, 'moby-m4b']
        step = json.loads((OUT / f'step1-{storage}-http-moby-m4b.json').read_text())
        sg = grouped(step)
        for col, (kind, title) in enumerate([('warm_manifest', 'Warm manifest · complete p50'), ('cold_manifest_burst', 'First manifest · eight-client burst p50')]):
            ax = axes[row, col]
            for i, (g, label) in enumerate([(bg, 'Before'), (sg, 'Compression setup only'), (ag, 'Current combined')]):
                vals = [percentile(g[name, kind], 'complete_ms', .5) for name, _ in MEDIA['moby-m4b']]
                bars = ax.bar(np.arange(3)+(i-1)*.25, vals, .24, color=colors[i], label=label)
                ax.bar_label(bars, fmt='%.2f', padding=3, fontsize=8)
            ax.set_xticks(np.arange(3), ['EPUB', 'CBZ', 'M4B'])
            ax.set_ylabel('Milliseconds · lower is faster')
            ax.set_title(f'{"Local" if storage=="local" else "HDD NAS / SMB"} · {title}', loc='left', weight='bold', fontsize=10)
            ax.set_ylim(0, ax.get_ylim()[1]*1.18)
        ax = axes[row, 2]
        for i, r in enumerate([before, step, after]):
            vals = [memory(r, 'external-2.m4b_post_gc', 'heap_alloc_bytes'), memory(r, 'external-2.m4b_post_gc', 'linux_rss_bytes'), max(m['linux_peak_rss_bytes'] for m in r['memory'])/2**20]
            bars = ax.bar(np.arange(3)+(i-1)*.25, vals, .24, color=colors[i])
            ax.bar_label(bars, fmt='%.2f', padding=3, fontsize=8)
        ax.set_xticks(np.arange(3), ['Live heap', 'Sampled RSS', 'Lifetime peak RSS'])
        ax.set_title(f'{"Local" if storage=="local" else "HDD NAS / SMB"} · memory', loc='left', weight='bold', fontsize=10)
        ax.set_ylabel('MiB · per CLI process')
        ax.set_ylim(0, ax.get_ylim()[1]*1.15)
    for ax in axes.flat:
        ax.spines[['top', 'right']].set_visible(False)
        ax.grid(axis='y', alpha=.18)
        ax.set_axisbelow(True)
    fig.suptitle('Readium CLI · speed and memory before/after', fontsize=16, weight='bold', x=.04, ha='left')
    handles, labels = axes[0, 0].get_legend_handles_labels()
    fig.legend(handles, labels, ncol=3, loc='upper left', bbox_to_anchor=(.035, .945), frameon=False)
    fig.text(.04, .025, 'Same exact Moby-Dick EPUB / 240-page CBZ / 12-chapter M4B · 5 rounds · 100 warm requests per workload\nHeap/RSS medians follow the media workloads; peak covers full lifetime. Filesystem caches stay enabled. Cold serial NAS M4B variability is retained and flagged in the report.', fontsize=9, color='#555555')
    fig.subplots_adjust(top=.86, bottom=.13, left=.055, right=.99, hspace=.4, wspace=.28)
    fig.savefig(OUT/'optimization-comparison.png', dpi=170, facecolor='white')
    plt.close(fig)

    payload = {}
    for (storage, dataset), (_, _, bg, ag) in table.items():
        payload.setdefault(storage, {})[dataset] = {}
        for kind in WORKLOADS:
            payload[storage][dataset][kind] = [dict(label=label, series=[{metric+'_'+str(int(p*100)): percentile(g[name, kind], field, p) for metric, field in [('complete','complete_ms'),('first','first_byte_ms')] for p in [.5,.95]} for g in [bg,ag]]) for name, label in MEDIA[dataset]]
    html = '''<!doctype html><html><head><meta charset="utf-8"><style>:root{font-family:var(--font-sans,system-ui)}body{margin:0;color:var(--foreground,#222)}.controls{display:flex;flex-wrap:wrap;gap:8px;margin:10px 0 20px}label{font-size:12px}select{display:block;padding:6px;margin-top:4px;background:var(--secondary,#eee);color:var(--foreground,#222);border:1px solid var(--border,#ccc);border-radius:6px}.group{margin:18px 0}.name{font-size:14px;font-weight:600;margin-bottom:7px}.row{display:flex;align-items:center;gap:8px;height:25px;font-size:12px}.row>span{width:65px;color:var(--muted-foreground,#666)}.track{flex:1;background:var(--secondary,#eee);height:12px;border-radius:3px}.bar{height:12px;border-radius:3px}.v{width:60px;text-align:right;font-family:var(--font-mono,monospace);font-size:12px}.s0{background:var(--chart-3,#9298a1)}.s1{background:var(--chart-2,#16866b)}p{font-size:12px;line-height:1.5;color:var(--muted-foreground,#666)}.note{border-top:1px solid var(--border,#ccc);padding-top:12px}</style></head><body><div class="controls"><label>Storage<select id="storage"><option value="nas">HDD NAS · SMB</option><option value="local">Local filesystem</option></select></label><label>Dataset<select id="dataset"><option value="moby-m4b">Moby-Dick / CBZ / M4B</option><option value="synthetic">Synthetic EPUB / CBZ / WAV</option></select></label><label>Workload<select id="workload">'''
    html += ''.join(f'<option value="{k}"'+(' selected' if k=='warm_manifest' else '')+f'>{v}</option>' for k,v in WORKLOADS.items())
    html += '''</select></label><label>Latency<select id="metric"><option value="complete_50">Complete · p50</option><option value="complete_95">Complete · p95</option><option value="first_50">First byte · p50</option><option value="first_95">First byte · p95</option></select></label></div><div id="chart" role="img" aria-label="Before and after response latency"></div><p class="note">Milliseconds, lower is faster. Same binary settings, exact input hashes and workload counts; five rounds, 100 requests per warm workload. Fresh publication caches do not flush filesystem/NAS caches. Reader proxy and remote client network are excluded. NAS cold serial M4B has variable slow samples; see the preserved raw runs and controlled recheck in the PR.</p><script>const data='''+json.dumps(payload,separators=(',',':'))+''';function render(){const s=document.getElementById('storage').value,d=document.getElementById('dataset').value,w=document.getElementById('workload').value,m=document.getElementById('metric').value,rows=data[s][d][w],max=Math.max(...rows.flatMap(r=>r.series.map(v=>v[m])))*1.05;document.getElementById('chart').innerHTML=rows.map(r=>'<div class="group"><div class="name">'+r.label+'</div>'+r.series.map((v,i)=>'<div class="row"><span>'+['Before','Current'][i]+'</span><div class="track"><div class="bar s'+i+'" style="width:'+(100*v[m]/max)+'%"></div></div><div class="v">'+v[m].toFixed(3)+'</div></div>').join('')+'</div>').join('');}for(const e of document.querySelectorAll('select'))e.addEventListener('change',render);render();</script></body></html>'''
    (OUT/'optimization-comparison.html').write_text(html)

    text = '''# Readium serving optimizations — 2026-10-29

Measured sources: CLI `989c2f1b3d01cc2fc06ba697373e2887f0993ca8`, toolkit
`99d1e931c3a60131ed36de4448c17312cc2826ca`. Before uses CLI `d123c32` /
toolkit `c070049` and the matched local/HDD-NAS storage baselines. The original
baseline artifacts and the first combined pass, including slow NAS samples,
remain intact. Reader's source pins are updated in a companion PR.

![Before and after](optimization-2026-10-29/optimization-comparison.png)

[Interactive comparison](optimization-2026-10-29/optimization-comparison.html)
contains every latency workload and both datasets, with first-byte/complete
response and p50/p95 controls.

## Changes and memory limits

1. `b13872c` constructs compression factories once per router instead of once
   per matched request. Compression negotiation remains enabled. A regression
   test also found that offered compression removed Range: ranged publication
   requests now preserve their offsets and remain uncompressed.
2. `9a4bca4` coordinates cold local-file publication opens by fingerprint and
   selection digest, then rechecks the cache. Followers can cancel independently;
   different publications proceed independently. Session URLs keep per-request
   rights behavior. Locks disappear after users leave. Cache rejection/errors
   can still require a later parse; no uncached publication is retained just
   to claim a coalescing hit.
3. `989c2f1` reuses serialized manifest bytes and ETags. At most one self-URL /
   indentation variant per publication is retained, with combined key/body cap
   128 KiB. Across ten entries this adds at most about 1.25 MiB plus object
   overhead; oversized responses are served without retention. Session refresh
   invalidates the response; old in-flight response bytes stay immutable.
4. Toolkit `8319231` clamps archive reads to actual entry length before allocation
   and uses at most 32 KiB for local deflate stream copying. The 2 MiB remote
   copy behavior and stored-file raw fast paths remain. Image-probe budgets,
   dimensions, publication contents and metadata are preserved.
5. Toolkit `99d1e93` synchronizes file-resource tracking and prunes closed/dead
   weak references. The regression test reproduced both request-count growth
   and concurrent append races. Tracking now follows live resources instead
   of growing with all requests during a publication's lifetime. Error paths
   also close handles acquired before an unsuccessful stat/directory check.

Publication capacity remains **10**, with the existing **600-second TTL** and
existing random offset. No larger decoded-media cache or disk-cache policy was
introduced. These measurements cover the local file/archive path (including
mounted SMB), not remote HTTP/S3/GCS archive cache tuning. The previously flagged
remote buffer-ownership/configuration questions remain for separate review.

## Latest HTTP results

Pooled nearest-rank p50/p95 milliseconds; identical fixtures, workload order,
counts and selected-media response lengths (cache-pressure manifest self URLs
may differ in length with the generated scratch namespace). Each warm row has 500 samples,
cold burst 40, serial/revisit 5, cache-pressure opens 60. Speedup is before/current
median for the same workload, never an extrapolation to full user-visible startup.
All suites ran sequentially with Go 1.26.0, GOMAXPROCS=4, GOMEMLIMIT unset, debug
mode on, identity HTTP encoding and persistent connections. Fresh CLI processes
clear publication caches, not OS or NAS caches. Client, server and profiler
settings match the saved baselines.
'''
    for storage in ['local', 'nas']:
        for dataset in MEDIA:
            _,_,bg,ag = table[storage,dataset]
            text += f'\n### {storage} · {dataset}\n\n| Media | Workload | Before p50 / p95 | Current p50 / p95 | Median speedup |\n| --- | --- | ---: | ---: | ---: |\n'
            for name,label in MEDIA[dataset]:
                for kind,title in WORKLOADS.items():
                    b=[percentile(bg[name,kind],'complete_ms',p) for p in [.5,.95]]
                    a=[percentile(ag[name,kind],'complete_ms',p) for p in [.5,.95]]
                    text += f'| {label} | {title} | {b[0]:.3f} / {b[1]:.3f} | {a[0]:.3f} / {a[1]:.3f} | {b[0]/a[0]:.2f}× |\n'
    text += '\n## Process memory\n\nMoby-Dick/CBZ/M4B, median MiB after all media workloads and forced GC. Peak\nis the highest lifetime RSS across rounds, including cache pressure. These\nare per-CLI-process figures, excluding kernel filesystem caches and NAS RAM.\n\n| Storage | Metric | Before | Current | Reduction |\n| --- | --- | ---: | ---: | ---: |\n'
    for storage in ['local','nas']:
        b,a,_,_=table[storage,'moby-m4b']
        for field,label in [('heap_alloc_bytes','Live heap'),('heap_sys_bytes','Reserved heap'),('linux_rss_bytes','Sampled RSS')]:
            x,y=[memory(r,'external-2.m4b_post_gc',field) for r in [b,a]]
            text += f'| {storage} | {label} | {x:.2f} | {y:.2f} | {(1-y/x)*100:.1f}% |\n'
        x,y=[max(m['linux_peak_rss_bytes'] for m in r['memory'])/2**20 for r in [b,a]]
        text += f'| {storage} | Lifetime peak RSS | {x:.2f} | {y:.2f} | {(1-y/x)*100:.1f}% |\n'
    text += '\n## Allocations and toolkit open cost\n\nMedian of five synthetic-fixture Go benchmark runs. These exclude network/client\nallocation. B/op is allocation churn, not retained heap; cache open/close includes\neviction and toolkit parsing. The warm WAV asset benchmark is a 64 KiB range.\n'
    for storage in ['local','nas']:
        before=benches(BASE/f'{storage}-bench.txt');after=benches(OUT/f'current-{storage}-bench.txt')
        text += f'\n### {storage}\n\n| Benchmark | Before µs / B/op | Current µs / B/op | Median speedup |\n| --- | ---: | ---: | ---: |\n'
        for name,x in before.items():
            y=after[name]
            text+=f'| {name.removeprefix("Benchmark").removesuffix("-4")} | {x[0]/1000:,.3f} / {x[1]:,.0f} | {y[0]/1000:,.3f} / {y[1]:,.0f} | {x[0]/y[0]:.2f}× |\n'
    text += '''
## Regressions, variability and human review

**NAS serial cold M4B remains a review item.** The first combined pass measured
15.956 ms median / 127.572 ms maximum across five samples; the current full run
measured 10.406 / 17.564 ms versus baseline 2.712 / 3.866 ms. Both raw runs are
retained rather than discarded. This is the notable slower row; do not claim a
cold single-open improvement for NAS M4B from these full runs.

A controlled recheck alternated before, compression-only and first-combined binaries
(the first-combined toolkit was `8319231`, before the tracking fix)
for 30 fresh-process trials each, opening EPUB, CBZ and M4B in the same order.
M4B before p50/p95/max was 2.445/3.761/15.873 ms; combined was
0.995/1.706/2.060 ms. It did not reproduce an optimized-only regression.
The controlled check excludes the full suite's warm/cache-pressure phases and
therefore does not resolve the full-suite discrepancy. Raw records are in
`cold-nas-diagnostic.json`. A separate instrumented three-round run retained in
`nas-io-diagnostic.json` showed filesystem stalls (including pread64 calls around
125 ms, and mount open/stat delays); the bounded syscall summary is in
`nas-io-summary.txt`. strace timings are diagnostic, not benchmark results.
The exact interaction between workload history, SMB/filesystem behavior and
process timing is not isolated. Human review/qualification of this NAS cold
case is needed before general rollout; changing NAS mount/cache settings is
outside this change.

Other rows, including slower/noisy samples, remain in the tables. Revisit
speedups are policy-dependent cache hits/misses and have only five samples;
use warm requests and larger cold-burst samples for the primary comparison.
No production memory ceiling, remote-cache speedup, Windows/macOS runtime gain,
full Reader-to-client speedup or uncached HDD throughput is inferred.

## Validation and reproduction

- Full CLI suite and serving/cache/harness race suites pass.
- Full toolkit suite plus affected archive/analyzer/image/audio/fetcher race
  suites pass with verified actual Git LFS fixtures. Original pointer files
  were restored; no downloaded fixture contents were committed.
- Regression coverage includes compression negotiation under concurrency,
  compressed-offer Range/HEAD, cold-open sharing/cancellation, bounded response
  variants and session invalidation, extreme/clamped archive ranges, local
  stream chunk sizes, resource-list growth and concurrent Get/Close lifecycles.
- Reader protocol tests pass (three suites, 18 tests).
- Original fixture SHA-256s, reading-order/TOC counts, duration and HTTP response
  length/prefix checks pass on every local/NAS dataset. No dimensions or reading
  order were removed as a performance shortcut.
- Actual CLI cross-builds pass for Windows amd64 and macOS arm64 with CGO disabled.
  Runtime measurements here are Linux/WSL only.

Use the [same baseline methodology](README.md), pointing `--binary` at the rebuilt
current executable and `--library-dir` at the retained local or NAS fixture set.
Use `READIUM_BENCH_LIBRARY` for Go benchmarks/profiles. Compare exact source
revisions and fixture hashes. Rebuild this report and PNG/HTML with
`python tools/servebench/compare_optimization.py` (matplotlib needed for reporting).

The `optimization-2026-10-29/` directory retains compression-only snapshots,
first-combined-pass raw samples, current raw samples, Go benchmarks, profiles,
validation output, and the NAS diagnostic records. The original local/HDD-NAS
baselines are unchanged. The pre-existing local go.mod replacement remains
uncommitted; Reader's reproducible build already supplies the pinned toolkit
checkout explicitly.
'''
    diagnostic = json.loads((OUT/'cold-nas-current.json').read_text())
    summary = []
    import math
    for variant in ['before','after']:
        values = sorted(x['sample']['complete_ms'] for x in diagnostic['records'] if x['variant']==variant and x['media']=='external-2.m4b')
        summary.append(f"{variant}: {values[math.ceil(.5*len(values))-1]:.3f} / {values[math.ceil(.95*len(values))-1]:.3f} / {max(values):.3f} ms")
    paragraph = "A second alternating check uses the **current tracking-fixed binary** for 30\ntrials per variant. M4B p50 / p95 / max: " + "; ".join(summary) + ".\nThe binary hashes and records are retained in `cold-nas-current.json`. This\nalso excludes the full warm/cache-pressure history, so the full-suite cold\ncase remains a human-review item. Reproduce the controlled check with:\n\n```sh\nGOMAXPROCS=4 READIUM_COMPARE_BEFORE=/path/before READIUM_COMPARE_AFTER=/path/current READIUM_COMPARE_LIBRARY=/mnt/Media/Reader_Testing/moby-m4b READIUM_COMPARE_OUTPUT=/path/new-output.json go test ./tools/servebench -run '^TestCompareColdOpens$' -count=1\n```\n\n"
    text = text.replace('Other rows, including slower/noisy samples,', paragraph+'Other rows, including slower/noisy samples,')
    (DOCS/'optimization-2026-10-29.md').write_text(text)
    print('Generated before/after report and charts; fixture/workload checks passed.')


if __name__ == '__main__':
    main()
