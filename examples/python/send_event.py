#!/usr/bin/env python3
"""Send one v1 HMAC event using Python's standard library; no secrets in argv."""
import argparse
import hashlib
import hmac
import json
import os
from pathlib import Path
import secrets
import stat
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None  # Never forward authentication to a redirect target.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--status', choices=['test', 'info', 'firing', 'resolved'], default='test')
    parser.add_argument('--dedupe-key', default='example-disk')
    args = parser.parse_args()
    endpoint = os.environ.get('ALERTBRIDGE_URL', 'http://127.0.0.1:18081').rstrip('/')
    client = os.environ.get('ALERTBRIDGE_CLIENT_ID', '')
    route = os.environ.get('ALERTBRIDGE_ROUTING_KEY', '')
    secret_file = os.environ.get('ALERTBRIDGE_CLIENT_SECRET_FILE', '')
    url = urllib.parse.urlsplit(endpoint)
    local = url.hostname in ('127.0.0.1', 'localhost', '::1')
    if (url.scheme != 'https' and not (url.scheme == 'http' and local)) or not url.hostname or url.username or url.password or url.query or url.fragment or url.path:
        parser.error('ALERTBRIDGE_URL must be an HTTPS origin (HTTP allowed only on loopback)')
    if not client or not route or not secret_file:
        parser.error('set ALERTBRIDGE_CLIENT_ID, ALERTBRIDGE_ROUTING_KEY and ALERTBRIDGE_CLIENT_SECRET_FILE')
    path = Path(secret_file)
    info = path.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        parser.error('client secret must be a private regular file (chmod 600)')
    secret = path.read_bytes().strip()
    if not secret:
        parser.error('client secret file is empty')
    event = {
        'event_id': 'example-' + secrets.token_hex(16), 'source': 'python-example',
        'routing_key': route, 'status': args.status,
        'severity': os.environ.get('ALERTBRIDGE_SEVERITY', 'warning'),
        'title': 'AlertBridge Python example', 'message': 'Explicitly requested example event.',
        'occurred_at': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
        'dedupe_key': args.dedupe_key,
    }
    body = json.dumps(event, ensure_ascii=False, separators=(',', ':')).encode('utf-8')
    timestamp, nonce = str(int(time.time())), secrets.token_hex(16)
    signed = f'{timestamp}\n{nonce}\n{hashlib.sha256(body).hexdigest()}'.encode('ascii')
    signature = hmac.new(secret, signed, hashlib.sha256).hexdigest()
    request = urllib.request.Request(endpoint + '/api/v1/events', data=body, method='POST', headers={
        'Content-Type': 'application/json', 'X-Notify-Client': client,
        'X-Notify-Timestamp': timestamp, 'X-Notify-Nonce': nonce, 'X-Notify-Signature': signature,
    })
    try:
        with urllib.request.build_opener(NoRedirect).open(request, timeout=15) as response:
            print(f'HTTP {response.status}')
            print(response.read(65536).decode('utf-8'))
            print('Accepted is not delivered. Check delivery records in the admin console.')
    except urllib.error.HTTPError as exc:
        print(f'HTTP {exc.code}; inspect server logs without sharing credentials.', file=sys.stderr)
        return 1
    except urllib.error.URLError:
        print('Connection failed; check endpoint, TLS and network.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except OSError:
        sys.exit('Could not read the private secret file; verify path and permissions.')
