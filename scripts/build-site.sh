#!/bin/sh
# Build only public, allowlisted files. Never copy the repository or runtime data.
set -eu
cd "$(dirname "$0")/.."
python3 test/site/check.py
python3 - <<'PY'
from pathlib import Path
import shutil
import hashlib
out = Path('dist-site')
# This directory is exclusively generated; fail on a symlink to protect other paths.
if out.is_symlink():
    raise SystemExit('dist-site must not be a symlink')
if out.exists():
    shutil.rmtree(out)
(out / 'alertbridge').mkdir(parents=True)
for name in ('index.html', 'style.css', 'demo.js', 'mark.svg'):
    shutil.copyfile(Path('site') / name, out / 'alertbridge' / name)
shutil.copyfile('site/collection.html', out / 'index.html')
entries = [f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(out).as_posix()}' for p in sorted(out.rglob('*')) if p.is_file()]
(out / 'SHA256SUMS').write_text('\n'.join(entries) + '\n')
print('Built dist-site/: collection at /, AlertBridge at /alertbridge/')
PY
