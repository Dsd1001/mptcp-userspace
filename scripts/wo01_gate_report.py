#!/usr/bin/env python3
"""Strict per-batch WO-01 report; all FAIL rounds retained. Usage: script.py <batch_dir>"""
import json
import math
import sys
from pathlib import Path
from statistics import median

def med(rows,key):
    vs=[float(r[key]) for r in rows if isinstance(r.get(key),(int,float)) and math.isfinite(float(r[key]))]
    return median(vs) if vs else None
def show(x):
    return "n/a" if x is None else f"{x:.1f}"
def report(path):
    meta=json.loads((path/"meta.json").read_text())
    rows=json.loads((path/"all-rounds.json").read_text())
    all_fail=sum(not r.get("integrity_pass",False) for r in rows)
    summary={"meta":meta,"total_runs":len(rows),"total_fails":all_fail,"by_scenario":{},"overall_pass":False}
    out=["# WO-01 本机 A/B 分阶段性能闸门报告","","只评价此批本地回环实验；不代表公网或生产验收。","",
         "编译器："+meta.get("go","unknown"),
         "共享夹具 SHA256："+meta.get("fixture_sha256","unknown"),
         f"每阶段计划轮数：{meta['rounds']}；记录进程：{len(rows)}；失败轮：{all_fail}。","",
         "## 原始源码版本"]
    for stage,sha in meta["refs"].items():out.append("- "+stage+"："+sha)
    out+=["","## 分场景结果"]
    gates=[]
    for scenario in meta["scenarios"]:
        by={stage:[r for r in rows if r.get("stage")==stage and r.get("scenario")==scenario] for stage in meta["stages"]}
        legacy=by.get("legacy",[])
        base_rate,base_p95,base_p99=[med(legacy,k) for k in ("mbps","probe_p95_ms","probe_p99_ms")]
        base_ttfb_p95,base_ttfb_p99=[med(legacy,k) for k in ("probe_ttfb_p95_ms","probe_ttfb_p99_ms")]
        out += ["","### "+scenario,"","| 组 | 轮数 | 失败 | 吞吐中位/最低 Mbps | P95中位/最高 ms | P99中位 ms | MPX重传总数 | B1升速帧中位数 | 闸门 |","|---|---:|---:|---:|---:|---:|---:|---:|---|"]
        groups={}
        for stage, rr in by.items():
            rate,p95,p99=[med(rr,k) for k in ("mbps","probe_p95_ms","probe_p99_ms")]
            ttfb_p95,ttfb_p99,open_p95,write_p95,wait_first_p95=[
                med(rr,k) for k in ("probe_ttfb_p95_ms","probe_ttfb_p99_ms","probe_open_p95_ms",
                                     "probe_write_p95_ms","probe_wait_first_p95_ms")]
            rate_min=min((r["mbps"] for r in rr if isinstance(r.get("mbps"),(float,int))),default=None)
            p95_max=max((r["probe_p95_ms"] for r in rr if isinstance(r.get("probe_p95_ms"),(float,int))),default=None)
            failed=sum(not r.get("integrity_pass",False) for r in rr)
            full=len(rr)==meta["rounds"] and failed==0
            if stage=="legacy":gate=full
            else:
                old_completion_ok=bool(full and base_rate and base_p95 and base_p99 and rate is not None and p95 is not None and p99 is not None and
                                       rate>=0.95*base_rate and p95<=1.10*base_p95 and p99<=1.10*base_p99)
                true_ttfb_ok=bool((base_ttfb_p95 is None and ttfb_p95 is None) or
                                  (base_ttfb_p95 and base_ttfb_p99 and ttfb_p95 is not None and ttfb_p99 is not None and
                                   ttfb_p95<=1.10*base_ttfb_p95 and ttfb_p99<=1.10*base_ttfb_p99))
                gate=old_completion_ok and true_ttfb_ok
                gates.append(gate)
            sent=sum(int(r.get("mpx_retransmits",0)) for r in rr)
            fast=med(rr,"first_data_priority_frames")
            groups[stage]={
                "source_sha":meta["refs"][stage],"rounds":len(rr),"failures":failed,
                "median_mbps":rate,"minimum_mbps":rate_min,"median_p95_ms":p95,
                "max_p95_ms":p95_max,"median_p99_ms":p99,"sum_retransmits":sent,
                "median_first_data_fast_frames":fast,
                "median_true_ttfb_p95_ms":ttfb_p95,"median_true_ttfb_p99_ms":ttfb_p99,
                "median_open_p95_ms":open_p95,"median_write_p95_ms":write_p95,
                "median_wait_first_byte_p95_ms":wait_first_p95,
                "max_writer_stall_ms":max([float(r.get("writer_stall_max_ms",0)) for r in rr],default=0),
                "sum_path_errors":sum(sum(int(v) for v in r.get("path_errors",[])) for r in rr),
                "median_cpu_seconds":med(rr,"os_process_cpu_seconds"),
                "median_heap_peak_bytes":med(rr,"peak_heap_bytes"),
                "median_tail_t99_to_t100_seconds":med(rr,"tail_t99_to_100_seconds"),
                "gate_pass":gate,
                "individual_rounds":[{
                    "round":r.get("round"),"exit_code":r.get("exit_code"),
                    "integrity_pass":r.get("integrity_pass"),"mbps":r.get("mbps"),
                    "p95_ms":r.get("probe_p95_ms"),"p99_ms":r.get("probe_p99_ms"),
                    "true_ttfb_p95_ms":r.get("probe_ttfb_p95_ms"),
                    "true_ttfb_p99_ms":r.get("probe_ttfb_p99_ms"),
                    "open_p95_ms":r.get("probe_open_p95_ms"),
                    "write_p95_ms":r.get("probe_write_p95_ms"),
                    "wait_first_p95_ms":r.get("probe_wait_first_p95_ms"),
                    "first_data_priority_frames":r.get("first_data_priority_frames"),
                    "client_first_data_priority_frames":r.get("client_first_data_priority_frames"),
                    "peak_heap_bytes":r.get("peak_heap_bytes"),
                    "cpu_seconds":r.get("os_process_cpu_seconds"),
                    "path_errors":r.get("path_errors"),
                    "tail_t99_to_t100_s":r.get("tail_t99_to_100_seconds"),
                    "writer_stall_ms":r.get("writer_stall_max_ms"),
                    "retransmits":r.get("mpx_retransmits"),"log":r.get("log")
                } for r in rr],
            }
            out.append(f"| {stage} | {len(rr)}/{meta['rounds']} | {failed} | {show(rate)}/{show(rate_min)} | {show(p95)}/{show(p95_max)} | {show(p99)} | {sent} | {show(fast)} | {'PASS' if gate else 'FAIL'} |")
        out += ["", "首字节诊断：", "",
                "| 组 | 真正TTFB P95/P99中位(ms) | OPEN P95(ms) | Write P95(ms) | 等待首字节 P95(ms) | 最长Socket Write阻塞(ms) | 路径错误累计 | CPU秒中位 | Heap峰值MiB中位 | t99→t100秒中位 |",
                "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|"]
        for k,z in groups.items():
            heap=(z["median_heap_peak_bytes"] / 1048576) if z["median_heap_peak_bytes"] is not None else None
            out.append(f"| {k} | {show(z['median_true_ttfb_p95_ms'])}/{show(z['median_true_ttfb_p99_ms'])} | {show(z['median_open_p95_ms'])} | {show(z['median_write_p95_ms'])} | {show(z['median_wait_first_byte_p95_ms'])} | {show(z['max_writer_stall_ms'])} | {z['sum_path_errors']} | {show(z['median_cpu_seconds'])} | {show(heap)} | {show(z['median_tail_t99_to_t100_seconds'])} |")
        summary["by_scenario"][scenario]=groups
    mandatory=set(["hetero12","uniform12","single","synch150","stagger150","high100","high300","onecarrier","eightcarriers"])
    summary["overall_pass"]=bool(gates and all(gates) and all_fail==0 and mandatory.issubset(set(meta["scenarios"])))
    out+=["","## 判定与统计限制","",
          "单阶段全部原始轮次无失败，且相对 Legacy 吞吐中位不少于 95%、32KiB完成 P95/P99 与真实首字节 TTFB P95/P99 中位均不超过 110%，才算此场景 PASS。",
          "本夹具每轮仅 12 个短探针，按最近秩计算 P95 与 P99 通常等于最大样本值；不能将其误认为大样本概率保证。",
          "CPU秒指 Python Runner 用操作系统 RUSAGE_CHILDREN 统计本轮 Go 测试进程 user+system CPU；旧轮次缺失此字段显示 n/a。Go /cpu/classes/total 包含闲置 CPU 容量，严禁当作实际进程 CPU。",
          "控制帧专项、丢包、150 Stream 和高 BDP 未全部通过前，不接受整体放行。","",
          "**NO-GO**" if not summary["overall_pass"] else "本机全矩阵闸门暂 PASS（仍不能自行部署/发版）。",""]
    (path/"gate-report.md").write_text("\n".join(out))
    (path/"gate-report.json").write_text(json.dumps(summary,indent=2,ensure_ascii=False)+"\n")
    print("REPORT",path,"RUNS",len(rows),"FAIL",all_fail,"OVERALL",summary["overall_pass"])
    for scene,states in summary["by_scenario"].items():
        print(scene,[(k,z["gate_pass"],show(z["median_mbps"]),show(z["median_p95_ms"])) for k,z in states.items()])
if __name__=="__main__":
    if len(sys.argv)!=2:raise SystemExit("usage: wo01_gate_report.py BATCH_DIR")
    report(Path(sys.argv[1]))
