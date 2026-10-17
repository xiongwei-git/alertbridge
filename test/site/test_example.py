#!/usr/bin/env python3
"""Verify the public Python example's wire signature and redirect boundary."""
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ExampleTest(unittest.TestCase):
    def test_signed_bytes_and_no_redirect(self):
        received = []
        response_code = [202]

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                received.append((self.path, self.headers, body))
                self.send_response(response_code[0])
                if response_code[0] == 307:
                    self.send_header('Location', '/must-not-follow')
                self.end_headers()
                self.wfile.write(b'{"outcome":"queued"}')

            def log_message(self, *args):
                pass

        server = HTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as folder:
                secret = Path(folder) / 'secret'
                secret.write_text('example-only-secret\n')
                secret.chmod(0o600)
                env = {**os.environ, 'ALERTBRIDGE_URL': f'http://127.0.0.1:{server.server_port}', 'ALERTBRIDGE_CLIENT_ID': 'example-client', 'ALERTBRIDGE_ROUTING_KEY': 'ops', 'ALERTBRIDGE_CLIENT_SECRET_FILE': str(secret)}
                command = [sys.executable, str(ROOT / 'examples/python/send_event.py'), '--status', 'firing']
                result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                path, headers, body = received[-1]
                self.assertEqual(path, '/api/v1/events')
                signed = f'{headers["X-Notify-Timestamp"]}\n{headers["X-Notify-Nonce"]}\n{hashlib.sha256(body).hexdigest()}'.encode()
                self.assertEqual(headers['X-Notify-Signature'], hmac.new(b'example-only-secret', signed, hashlib.sha256).hexdigest())
                self.assertEqual(json.loads(body)['status'], 'firing')
                response_code[0] = 307
                result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(len(received), 2, 'must not forward authentication through redirects')
                secret.chmod(0o644)
                result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(len(received), 2, 'must reject unsafe secret before connecting')
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)


if __name__ == '__main__':
    unittest.main()
