#!/usr/bin/env python3
"""Offline validation for the public-only static artifact and documentation links."""
from html.parser import HTMLParser
from pathlib import Path
import re
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]


class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = set()
        self.links = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if 'id' in attrs:
            assert attrs['id'] not in self.ids, f'duplicate ID {attrs["id"]}'
            self.ids.add(attrs['id'])
        for key in ('href', 'src'):
            if key in attrs:
                self.links.append(attrs[key])


for name in ('index.html', 'collection.html'):
    file = ROOT / 'site' / name
    page = Page()
    page.feed(file.read_text())
    for link in page.links:
        url = urlsplit(link)
        if url.scheme or url.netloc:
            assert url.scheme == 'https', f'insecure external link: {link}'
            continue
        # collection.html is published one level above the project assets.
        path = url.path.removeprefix('alertbridge/') if name == 'collection.html' else url.path
        target = file.parent / unquote(path)
        if path:
            assert (name == 'index.html' and path == '../') or target.exists(), f'missing site asset: {link}'
        if not path and url.fragment:
            assert url.fragment in page.ids, f'missing anchor: {link}'

home_source = (ROOT / 'internal/httpapi/static/service-home.html').read_text()
home = Page()
home.feed(home_source)
assert home_source.count('<style>') == 1, 'service home uses one CSP-hashed style block'
assert not re.search(r'<(?:script|form|input|iframe|object|embed|img|link)\b', home_source, re.I), 'service home must be standalone and passive'
assert not re.search(r'@import|url\s*\(|\son\w+\s*=', home_source, re.I), 'service home must not load assets or execute handlers'
for link in home.links:
    assert link in ('/', '/admin/', '#main') or link.startswith('https://'), f'unexpected service home link: {link}'
assert '无需登录' in home_source and '不读取或展示内部告警数据' in home_source

script = (ROOT / 'site/demo.js').read_text()
for forbidden in ('fetch(', 'XMLHttpRequest', 'WebSocket(', 'localStorage', 'sessionStorage', 'innerHTML', 'sendBeacon'):
    assert forbidden not in script, f'demo must remain local-only: {forbidden}'

# Validate all tracked public Markdown references and local heading anchors.
import subprocess

def heading_ids(path):
    result, duplicates = set(), {}
    fence = False
    for line in path.read_text().splitlines():
        if line.startswith(('```', '~~~')):
            fence = not fence
            continue
        if fence or not re.match(r'^#{1,6} ', line):
            continue
        title = re.sub(r'^#{1,6} ', '', line).strip().lower()
        slug = re.sub(r'[^\w\- ]', '', title).replace(' ', '-')
        count = duplicates.get(slug, 0)
        duplicates[slug] = count + 1
        result.add(slug + (f'-{count}' if count else ''))
    return result

tracked = subprocess.check_output(['git', 'ls-files', '*.md'], cwd=ROOT, text=True).splitlines()
for relative in tracked:
    file = ROOT / relative
    for link in re.findall(r'\]\(([^)]+)\)', file.read_text()):
        url = urlsplit(link)
        if url.scheme or url.netloc:
            continue
        target = file.parent / unquote(url.path) if url.path else file
        assert target.exists(), f'{relative}: broken link {link}'
        if url.fragment and target.suffix == '.md':
            assert unquote(url.fragment) in heading_ids(target), f'{relative}: broken heading {link}'

version = (ROOT / 'VERSION').read_text().strip()
for relative in ('README.md', 'README.en.md', 'docs/deployment.md', 'site/index.html', 'site/collection.html'):
    source = (ROOT / relative).read_text()
    assert version in source, f'{relative}: missing current release {version}'
    assert 'github.io/alertbridge' not in source, f'{relative}: retired Pages URL'
for relative in ('README.md', 'docs/deployment.md'):
    source = (ROOT / relative).read_text()
    for referenced in re.findall(r'alertbridge/(v[0-9.]+)/compose(?:\.oidc)?\.yaml', source):
        assert referenced == version, f'{relative}: stale installation version {referenced}'
assert not (ROOT / '.github/workflows/pages.yml').exists(), 'Pages workflow is retired'
print('Static assets, anchors, all public Markdown links, release references and demo isolation checks passed.')
