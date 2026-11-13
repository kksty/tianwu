#!/usr/bin/env python3
"""Acceptance target: a tiny intranet app with a few deliberate weaknesses.

Runs in the Kali WSL VM (172.26.77.43) so the control plane (192.168.121.97)
reaches it across network segments — or on a VPS as a 127.0.0.1-only target
(`TW_TARGET_HOST=127.0.0.1`). Weaknesses are the kind a scanner finds without
exploit code: default credentials, an unauthenticated admin API, an exposed
backup archive and a directory listing.
"""
import io
import json
import os
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs

HOST = os.environ.get("TW_TARGET_HOST", "0.0.0.0")
PORT = int(os.environ.get("TW_TARGET_PORT", "18099"))
SID = "sid=tw-acceptance-admin"

LOGIN = """<!doctype html><html><head><title>运维平台登录</title></head><body>
<h2>运维平台</h2>
<form method="post" action="/login">
  <label>账号 <input name="user"></label>
  <label>密码 <input name="pass" type="password"></label>
  <button>登录</button>
</form></body></html>"""

ADMIN = """<!doctype html><html><head><title>运维平台 · 用户管理</title></head><body>
<h2>用户管理</h2>
<table border="1"><tr><th>账号</th><th>密码</th><th>角色</th></tr>
<tr><td>admin</td><td>admin</td><td>superadmin</td></tr>
<tr><td>ops</td><td>Ops@2024</td><td>operator</td></tr>
<tr><td>backup</td><td>Bk#7788</td><td>readonly</td></tr></table>
<p>数据库：mysql://ops:Ops%402024@10.20.30.11:3306/ops</p>
</body></html>"""

CONFIG = """server: ops.example.internal
database:
  host: 10.20.30.11
  port: 3306
  user: ops
  password: Ops@2024
backup:
  bucket: oss://ops-backup
  access_key: LTAI5tFakeAcceptanceKey
  secret_key: FakeSecretForAcceptanceOnly
"""

NOTES = "运维备注：备份包每周五导出到 /backup.zip，管理后台只做了登录页没做权限校验。\n"


def backup_zip() -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("config.yml", CONFIG)
        z.writestr("notes.txt", NOTES)
    return buf.getvalue()


BACKUP = backup_zip()


class Handler(BaseHTTPRequestHandler):
    server_version = "nginx/1.18.0"
    sys_version = ""

    def log_message(self, fmt, *args):
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)

    def send(self, code, body, ctype="text/html; charset=utf-8", headers=None):
        data = body.encode() if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)

    def authed(self):
        return SID in (self.headers.get("Cookie") or "")

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/":
            self.send(200, LOGIN)
        elif path == "/robots.txt":
            self.send(200, "User-agent: *\nDisallow: /admin\nDisallow: /backup.zip\n", "text/plain")
        elif path == "/admin":
            if self.authed():
                self.send(200, ADMIN)
            else:
                self.send(403, "<h1>403 Forbidden</h1>登录后才能访问管理后台。")
        elif path == "/api/users":
            # Unauthenticated: the frontend calls it before login.
            self.send(200, json.dumps([
                {"user": "admin", "pass": "admin", "role": "superadmin"},
                {"user": "ops", "pass": "Ops@2024", "role": "operator"},
            ]), "application/json")
        elif path == "/backup.zip":
            self.send(200, BACKUP, "application/zip",
                      {"Content-Disposition": 'attachment; filename="backup.zip"'})
        elif path in ("/files", "/files/"):
            names = sorted(os.listdir("/tmp/tw-target/files"))
            rows = "".join('<li><a href="/files/%s">%s</a></li>' % (n, n) for n in names)
            self.send(200, "<h2>Index of /files/</h2><ul>%s</ul>" % rows)
        elif path.startswith("/files/"):
            name = os.path.basename(path)
            full = os.path.join("/tmp/tw-target/files", name)
            if os.path.isfile(full):
                with open(full, "rb") as fh:
                    self.send(200, fh.read(), "text/plain; charset=utf-8")
            else:
                self.send(404, "not found")
        else:
            self.send(404, "<h1>404 Not Found</h1>")

    def do_POST(self):
        if self.path.split("?", 1)[0] != "/login":
            self.send(404, "not found")
            return
        size = int(self.headers.get("Content-Length") or 0)
        form = parse_qs(self.rfile.read(size).decode("utf-8", "replace"))
        user = (form.get("user") or [""])[0]
        pw = (form.get("pass") or [""])[0]
        if user == "admin" and pw == "admin":
            self.send(302, "", headers={"Location": "/admin", "Set-Cookie": SID + "; Path=/"})
        else:
            self.send(401, "<h1>401</h1>账号或密码错误。")


if __name__ == "__main__":
    os.makedirs("/tmp/tw-target/files", exist_ok=True)
    with open("/tmp/tw-target/files/notes.txt", "w") as fh:
        fh.write(NOTES)
    print("acceptance target on %s:%d" % (HOST, PORT), flush=True)
    ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
