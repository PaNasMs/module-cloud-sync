import hashlib, json, os, pathlib, shutil, zipfile
root = pathlib.Path(__file__).resolve().parents[1]
m = json.loads((root / 'manifest.json').read_text())
assert os.environ.get('GITHUB_REF_TYPE') != 'tag' or os.environ['GITHUB_REF_NAME'] == 'v'+m['version'], 'Tag must match manifest version'
(root/'dist/backend').mkdir(parents=True,exist_ok=True)
shutil.copy2(root/'tools/authorize.go',root/'dist/backend/authorize.go')
for name in ('LICENSE','NOTICE'): shutil.copy2(root/name, root/'dist'/name)
payload = {p.relative_to(root/'dist').as_posix():p.read_bytes() for folder in ('bin','ui') for p in (root/'dist'/folder).rglob('*') if p.is_file()}
payload['backend/authorize.go']=(root/'tools/authorize.go').read_bytes()
payload.update({name:(root/'dist'/name).read_bytes() for name in ('LICENSE','NOTICE')})
m['files'] = {name:hashlib.sha256(data).hexdigest() for name,data in payload.items()}
with zipfile.ZipFile(root/'dist'/(m['id']+'-'+m['version']+'-arm64.unsigned.zip'),'w',zipfile.ZIP_DEFLATED) as z:
 z.writestr('manifest.json',json.dumps(m,sort_keys=True,separators=(',',':'),ensure_ascii=False))
 for name,data in sorted(payload.items()):z.writestr(name,data)
