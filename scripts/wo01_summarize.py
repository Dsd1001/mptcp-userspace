#!/usr/bin/env python3
"""Independent WO-01 A/B summary; never discards failed rounds.
Group by exact pinned source SHA, fixture SHA, and scenario to prevent
mixing legacy/A/B candidates from different snapshots.
"""
from collections import defaultdict
from pathlib import Path
import argparse,csv,json,statistics,hashlib

def stat(vals):
    if not vals:return None
    vs=sorted(vals)
    return {"median":round(statistics.median(vs),4),
            "min":round(vs[0],4),"max":round(vs[-1],4),
            "p10_observed":round(vs[max(0,int((len(vs)-1)*.10))],4)}
def summarize(folder:Path):
    grouped=defaultdict(list)
    for meta in sorted(folder.glob("ab-*/meta.json")):
        rows=meta.parent/"all-rounds.json"
        if not rows.is_file():continue
        group=json.loads(meta.read_text())
        fixture=group.get("fixture_sha256","MISSING")
        for r in json.loads(rows.read_text()):
            sha=r.get("source_sha","MISSING")
            grouped[(r.get("scenario"),r.get("stage"),sha,fixture)].append({
                **r, "batch":meta.parent.name,
                "loopback_only":group.get("loopback_only"),
                "go":group.get("go"),
            })
    records=[]
    for (scenario,stage,sha,fixture),rows in sorted(grouped.items()):
        success=[r for r in rows if r.get("integrity_pass") is True]
        fails=[r for r in rows if not r.get("integrity_pass")]
        record={
            "scenario":scenario, "stage":stage,"source_sha":sha,
            "fixture_sha":fixture,"rounds":len(rows),"passed":len(success),
            "failed":len(fails),"min_mbps_all_completed_or_partial":stat([r["mbps"] for r in rows if isinstance(r.get("mbps"),(int,float))]),
            "complete_round_mbps":stat([r["mbps"] for r in success if isinstance(r.get("mbps"),(int,float))]),
            "complete_round_p95_ms":stat([r["probe_p95_ms"] for r in success if isinstance(r.get("probe_p95_ms"),(int,float))]),
            "p99_max_ms":max((r.get("probe_p99_ms",0) for r in rows),default=0),
            "max_short_probe_ms":max((r.get("probe_max_ms",0) for r in rows),default=0),
            "mpx_retrans_total":sum(r.get("mpx_retransmits",0) for r in rows),
            "max_socket_write_block_ms":max((r.get("writer_stall_max_ms",0) for r in rows),default=0),
            "max_heap_bytes":max((r.get("peak_heap_bytes",0) for r in rows),default=0),
            "max_cpu_seconds":max((r.get("runtime_cpu_seconds",0) for r in rows),default=0),
            "truncated_bytes":sum(max(0,r.get("expected_bytes",0)-r.get("delivered_bytes",0)) for r in rows),
            "failures":[{"batch":r["batch"],"round":r.get("round"),"exit_code":r.get("exit_code"),
                         "errors":r.get("errors"),"log":r.get("log")} for r in fails],
            "raw_rounds":[{"batch":r["batch"],"round":r.get("round"),"exit_code":r.get("exit_code"),
                "pass":r.get("integrity_pass"),"mbps":r.get("mbps"),"probe_p95_ms":r.get("probe_p95_ms"),
                "probe_p99_ms":r.get("probe_p99_ms"),"rtx":r.get("mpx_retransmits"),
                "max_write_ms":r.get("writer_stall_max_ms")} for r in rows],
        }
        records.append(record)
    return records

def main():
    p=argparse.ArgumentParser()
    p.add_argument("--validation",type=Path,default=Path(__file__).resolve().parents[1]/"validation")
    p.add_argument("--out",type=Path,default=None)
    args=p.parse_args()
    results=summarize(args.validation)
    out=args.out or args.validation/"wo01-all-batches-summary.json"
    out.write_text(json.dumps({"method":"groups by scenario, stage, sourceSHA and fixtureSHA, failures retained",
                               "groups":results},indent=2,ensure_ascii=False)+"\n")
    for row in results:
        mbps=row["complete_round_mbps"]
        print(f'{row["scenario"]:12} {row["stage"]:7} {row["source_sha"][:8]} '
              f'PASS={row["passed"]}/{row["rounds"]} '
              f'median_mbps={mbps["median"] if mbps else "NA"} '
              f'min_mbps_all={row["min_mbps_all_completed_or_partial"]["min"] if row["min_mbps_all_completed_or_partial"] else "NA"} '
              f'max_p99_ms={row["p99_max_ms"]:.1f} '
              f'mpx_rtx={row["mpx_retrans_total"]} '
              f'truncated_bytes={row["truncated_bytes"]}')
    print("wrote",out)
if __name__=="__main__":main()
