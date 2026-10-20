#!/usr/bin/env python3
"""Manage only the loopback-bound, synthetic MCP acceptance sandbox."""
import argparse
import datetime
import hashlib
import hmac
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / "test/mcp2/tmp/sandbox"
PROJECT = "alertbridge-mcp-sandbox"
BASE = "http://127.0.0.1:18083"


def private_write(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(text)


def load():
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    STATE.chmod(0o700)
    path = STATE / "state.json"
    if path.exists():
        return json.loads(path.read_text())
    state = {"mcp_token": secrets.token_hex(32), "callback_hosts": "callback.example", "public_url": ""}
    private_write(path, json.dumps(state))
    private_write(STATE / "admin-password", secrets.token_urlsafe(32))
    # Docker's non-root user must read the file-backed Secret; the parent stays 0700.
    (STATE / "admin-password").chmod(0o644)
    private_write(STATE / "compose.env", "")
    return state


def compose(state, *args):
    env = os.environ.copy()
    env.update({"ALERTBRIDGE_PORT": "18083", "ALERTBRIDGE_ADMIN_PASSWORD_FILE": str(STATE / "admin-password"),
                "ALERTBRIDGE_MCP_TOKEN_SHA256": hashlib.sha256(state["mcp_token"].encode()).hexdigest(),
                "ALERTBRIDGE_MCP_ROUTES": "sandbox", "ALERTBRIDGE_MCP_EVENTS": "1",
                "ALERTBRIDGE_MCP_EVENT_CALLBACK_HOSTS": state["callback_hosts"],
                "ALERTBRIDGE_PUBLIC_URL": state["public_url"], "ALERTBRIDGE_MCP_OAUTH_REDIRECT": "",
                "ALERTBRIDGE_ADMIN_USERNAME": "admin",
                "SANDBOX_INSECURE_COOKIE": "0" if state["public_url"] else "1"})
    subprocess.run(["docker", "compose", "--project-name", PROJECT, "--env-file", str(STATE / "compose.env"),
                    "-f", str(ROOT / "compose.yaml"), "-f", str(ROOT / "compose.mcp-sandbox.yaml"), *args],
                   cwd=ROOT, env=env, check=True)


def request(opener, path, data=None, headers=None):
    req = urllib.request.Request(BASE + path, data=data, headers=headers or {})
    with opener.open(req, timeout=10) as response:
        return response.read().decode()


def seed(state):
    if state.get("client_secret"):
        return
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), urllib.request.ProxyHandler({}))
    def form(path, values):
        return request(opener, path, urllib.parse.urlencode(values).encode(), {"Content-Type": "application/x-www-form-urlencoded"})
    form("/admin/login", {"username": "admin", "password": (STATE / "admin-password").read_text()})
    dashboard = request(opener, "/admin/")
    csrf = re.search(r'name="csrf" value="([^"]+)"', dashboard).group(1)
    form("/admin/channels/save", {"csrf": csrf, "id": "sandbox.sink", "type": "feishu", "enabled": "on",
                                 "endpoint": "http://sandbox-sink:9090/hook", "message_type": "card", "keyword": "AlertBridge"})
    for severity in ("info", "warning", "critical"):
        form("/admin/routes/save", {"csrf": csrf, "routing_key": "sandbox", "severity": severity, "channels": "sandbox.sink"})
    page = form("/admin/clients/create", {"csrf": csrf, "id": "sandbox.synthetic", "routes": "sandbox", "enabled": "on", "rate_limit": "60"})
    match = re.search(r'<pre class="secret-value"[^>]*>([^<]+)</pre>', page)
    if not match:
        raise RuntimeError("sandbox client creation failed; inspect its local admin console")
    state["client_secret"] = match.group(1)
    private_write(STATE / "state.json", json.dumps(state))


def emit(state, severity):
    if "client_secret" not in state:
        raise RuntimeError("start the sandbox before emitting a synthetic event")
    now = datetime.datetime.now(datetime.timezone.utc).isoformat()
    body = json.dumps({"event_id": "sandbox-" + secrets.token_hex(12), "source": "sandbox.synthetic", "routing_key": "sandbox",
                       "status": "info", "severity": severity, "title": "AlertBridge isolated synthetic alert",
                       "message": "Synthetic acceptance data only. No production event or action.", "occurred_at": now}, separators=(",", ":")).encode()
    stamp, nonce = str(int(time.time())), secrets.token_hex(16)
    signature = hmac.new(state["client_secret"].encode(), f"{stamp}\n{nonce}\n{hashlib.sha256(body).hexdigest()}".encode(), hashlib.sha256).hexdigest()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    response = request(opener, "/api/v1/events", body, {"Content-Type": "application/json", "X-Notify-Client": "sandbox.synthetic",
                       "X-Notify-Timestamp": stamp, "X-Notify-Nonce": nonce, "X-Notify-Signature": signature})
    print(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("up", "status", "down", "emit", "configure"))
    parser.add_argument("--callback-hosts")
    parser.add_argument("--public-url")
    parser.add_argument("--severity", choices=("info", "warning", "critical"), default="info")
    args = parser.parse_args()
    state = load()
    if args.action == "configure":
        if args.callback_hosts is not None:
            if not all(re.fullmatch(r"[A-Za-z0-9.-]+", h) for h in args.callback_hosts.split(",")):
                parser.error("supply exact callback hostnames, not URLs or wildcards")
            state["callback_hosts"] = args.callback_hosts
        if args.public_url is not None:
            u = urllib.parse.urlsplit(args.public_url)
            if u.scheme != "https" or not u.netloc or u.username or u.password or u.path not in ("", "/") or u.query or u.fragment:
                parser.error("public-url must be an HTTPS origin")
            state["public_url"] = args.public_url.rstrip("/")
        private_write(STATE / "state.json", json.dumps(state))
        print("Sandbox-only configuration saved. Run up to apply; no external ingress was opened.")
    elif args.action == "up":
        compose(state, "up", "-d", "--build")
        for attempt in range(60):
            try:
                urllib.request.build_opener(urllib.request.ProxyHandler({})).open(BASE + "/readyz", timeout=2).close()
                break
            except (OSError, urllib.error.URLError):
                time.sleep(.5)
        else:
            raise RuntimeError("sandbox did not become ready")
        seed(state)
        print("Isolated synthetic sandbox ready: " + BASE + "/mcp")
        print("Only loopback port 18083; separate database/key volumes and credentials. No production changes.")
    elif args.action == "emit":
        emit(state, args.severity)
    elif args.action == "status":
        compose(state, "ps")
    elif args.action == "down":
        compose(state, "down", "--remove-orphans")  # Deliberately retains data/key volumes.

if __name__ == "__main__":
    main()
