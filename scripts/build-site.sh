#!/bin/sh
# Build only public, allowlisted files. Never copy the repository or runtime data.
set -eu
cd "$(dirname "$0")/.."
python3 test/site/check.py
python3 - <<'PY'
from pathlib import Path
import shutil
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
(out / '.nojekyll').touch()
print('Built dist-site/: collection at /, AlertBridge at /alertbridge/')
PY
