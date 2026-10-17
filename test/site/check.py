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
            assert target.exists(), f'missing site asset: {link}'
        if not path and url.fragment:
            assert url.fragment in page.ids, f'missing anchor: {link}'

script = (ROOT / 'site/demo.js').read_text()
for forbidden in ('fetch(', 'XMLHttpRequest', 'WebSocket(', 'localStorage', 'sessionStorage', 'innerHTML', 'sendBeacon'):
    assert forbidden not in script, f'demo must remain local-only: {forbidden}'

# Validate local Markdown references in all changed/new public documentation.
for file in [ROOT / n for n in ('README.md', 'README.en.md', 'CONTRIBUTING.md', 'SECURITY.md', 'CODE_OF_CONDUCT.md', 'docs/dots-example.md', 'docs/showcase-deployment.md', 'docs/opensource-review.md', 'examples/python/README.md')]:
    for link in re.findall(r'\]\(([^)]+)\)', file.read_text()):
        url = urlsplit(link)
        if url.scheme or url.netloc or not url.path:
            continue
        assert (file.parent / unquote(url.path)).exists(), f'{file}: broken link {link}'
print('Static assets, anchors, documentation links and demo isolation checks passed.')
