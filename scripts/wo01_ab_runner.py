#!/usr/bin/env python3
"""WO-01 isolated loopback A/B replay; validates EVERY test process and JSON.
Usage: python3 scripts/wo01_ab_runner.py --stages legacy,a1 --scenarios hetero12 --rounds 5
Ref defaults: v1.1.5, e9bd85f. Pass --a-ref and --ab-ref after the corresponding commits.
Only 127.0.0.1 endpoints; no SSH, external hosts, deployment or production modifications.
"""
import argparse
import csv
import hashlib
import json
import os
import signal
from pathlib import Path
import re
import subprocess
import sys
import time
from datetime import datetime, timezone

WORKTREE=Path(__file__).resolve().parents[1]
PROJECT=WORKTREE.parents[2]
GO=PROJECT/".scratch/go-ab-toolchain/go/bin/go"
FIXTURE=WORKTREE/"macos/engine/multipath/wo01_ab_test.go"
STAGES=("legacy","a1","a","ab")
def git(*args,check=True):
    return subprocess.run(["git","-C",str(PROJECT),*args],text=True,capture_output=True,check=check)
def run_one(args,stage,worktree,scenario,round_id,out_dir):
    env=os.environ.copy()
    env.update(WO01_RUN="1",WO01_SCENARIO=scenario,WO01_ROUND=str(round_id))
    if args.mib:env["WO01_STREAM_MIB"]=str(args.mib)
    if args.delays:env["WO01_DELAYS"]=args.delays
    if args.rates:env["WO01_RATES"]=args.rates
    if args.go_cache:env["GOCACHE"]=args.go_cache
    command=[str(GO),"test","./multipath","-run","^TestWO01LocalAB$","-count=1","-v","-timeout=125s"]
    started=time.monotonic()
    # Own process group: if go test hangs, kill Go AND the spawned test
    # binary, then preserve a structured FAIL rather than orphaning stress.
    timed_out=False
    with subprocess.Popen(command,cwd=worktree/"macos/engine",env=env,
                          text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,
                          start_new_session=True) as outcome:
        try:
            output,_=outcome.communicate(timeout=145)
        except subprocess.TimeoutExpired:
            timed_out=True
            os.killpg(outcome.pid,signal.SIGKILL)
            output,_=outcome.communicate()
            output += "\nWO01_RUNNER_TIMEOUT 145s; killed test process group\n"
    exit_code=124 if timed_out else outcome.returncode
    path=out_dir/f"{round_id:02d}-{stage}-{scenario}.log"
    path.write_text(output)
    matches=re.findall(r"WO01_RESULT\s+(\{[^\n]+\})",output)
    passed=re.search(r"--- PASS: TestWO01LocalAB\b",output) is not None
    complete=len(matches)==1
    result=json.loads(matches[0]) if complete else {}
    ok=(exit_code==0 and passed and complete and
        result.get("completion_timing_valid") is True and
        result.get("completed_streams")==result.get("streams") and
        result.get("expected_bytes")==result.get("delivered_bytes") and
        result.get("backend_failures")==0 and
        result.get("backend_graceful")==result.get("streams",0)+12 and
        result.get("probe_failures")==0 and not result.get("errors") and
        result.get("100ms_samples",0)>0 and
        result.get("t100_seconds",0)>=result.get("t99_seconds",0))
    row={"stage":stage,"scenario":scenario,"round":round_id,
         "source_sha":git("rev-parse",f"{args.refs[stage]}^{{commit}}").stdout.strip(),
         "elapsed_command_s":round(time.monotonic()-started,3),
         "exit_code":exit_code,"test_pass_marker":passed,
         "json_records":len(matches),"integrity_pass":ok,
         "log":path.name,**result}
    (out_dir/f"{round_id:02d}-{stage}-{scenario}.json").write_text(json.dumps(row,indent=2,ensure_ascii=False)+"\n")
    print(f"{'PASS' if ok else 'FAIL'} {stage:7s} {scenario:12s} round={round_id} "
          f"Mbps={result.get('mbps')} probe_p95={result.get('probe_p95_ms')} "
          f"exit={exit_code} log={path.name}",flush=True)
    return row

def main():
    p=argparse.ArgumentParser()
    p.add_argument("--stages",default="legacy,a1,a,ab")
    p.add_argument("--scenarios",default="hetero12,uniform12,single,synch150,stagger150,high100,high300,onecarrier,eightcarriers")
    p.add_argument("--rounds",type=int,default=5)
    p.add_argument("--cooldown",type=float,default=0.0,help="pause between tests, never during a test")
    p.add_argument("--mib",type=int,default=0)
    p.add_argument("--delays",default="")
    p.add_argument("--rates",default="")
    p.add_argument("--a-ref",default="")
    p.add_argument("--ab-ref",default="")
    p.add_argument("--a1-ref",default="e9bd85f")
    p.add_argument("--go-cache",default="")
    p.add_argument("--out",default="")
    args=p.parse_args()
    args.refs={"legacy":"v1.1.5","a1":args.a1_ref,"a":args.a_ref,"ab":args.ab_ref}
    stage_list=args.stages.split(",")
    if args.cooldown<0 or args.cooldown>60:p.error("invalid cooldown")
    if args.rounds<1 or any(s not in STAGES for s in stage_list) or any(not args.refs.get(s) for s in stage_list):
        p.error("invalid stages/rounds or missing --a-ref/--ab-ref")
    scenarios=args.scenarios.split(",")
    if not GO.is_file() or not FIXTURE.is_file():p.error("Go toolchain or fixture missing")
    # The fixture and the six-carrier relay helper MUST bind loopback.
    source=FIXTURE.read_text()
    relay=(WORKTREE/"macos/engine/multipath/highbdp_test.go").read_text()
    if 'PlainListen(context.Background(), "127.0.0.1:0")' not in relay:
        p.error("loopback-only relay check failed")
    if '"127.0.0.1:0"' not in source or "PlainDial" in source:
        p.error("loopback-only test source check failed")
    directory=(Path(args.out).resolve() if args.out else WORKTREE/"validation"/("ab-"+datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")))
    directory.mkdir(parents=True,exist_ok=False)
    tests={}
    sha=hashlib.sha256(FIXTURE.read_bytes()).hexdigest()
    for stage in stage_list:
        rev=git("rev-parse",f"{args.refs[stage]}^{{commit}}").stdout.strip()
        target=WORKTREE/"validation"/"reference-worktrees"/(stage+"-"+rev[:12]+"-"+sha[:10])
        if target.exists():
            if git("rev-parse","--show-toplevel",check=False).returncode!=0: p.error("unexpected git status")
            actual=subprocess.run(["git","-C",str(target),"rev-parse","HEAD"],capture_output=True,text=True)
            if actual.returncode!=0 or actual.stdout.strip()!=rev:p.error(f"existing reference worktree {target} has other commit")
        else:
            target.parent.mkdir(parents=True,exist_ok=True)
            subprocess.run(["git","-C",str(PROJECT),"worktree","add","--detach",str(target),rev],check=True,stdout=subprocess.DEVNULL)
        test=target/"macos/engine/multipath/wo01_ab_test.go"
        # The exact pinned source reference stays unchanged. Overlay ONLY the
        # frozen shared test fixture in its isolated detached worktree: older
        # A1/A2 commits can themselves contain an earlier tracked fixture.
        # Its hash is recorded in meta, never counted as a production change.
        if not test.exists() or hashlib.sha256(test.read_bytes()).hexdigest()!=sha:
            test.write_bytes(FIXTURE.read_bytes())
        tests[stage]=target
    meta={"stages":stage_list,"scenarios":scenarios,"rounds":args.rounds,
          "go":subprocess.check_output([str(GO),"version"],text=True).strip(),
          "fixture_sha256":sha,
          "refs":{s:git("rev-parse",f"{args.refs[s]}^{{commit}}").stdout.strip() for s in stage_list},
          "date_utc":datetime.now(timezone.utc).isoformat(),
          "loopback_only":True,"cooldown_seconds":args.cooldown,"mib_override":args.mib,"delays_override":args.delays,"rates_override":args.rates}
    (directory/"meta.json").write_text(json.dumps(meta,indent=2,ensure_ascii=False)+"\n")
    all_rows=[]
    try:
        for scenario in scenarios:
            for round_id in range(1,args.rounds+1):
                # Rotation removes systematic first/last-run advantage.
                order=stage_list[(round_id-1)%len(stage_list):]+stage_list[:(round_id-1)%len(stage_list)]
                if round_id%2==0:order=order[::-1]
                for stage in order:
                    if all_rows and args.cooldown>0:time.sleep(args.cooldown)
                    all_rows.append(run_one(args,stage,tests[stage],scenario,round_id,directory))
                    (directory/"all-rounds.json").write_text(json.dumps(all_rows,indent=2,ensure_ascii=False)+"\n")
    except (subprocess.TimeoutExpired,Exception) as e:
        (directory/"RUNNER-ERROR.txt").write_text(repr(e)+"\n")
        raise
    cols=["stage","scenario","round","source_sha","exit_code","integrity_pass","mbps","probe_p95_ms","probe_p99_ms","probe_max_ms","t99_seconds","t100_seconds","tail_t99_to_100_seconds","mpx_retransmits","peak_heap_bytes","runtime_cpu_seconds","writer_stall_max_ms","backend_graceful","backend_failures","log"]
    with (directory/"all-rounds.csv").open("w",newline="") as f:
        wr=csv.DictWriter(f,cols,extrasaction="ignore");wr.writeheader();wr.writerows(all_rows)
    fail=[x for x in all_rows if not x["integrity_pass"]]
    print("SUMMARY runs",len(all_rows),"fail",len(fail),"output",directory,flush=True)
    return int(bool(fail))
if __name__=="__main__":sys.exit(main())
