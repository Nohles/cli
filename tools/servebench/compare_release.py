"""Rebuild the final release report from immutable, compressed benchmark data."""
from pathlib import Path
from collections import defaultdict
import gzip
import json
import math
import statistics

DOCS = Path(__file__).resolve().parents[2] / "docs/performance"
OUT = DOCS / "release-qualification-2026-10-29"
MEDIA = {"moby-m4b": [("external-0.epub", "EPUB"), ("comic.cbz", "CBZ"), ("external-2.m4b", "M4B")],
         "synthetic": [("book.epub", "EPUB"), ("comic.cbz", "CBZ"), ("audio.wav", "WAV")],
         "directory": [("plain", "100 chapters · plain"), ("selected", "100 chapters · Reader selection")]}


def read(path):
    with gzip.open(path, "rt") as f:
        return json.load(f)


def quantile(values, p=.5):
    return sorted(values)[math.ceil(len(values) * p) - 1]


def main():
    table, payload = {}, {}
    for storage in ["local", "nas"]:
        payload[storage] = {}
        for dataset in MEDIA:
            reports = [[read(p) for p in sorted(OUT.glob(f"[0-9][0-9]-{storage}-{dataset}-{variant}.json.gz"))]
                       for variant in ["before", "after"]]
            assert all(reports)
            assert all(r["fixtures"] == reports[0][0]["fixtures"] for rs in reports for r in rs)
            def shape(report):
                return [(w["media"], w["name"], w["concurrency"], len(w["samples"]),
                         w["summary"]["bytes_per_response"] if "pressure" not in w["name"] else None)
                        for w in report["workloads"] if w["round"] == 1]
            assert all(shape(r) == shape(reports[0][0]) for rs in reports for r in rs), "workload shape changed"
            groups = []
            for rs in reports:
                g = defaultdict(list)
                for report in rs:
                    for work in report["workloads"]:
                        g[work["media"], work["name"]].extend(work["samples"])
                groups.append(g)
            assert set(groups[0]) == set(groups[1])
            rows = []
            for media, label in MEDIA[dataset]:
                for kind in sorted(k for m, k in groups[0] if m == media):
                    series = []
                    for g in groups:
                        samples = g[media, kind]
                        series.append({f"{field}_{p}": quantile([s[field] for s in samples], p/100)
                                       for field in ["complete_ms", "first_byte_ms"] for p in [50, 95]} |
                                      {"count": len(samples)})
                    rows.append({"media": label, "workload": kind, "series": series})
            table[storage, dataset] = (reports, groups, rows)
            payload[storage][dataset] = rows

    # The module pin now reports its actual reviewed revision instead of the
    # stale required version hidden by the previous local replacement.
    manifests = [read(OUT / f"directory-manifest-{v}.json.gz") for v in ["before", "after"]]
    version_key = "https://github.com/nohles/go-toolkit#version"
    versions = [m["metadata"].pop(version_key) for m in manifests]
    assert versions == ["v0.0.0-20260811165309-b89220a022f5", "v0.0.0-20261029104244-99d1e931c3a6"]
    assert manifests[0] == manifests[1], "directory manifest content changed beyond the toolkit build version"

    text = """# Final serving release qualification

This report supersedes the previous combined candidate. The release keeps
compression-adapter reuse, correct uncompressed Range handling, bounded manifest
serialization caching, archive allocation/stream-buffer bounds and synchronized
file-resource tracking. **Cold-open coalescing is excluded** after its full-history
NAS regression reproduced. It can be investigated separately; its earlier burst
improvements are not claimed for this release.

Final production source: CLI `5a974b4d885b0fcdfff84548db661657af482d43`, toolkit
`99d1e931c3a60131ed36de4448c17312cc2826ca`. The CLI now pins the reviewed toolkit
pseudo-version and builds without an absolute/local replacement. The before
binary is CLI `d123c32580915b722464f3a9c5dd8eb42a68e0ee` / toolkit `c070049`.

## NAS audiobook merge gate

The unchanged full workload reproduced the earlier adverse result. A fresh CLI
process alone did not expose it: later rounds after the burst/warm/cache-pressure
phases did. Compression-only stayed fast; using the baseline toolkit with the
current CLI still reproduced it. Removing only cold-open serialization removed
the symptom; removing only manifest caching did not. A diagnostic syscall trace
placed the waiting time in NAS reads, including ZIP tail probes on the raw M4B.
This identifies the triggering CLI change, not a proven SMB/kernel mechanism.
No NAS mount/cache settings or benchmark pauses were changed to obtain a pass.

| Full-history check | Cold M4B complete median, ms |
| --- | ---: |
"""
    for name, label in [("nas-1-before-moby-m4b", "Adverse-run baseline A"), ("nas-2-after-moby-m4b", "Previous candidate B"),
                        ("nas-3-after-moby-m4b", "Previous candidate B repeated"), ("nas-4-before-moby-m4b", "Adverse-run baseline A repeated"),
                        ("01-nas-moby-m4b-before", "Final qualification A"), ("02-nas-moby-m4b-after", "Final release B"),
                        ("03-nas-moby-m4b-after", "Final release B repeated"), ("04-nas-moby-m4b-before", "Final qualification A repeated")]:
        r = read(OUT / (name + ".json.gz"))
        values = [s["complete_ms"] for w in r["workloads"] if w["media"] == "external-2.m4b" and w["name"] == "cold_manifest_serial" for s in w["samples"]]
        text += f"| {label} | {quantile(values):.3f} |\n"
    text += """
Each batch has five fresh-process rounds, 100 requests per warm workload and
eight concurrent clients. Final NAS M4B uses ABBA order (ten cold samples per
variant); other datasets use five rounds per variant. All measured suites ran
sequentially without concurrent builds/tests, Go 1.26.0, GOMAXPROCS=4, debug mode,
identity encoding and persistent connections. Cold means publication/process
caches, with OS/NAS filesystem caches uncontrolled. These are loopback CLI
timings, not click-to-render or physical cold-HDD measurements.

## Current comparison

![Final release comparison](release-qualification-2026-10-29/comparison.png)

[Interactive comparison of every workload](release-qualification-2026-10-29/comparison.html)
includes complete/first-byte p50 and p95, both storage locations, generated and
real-media fixtures, and plain/Reader-selected directory comics.
Percentiles pool individual requests; cold serial estimates have only 5–10
samples. Historical adverse samples remain available beside the final results.
"""
    for (storage, dataset), (reports, groups, rows) in table.items():
        text += f"\n### {storage} · {dataset}\n\n| Media | Workload | Before p50 / p95 ms | Release p50 / p95 ms | Median speedup |\n| --- | --- | ---: | ---: | ---: |\n"
        for row in rows:
            b, a = row["series"]
            text += f'| {row["media"]} | {row["workload"]} | {b["complete_ms_50"]:.3f} / {b["complete_ms_95"]:.3f} | {a["complete_ms_50"]:.3f} / {a["complete_ms_95"]:.3f} | {b["complete_ms_50"]/a["complete_ms_50"]:.2f}× |\n'
    text += "\n## Memory and content checks\n\nPublication cache capacity remains 10, with the existing TTL. The response cache\nretains one URL/indent variant, capped at 128 KiB including key and body per\npublication. Local archive copying uses at most 32 KiB; remote behavior is unchanged.\nMemory below is cumulative per-process state, not per-request allocation or a\nproduction ceiling. Heap rows force GC outside timed requests.\n\n| Storage | After M4B workloads | Before MiB | Release MiB |\n| --- | --- | ---: | ---: |\n"
    for storage in ["local", "nas"]:
        reports = table[storage, "moby-m4b"][0]
        for field, label in [("heap_alloc_bytes", "Live heap after GC"), ("linux_rss_bytes", "Sampled RSS after GC")]:
            values = [statistics.median(m[field] for r in rs for m in r["memory"] if m["phase"] == "external-2.m4b_post_gc")/2**20 for rs in reports]
            text += f"| {storage} | {label} | {values[0]:.2f} | {values[1]:.2f} |\n"
        values = [max(m["linux_peak_rss_bytes"] for r in rs for m in r["memory"])/2**20 for rs in reports]
        text += f"| {storage} | Highest lifetime peak RSS | {values[0]:.2f} | {values[1]:.2f} |\n"
    text += """
Fixture hashes, workload shapes, response lengths, duration/chapter metadata and
Range prefixes remain checked by the harness. Directory plain/selected results
contain 100 ordered chapters, 100 TOC entries and 100 positions; three sampled
chapter archives have 24 pages each. Their normalized manifest hashes differ
only because the corrected module pin changes the toolkit build-version field;
the retained full manifests compare identically after removing exactly that
verified field. Selection digests are unchanged. No chapter/media information
was removed to obtain a speed or memory gain.

Go toolkit full and affected race suites, CLI full and serving race suites pass.
Clean module builds for Linux amd64, Windows amd64 and macOS arm64 succeed;
Windows/macOS were cross-compiled, not runtime-benchmarked. Rebuild this report
with `python3 tools/servebench/compare_release.py` (matplotlib for PNG).
"""
    (DOCS / "release-qualification-2026-10-29.md").write_text(text)
    (OUT / "summary.json").write_text(json.dumps(payload, indent=2) + "\n")
    html = """<!doctype html><html><head><meta charset="utf-8"><style>body{font:14px system-ui;margin:20px;color:var(--foreground,#222)}select{padding:7px;margin:0 8px 10px 0}.row{display:flex;align-items:center;gap:8px;margin:7px 0}.label{width:72px}.track{flex:1;height:15px;background:#8882}.bar{height:100%}.value{width:94px;text-align:right;font:12px monospace}section{margin:20px 0}p{font-size:12px;line-height:1.5}</style></head><body><h3>Readium final release · request latency</h3><select id="storage"><option value="nas">HDD NAS / SMB</option><option>local</option></select><select id="dataset"><option value="moby-m4b">EPUB / CBZ / M4B</option><option value="synthetic">Generated fixtures</option><option value="directory">100-chapter directory</option></select><select id="workload"></select><select id="metric"><option value="complete_ms_50">Complete p50</option><option value="complete_ms_95">Complete p95</option><option value="first_byte_ms_50">First byte p50</option><option value="first_byte_ms_95">First byte p95</option></select><div id="chart"></div><p>Milliseconds; lower is faster. Identical fixture hashes and workload settings. Final NAS M4B: ABBA, ten cold samples per variant; other cases: five. Cold is process/publication cache, with OS/NAS caches uncontrolled. Removed cold-open coalescing is excluded. Loopback CLI measurements exclude Reader and browser rendering.</p><script>const data=PAYLOAD;const ids=['storage','dataset','workload','metric'];const [s,d,w,m]=ids.map(x=>document.getElementById(x));function draw(reset=false){const rows=data[s.value][d.value];if(reset){const old=w.value;w.replaceChildren(...[...new Set(rows.map(r=>r.workload))].map(k=>new Option(k,k)));w.value=[...w.options].some(o=>o.value===old)?old:'warm_manifest'}const selected=rows.filter(r=>r.workload===w.value);const max=Math.max(...selected.flatMap(r=>r.series.map(v=>v[m.value])));document.getElementById('chart').innerHTML=selected.map(r=>'<section><b>'+r.media+'</b>'+r.series.map((v,i)=>'<div class="row"><span class="label">'+['Before','Release'][i]+'</span><div class="track"><div class="bar" style="background:'+['#949aa3','#16866b'][i]+';width:'+v[m.value]/max*100+'%"></div></div><span class="value">'+v[m.value].toFixed(3)+' ms</span></div>').join('')+'</section>').join('')}s.onchange=d.onchange=()=>draw(true);w.onchange=m.onchange=()=>draw();draw(true);</script></body></html>"""
    (OUT / "comparison.html").write_text(html.replace("PAYLOAD", json.dumps(payload)))
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    fig, axes = plt.subplots(2, 2, figsize=(12, 8))
    for ax, (storage, kind) in zip(axes.flat, [(s,k) for s in ["local", "nas"] for k in ["cold_manifest_serial", "warm_manifest"]]):
        rows = [r for r in payload[storage]["moby-m4b"] if r["workload"] == kind]
        for i, label in enumerate(["Before", "Final release"]):
            bars = ax.bar([j+(i-.5)*.35 for j in range(len(rows))], [r["series"][i]["complete_ms_50"] for r in rows], .34, label=label, color=["#949aa3", "#16866b"][i])
            ax.bar_label(bars, fmt="%.2f", padding=3)
        ax.set_xticks(range(len(rows)), [r["media"] for r in rows]); ax.set_ylabel("Complete response p50 · ms")
        ax.set_title(f"{storage.upper()} · {kind.replace('_', ' ')}"); ax.set_ylim(0, ax.get_ylim()[1]*1.2)
        ax.spines[["top", "right"]].set_visible(False)
    axes[0, 0].legend(); fig.suptitle("Final Readium serving qualification · lower is faster")
    fig.tight_layout(); fig.savefig(OUT / "comparison.png", dpi=160); plt.close(fig)


if __name__ == "__main__":
    main()
