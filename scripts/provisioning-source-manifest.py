#!/usr/bin/env python3
from __future__ import annotations
import argparse,gzip,hashlib,io,json,pathlib,re,tarfile
ROOT=pathlib.Path(__file__).resolve().parents[1]
FILES=(
'provisioning/VERSION','provisioning/go.mod','provisioning/main.go','provisioning/main_test.go','provisioning/Dockerfile','provisioning/docker-compose.example.yml','provisioning/.dockerignore','provisioning/.gitignore','provisioning/web/index.html','scripts/build-provisioning.sh','scripts/provisioning-source-manifest.py','docs/userspace/PROVISIONING.md')
def sha(b): return hashlib.sha256(b).hexdigest()
def collect():
 out={}
 for n in FILES:
  p=ROOT/n
  if not p.is_file() or p.is_symlink(): raise ValueError('missing source '+n)
  b=p.read_bytes(); b.decode('utf-8')
  if re.search(rb'-----BEGIN (?:OPENSSH |RSA |EC )?PRIVATE KEY-----',b): raise ValueError('private key in '+n)
  out[n]=b
 return out
def manifest(fs): return ''.join(f'{sha(b)}  {n}\n' for n,b in sorted(fs.items())).encode()
def archive(path,fs):
 with path.open('wb') as raw, gzip.GzipFile(filename='',mode='wb',fileobj=raw,mtime=0) as gz:
  with tarfile.open(fileobj=gz,mode='w',format=tarfile.USTAR_FORMAT) as t:
   for n,b in sorted(fs.items()):
    e=tarfile.TarInfo(n);e.size=len(b);e.mtime=e.uid=e.gid=0;e.uname=e.gname='root';e.mode=0o755 if n.endswith('.sh') or n.endswith('.py') else 0o644;t.addfile(e,io.BytesIO(b))
def main():
 ap=argparse.ArgumentParser();ap.add_argument('--id',action='store_true');ap.add_argument('--freeze',action='store_true');a=ap.parse_args();fs=collect();sums=manifest(fs);sid=sha(sums)
 if a.id: print(sid);return
 if not a.freeze: ap.error('select --id or --freeze')
 v=(ROOT/'provisioning/VERSION').read_text().strip();out=ROOT/'dist'/('provisioning-'+v);out.mkdir(parents=True,exist_ok=True);name=f'MPX-Provisioning-{v}-source.tar.gz';archive(out/name,dict(fs,PROVISIONING_SOURCE_ID=(sid+'\n').encode(),PROVISIONING_SOURCE_SHA256SUMS=sums));(out/'PROVISIONING_SOURCE_ID').write_text(sid+'\n');(out/'PROVISIONING_SOURCE_SHA256SUMS').write_bytes(sums);print(json.dumps({'version':v,'source_id':sid,'archive':name,'archive_sha256':sha((out/name).read_bytes())},indent=2))
if __name__=='__main__': main()
