"""Regenerate directory-comic qualification from immutable before/after data.
Usage: python tools/servebench/compare_directory.py (matplotlib for PNG export).
"""
from pathlib import Path
import sys
sys.dont_write_bytecode = True
import json
import statistics
from compare_storage import grouped, percentile, memory, benches

DOCS=Path(__file__).resolve().parents[2]/'docs/performance'
OUT=DOCS/'directory-comics-2026-10-29'
WORKLOADS={
 'selection_register':'Registration (opens/caches selected publication)',
 'cold_selected_open_total':'Selected first open: registration + first GET',
 'cold_manifest_serial':'First parent GET (cold only in plain mode)',
 'cold_manifest_burst':'Eight-client first GET burst (post-registration for selected)',
 'warm_manifest':'Warm parent manifest',
 'warm_manifest_concurrent':'Warm parent manifest, concurrency 8',
 'warm_positions':'Parent positions',
 'warm_chapter_archive':'Full middle CBZ through parent',
 'warm_chapter_archive_concurrent':'Full middle CBZ through parent, concurrency 8',
 'warm_chapter_range':'Middle CBZ prefix range',
 'warm_child_manifest':'Individual 24-page chapter manifest',
 'chapter_cache_pressure_open':'Open twelve individual chapter publications',
 'parent_revisit_after_12_chapters':'Revisit parent after twelve chapter opens',
}


def load(storage,variant):return json.loads((OUT/f'{storage}-{variant}.json').read_text())


def main():
 import matplotlib
 matplotlib.use('Agg')
 import matplotlib.pyplot as plt
 import numpy as np
 reports={s:[load(s,v) for v in ['before','after']] for s in ['local','nas']}
 reference=reports['local'][0]
 for pair in reports.values():
  for r in pair:
   assert r['fixtures']==reference['fixtures']
   assert r['directory_validation']==reference['directory_validation']
  signature=lambda r:[(w['round'],w['media'],w['name'],w['concurrency'],len(w['samples']),w['summary']['bytes_per_response'] if 'register' not in w['name'] else None) for w in r['workloads']]
  assert signature(pair[0])==signature(pair[1]),'workload shape or body sizes changed'
 groups={s:[grouped(r) for r in pair] for s,pair in reports.items()}
 fig,axes=plt.subplots(2,3,figsize=(14,8.5));colors=['#9298a1','#16866b']
 for row,storage in enumerate(['local','nas']):
  bg,ag=groups[storage]
  for col,title in [(0,'Warm parent manifest · p50'),(1,'First open · p50, registration included')]:
   ax=axes[row,col]
   for i,(g,label) in enumerate([(bg,'Before'),(ag,'Current')]):
    vals=[percentile(g[mode,'warm_manifest' if col==0 else ('cold_manifest_serial' if mode=='plain' else 'cold_selected_open_total')],'complete_ms',.5) for mode in ['plain','selected']]
    bars=ax.bar(np.arange(2)+(i-.5)*.34,vals,.32,label=label,color=colors[i]);ax.bar_label(bars,fmt='%.3f',padding=3,fontsize=9)
   ax.set_xticks(np.arange(2),['Plain folder','Reader-selected']);ax.set_ylabel('Milliseconds · lower is faster');ax.set_ylim(0,ax.get_ylim()[1]*1.2)
   ax.set_title(f'{"Local" if storage=="local" else "HDD NAS / SMB"} · {title}',loc='left',weight='bold',fontsize=10)
  ax=axes[row,2]
  for i,r in enumerate(reports[storage]):
   vals=[max(m['linux_peak_rss_bytes'] for m in r['memory'] if m['phase'].startswith(mode+'_'))/2**20 for mode in ['plain','selected']]
   bars=ax.bar(np.arange(2)+(i-.5)*.34,vals,.32,color=colors[i]);ax.bar_label(bars,fmt='%.2f',padding=3,fontsize=9)
  ax.set_xticks(np.arange(2),['Plain folder','Reader-selected']);ax.set_ylabel('MiB · per CLI process');ax.set_ylim(0,ax.get_ylim()[1]*1.2)
  ax.set_title(f'{"Local" if storage=="local" else "HDD NAS / SMB"} · peak process RSS',loc='left',weight='bold',fontsize=10)
 for ax in axes.flat:
  ax.spines[['top','right']].set_visible(False);ax.grid(axis='y',alpha=.18);ax.set_axisbelow(True)
 fig.suptitle('Directory comics · 100 CBZ chapters × 24 pages',fontsize=16,weight='bold',x=.04,ha='left')
 handles,labels=axes[0,0].get_legend_handles_labels();fig.legend(handles,labels,ncol=2,loc='upper left',bbox_to_anchor=(.035,.945),frameon=False)
 fig.text(.04,.025,'Exact same 100 distinct archives · 2,400 total image pages · 5 rounds · 100 warm requests per workload\nParent manifest enumerates chapters; it does not flatten image pages. Selected first open includes registration, which parses/caches the parent. OS/NAS caches remain enabled.',fontsize=9,color='#555555')
 fig.subplots_adjust(top=.86,bottom=.13,left=.065,right=.99,hspace=.4,wspace=.28)
 fig.savefig(OUT/'directory-comparison.png',dpi=170,facecolor='white');plt.close(fig)

 text='''# Directory comics: 100 CBZ chapters — 2026-10-29

This extends the existing [optimization results](optimization-2026-10-29.md)
with a directory-of-CBZ publication. **No production serving/cache code was
changed for this test.** It uses the exact saved before/after executables:

- Before: CLI `d123c32580915b722464f3a9c5dd8eb42a68e0ee`, toolkit `c070049715c50c756fc29f8349c6af96324e266d`.
- After: CLI `989c2f1b3d01cc2fc06ba697373e2887f0993ca8`, toolkit `99d1e931c3a60131ed36de4448c17312cc2826ca`.

![Directory-comic comparison](directory-comics-2026-10-29/directory-comparison.png)

[Interactive directory comparison](directory-comics-2026-10-29/directory-comparison.html)
includes all parent/chapter workloads and first-byte/complete p50/p95 controls.

## Fixture, protocol and identity checks

`Series/` contains **100 distinct CBZ files, 24 pages each (2,400 pages total)**,
named `Chapter 001.cbz` through `Chapter 100.cbz`. Each contains a unique
ComicInfo.xml and valid 256×384 PNG pages. Total archive size is 34,540,298 bytes.
All file SHA-256s match between local storage, NAS and both executable runs.
The complete normalized parent manifest matches, not just its chapter count.

The parent manifest enumerates CBZ chapter resources. It is **not a flattened
2,400-page manifest**. Both plain and selected variants return exactly 100
reading-order links, 100 TOC links and 100 positions, in chapter order. The test
also verifies full archive hashes and prefix-range bytes for chapters 1/50/100,
and opens chapter 50 separately to verify its 24 pages and dimensions.

Two cases run in separate fresh CLI processes:

- **Plain folder:** direct manifest request for `Series`.
- **Reader-style selection:** register all 100 ordered HREFs with the private
  directory-selection v1 API, then open the parent using the opaque handle.
  The returned acknowledgement/digest is checked. Both cases must expose the
  same full parent manifest; no chapters or metadata are removed for speed.

**Registration itself opens and caches the selected publication.** Consequently,
its first GET and first eight-client GET burst are cache hits, not cold parsing.
The correct selected first-open comparison is **registration + first GET**;
registration and GET are also reported separately. CLI startup is excluded.

The saved fixtures remain at `/tmp/readium-perf/directory-comics` locally and
`/mnt/Media/Reader_Testing/directory-comics` on the HDD-backed SMB share. SHA-256
lists are retained as `fixtures.sha256`, outside `Series` so they cannot alter
the directory publication. Metadata/run order is in `metadata.json`.

## HTTP results

Pooled nearest-rank **p50 / p95 milliseconds**, full-body completion. Five rounds,
100 requests per warm workload per round, concurrency eight where indicated,
Go 1.26.0, GOMAXPROCS=4, GOMEMLIMIT unset, identity encoding, debug mode on.
First opens/revisits have only five samples, bursts 40, warm rows 500, and
chapter-pressure opens 60. All suites ran sequentially, before then after on
local storage, then before/after on NAS. These are application-cache tests;
OS and NAS caches are not flushed and source hashing warms filesystem data.
Reader proxy/DB, remote user network and image rendering are excluded.

For cache pressure the test opens twelve existing chapter publications, then
revisits the parent: this models chapter navigation without copying the whole
series twelve times. Cache capacity stays ten. Hit/admission behavior is policy-
dependent, so revisits are not assumed to miss. Cold single-source timing on
NAS is variable, and differences include cache/history effects; these five-
sample rows do not isolate a particular optimization or physical HDD speed.
'''
 for storage in ['local','nas']:
  bg,ag=groups[storage]
  for mode in ['plain','selected']:
   text+=f'\n### {storage} · {mode}\n\n| Workload | Before p50 / p95 | Current p50 / p95 | Median speedup |\n| --- | ---: | ---: | ---: |\n'
   for name,label in WORKLOADS.items():
    if (mode,name) not in bg:continue
    b=[percentile(bg[mode,name],'complete_ms',p) for p in [.5,.95]];a=[percentile(ag[mode,name],'complete_ms',p) for p in [.5,.95]]
    text+=f'| {label} | {b[0]:.3f} / {b[1]:.3f} | {a[0]:.3f} / {a[1]:.3f} | {b[0]/a[0]:.2f}× |\n'
 text+='''
## Process memory

Median MiB after the warm parent/positions/archive/child workloads and forced GC;
peak is maximum lifetime RSS through cache pressure. Plain and selected run in
separate processes. These figures exclude NAS server RAM and client kernel caches.

| Storage / mode | Metric | Before | Current |
| --- | --- | ---: | ---: |
'''
 for storage,pair in reports.items():
  for mode in ['plain','selected']:
   for field,label in [('heap_alloc_bytes','Live heap'),('linux_rss_bytes','Sampled RSS')]:
    vals=[memory(r,mode+'_workload_post_gc',field) for r in pair]
    text+=f'| {storage} / {mode} | {label} | {vals[0]:.2f} | {vals[1]:.2f} |\n'
   vals=[max(m['linux_peak_rss_bytes'] for m in r['memory'] if m['phase'].startswith(mode+'_'))/2**20 for r in pair]
   text+=f'| {storage} / {mode} | Peak RSS | {vals[0]:.2f} | {vals[1]:.2f} |\n'
 text+='''
Reader-selected live heap is slightly higher (NAS **3.36 → 3.51 MiB**), while
peak RSS falls **75.04 → 48.61 MiB**. The bounded response cache retains reusable
JSON; lower allocation churn does not imply lower live heap at every snapshot.
This tradeoff is preserved rather than hidden. The existing response cap remains
128 KiB per publication and publication capacity remains ten.

## Go router benchmarks and allocation

Median of five 500 ms Go benchmark runs using these same in-place files. Warm
selected benchmarks register outside the timed loop. `cold_open_close` evicts
the parent key before every GET, so it deliberately measures reparsing plus
close/serialization; it is different from the post-registration first GET above.
The baseline executable was compiled from exact `git archive` snapshots of the
recorded baseline CLI/toolkit, adding only this benchmark and fixture helpers.
Its module replacement points at the matching archived toolkit, not the current
checkout. The after benchmark uses the current optimized source. B/op is churn,
not live memory. No production sources were patched to obtain the baseline.
'''
 for storage in ['local','nas']:
  b=benches(OUT/f'{storage}-before-bench.txt');a=benches(OUT/f'{storage}-after-bench.txt')
  text+=f'\n### {storage}\n\n| Benchmark | Before µs / B/op | Current µs / B/op | Median speedup |\n| --- | ---: | ---: | ---: |\n'
  for name,x in b.items():
   y=a[name];text+=f'| {name.removeprefix("Benchmark").removesuffix("-4")} | {x[0]/1000:,.3f} / {x[1]:,.0f} | {y[0]/1000:,.3f} / {y[1]:,.0f} | {x[0]/y[0]:.2f}× |\n'
 text+='''
## Reproduce

Build the measurement harness from this branch; use the recorded before/after
production source pairs for the measured CLI executables. Do not regenerate
fixtures between before and after runs.

```sh
go build -o servebench ./tools/servebench
./servebench --directory-comics --prepare-only --library-dir /path/new-directory-library
# Copy the exact fixture to the target storage once; verify all archive hashes.
GOMAXPROCS=4 ./servebench --directory-comics --binary /path/before --directory-toolkit-revision c070049715c50c756fc29f8349c6af96324e266d --library-dir /mnt/Media/Reader_Testing/directory-comics --rounds 5 --requests 100 --concurrency 8 --output /path/new-before.json
GOMAXPROCS=4 ./servebench --directory-comics --binary /path/after --directory-toolkit-revision 99d1e931c3a60131ed36de4448c17312cc2826ca --library-dir /mnt/Media/Reader_Testing/directory-comics --rounds 5 --requests 100 --concurrency 8 --output /path/new-after.json
READIUM_DIRECTORY_LIBRARY=/mnt/Media/Reader_Testing/directory-comics GOMAXPROCS=4 go test ./pkg/serve -run '^$' -bench '^BenchmarkDirectoryComics$' -benchmem -count=5 -benchtime=500ms
```

For the local comparison change only `--library-dir` (or the benchmark env var).
Preparation refuses an existing directory and measurements refuse existing output
files. Media are served in place and never copied for timing. Metadata includes
binary SHA/build information, the explicitly declared toolkit revision, all 100
file hashes, complete validated manifest hash, selection digest and sample counts.
The declared toolkit revision describes the measured binary, not necessarily
the harness's current checkout. Existing baseline/result files are unchanged.

Regenerate this report/PNG with `python tools/servebench/compare_directory.py`
(matplotlib for export). The main optimization chart also includes plain and
selected 100-CBZ directory datasets and their distinct registration/positions/
child-manifest workloads. Raw results and Go benchmark output live in
`directory-comics-2026-10-29/`.

Validation: the complete CLI suite and harness/fixture race suites pass. Fixture
tests verify all 100 distinct archives and 24-page structure and reject overwrite.
Every before/after/local/NAS run checks exact input hashes, complete normalized
parent manifest, order/TOC/positions, selection acknowledgement, sampled archive
bytes/ranges and child dimensions. The pre-existing NAS cold single-audiobook
review item remains in the main report; this directory test does not clear it.
'''
 chart=DOCS/'optimization-2026-10-29/optimization-comparison.html'
 if chart.exists():
  html=chart.read_text().replace('<option value="directory-selected">','<option value="directory-selected" selected>')
  (OUT/'directory-comparison.html').write_text(html)
 (DOCS/'directory-comics-2026-10-29.md').write_text(text)
 print('Directory report/chart generated; all manifest, fixture and workload checks passed.')

if __name__=='__main__':main()
