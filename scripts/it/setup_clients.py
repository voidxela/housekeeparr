#!/usr/bin/env python3
"""Harness-side setup of disposable torrent clients for the integration suite.

Adds the synthetic torrents from manifest.json to one disposable client and
applies the per-torrent setup (file deselection, recheck, stop). These writes
go directly to the disposable instance on loopback, never through
Housekeeparr. Nothing here contacts a public network.

Usage:
  setup_clients.py transmission URL USER PASS MANIFEST TORRENT_DIR SAVE_DIR
  setup_clients.py deluge URL PASSWORD DAEMON_PORT DAEMON_USER DAEMON_PASS MANIFEST TORRENT_DIR SAVE_DIR
  setup_clients.py rtorrent SCGI_PORT MANIFEST TORRENT_DIR SAVE_DIR
"""
import base64, json, os, socket, sys, time, urllib.request, xmlrpc.client


def load(manifest):
    with open(manifest) as f:
        return json.load(f)


def metainfo(tdir, t):
    with open("%s/%s.torrent" % (tdir, t["name"]), "rb") as f:
        return f.read()


def errored(m):
    return [t for t in m if t["key"] == "errored"]


def delete_payload(save, t):
    os.remove(os.path.join(save, t["name"]))


def magnet(t):
    return "magnet:?xt=urn:btih:%s&dn=%s" % (t["hash"], t["name"])


# --- Transmission (legacy RPC) ---------------------------------------------

class Transmission:
    def __init__(self, url, user, pw):
        self.url, self.sid = url, ""
        self.auth = "Basic " + base64.b64encode(("%s:%s" % (user, pw)).encode()).decode()

    def call(self, method, **args):
        body = json.dumps({"method": method, "arguments": args}).encode()
        for _ in range(2):
            req = urllib.request.Request(self.url, data=body, headers={
                "Content-Type": "application/json", "Authorization": self.auth, "X-Transmission-Session-Id": self.sid})
            try:
                with urllib.request.urlopen(req, timeout=30) as r:
                    out = json.load(r)
            except urllib.error.HTTPError as e:
                if e.code == 409:
                    self.sid = e.headers["X-Transmission-Session-Id"]
                    continue
                raise
            if out.get("result") != "success":
                raise RuntimeError("%s: %s" % (method, out.get("result")))
            return out.get("arguments", {})
        raise RuntimeError("session handshake failed")


def transmission(url, user, pw, manifest, tdir, save):
    c = Transmission(url, user, pw)
    m = load(manifest)
    for t in m:
        if t["key"] == "magnet":
            c.call("torrent-add", filename=magnet(t), **{"download-dir": save})
        else:
            c.call("torrent-add", metainfo=base64.b64encode(metainfo(tdir, t)).decode(), **{"download-dir": save})
    time.sleep(2)
    for t in m:
        if t.get("deselect"):
            c.call("torrent-set", ids=[t["hash"]], **{"files-unwanted": [t["files"].index(t["deselect"])]})
        if t.get("data", "none") != "none":  # verify any torrent with local data
            c.call("torrent-verify", ids=[t["hash"]])
        if t["key"] == "stopped":
            c.call("torrent-stop", ids=[t["hash"]])
    time.sleep(3)
    for t in errored(m):
        c.call("torrent-stop", ids=[t["hash"]])
        time.sleep(1)
        delete_payload(save, t)
        c.call("torrent-start", ids=[t["hash"]])
    time.sleep(3)


# --- Deluge (deluge-web JSON API) --------------------------------------------

class DelugeWeb:
    def __init__(self, url):
        self.url, self.cookie, self.id = url.rstrip("/") + "/json", "", 0

    def call(self, method, *params):
        self.id += 1
        req = urllib.request.Request(self.url, data=json.dumps({"method": method, "params": list(params), "id": self.id}).encode(),
                                     headers={"Content-Type": "application/json", "Cookie": self.cookie})
        with urllib.request.urlopen(req, timeout=30) as r:
            c = r.headers.get("Set-Cookie")
            if c:
                self.cookie = c.split(";", 1)[0]
            out = json.load(r)
        if out.get("error"):
            raise RuntimeError("%s: %s" % (method, out["error"]))
        return out["result"]


def deluge(url, pw, dport, duser, dpass, manifest, tdir, save):
    c = DelugeWeb(url)
    if not c.call("auth.login", pw):
        raise RuntimeError("deluge-web login failed")
    host_id = None
    for h in c.call("web.get_hosts"):
        if h[1] in ("127.0.0.1", "localhost") and int(h[2]) == int(dport):
            host_id = h[0]
    if host_id is None:
        res = c.call("web.add_host", "127.0.0.1", int(dport), duser, dpass)
        host_id = res[1] if isinstance(res, list) else res
    c.call("web.connect", host_id)
    for _ in range(30):
        if c.call("web.connected"):
            break
        time.sleep(1)
    else:
        raise RuntimeError("deluge-web did not connect to the daemon")
    m = load(manifest)
    opts = {"download_location": save, "add_paused": False}
    for t in m:
        if t["key"] == "magnet":
            c.call("core.add_torrent_magnet", magnet(t), opts)
        else:
            c.call("core.add_torrent_file", t["name"] + ".torrent", base64.b64encode(metainfo(tdir, t)).decode(), opts)
    time.sleep(2)
    for t in m:
        if t.get("deselect"):
            prio = [0 if f == t["deselect"] else 4 for f in t["files"]]
            c.call("core.set_torrent_options", [t["hash"]], {"file_priorities": prio})
        if t.get("data", "none") != "none":  # verify any torrent with local data
            c.call("core.force_recheck", [t["hash"]])
        if t["key"] == "stopped":
            c.call("core.pause_torrent", [t["hash"]])
    time.sleep(3)
    for t in errored(m):
        c.call("core.pause_torrent", [t["hash"]])
        time.sleep(1)
        delete_payload(save, t)
        c.call("core.resume_torrent", [t["hash"]])
    time.sleep(3)


# --- rTorrent (direct SCGI; the adapter itself goes through the HTTP gateway) -

def scgi_call(port, method, *params):
    body = xmlrpc.client.dumps(params, method).encode()
    headers = b"CONTENT_LENGTH\0%d\0SCGI\x001\0REQUEST_METHOD\0POST\0REQUEST_URI\0/RPC2\0" % len(body)
    s = socket.create_connection(("127.0.0.1", int(port)), timeout=30)
    s.sendall(b"%d:%s," % (len(headers), headers) + body)
    data = b""
    while True:
        chunk = s.recv(65536)
        if not chunk:
            break
        data += chunk
    s.close()
    xml = data.split(b"\r\n\r\n", 1)[1] if b"\r\n\r\n" in data else data.split(b"\n\n", 1)[1]
    return xmlrpc.client.loads(xml.decode())[0][0]


def rtorrent(port, manifest, tdir, save):
    m = load(manifest)
    for t in m:
        cmds = ["d.directory.set=" + save]
        if t["key"] == "magnet":
            # With DHT disabled rTorrent needs a tracker in the magnet; this
            # is the same closed loopback port, so metadata never arrives.
            scgi_call(port, "load.start", "", magnet(t) + "&tr=http%3A%2F%2F127.0.0.1%3A9%2Fannounce", *cmds)
        else:
            scgi_call(port, "load.raw_start", "", xmlrpc.client.Binary(metainfo(tdir, t)), *cmds)
    time.sleep(3)
    for t in m:
        h = t["hash"].upper()
        if t.get("deselect"):
            idx = t["files"].index(t["deselect"])
            scgi_call(port, "f.priority.set", "%s:f%d" % (h, idx), 0)
            scgi_call(port, "d.update_priorities", h)
        if t.get("data", "none") != "none":  # verify any torrent with local data
            scgi_call(port, "d.check_hash", h)
        if t["key"] == "stopped":
            scgi_call(port, "d.stop", h)
    time.sleep(3)
    for t in errored(m):
        h = t["hash"].upper()
        scgi_call(port, "d.stop", h)
        scgi_call(port, "d.close", h)
        time.sleep(1)
        delete_payload(save, t)
        scgi_call(port, "d.start", h)
    time.sleep(3)


if __name__ == "__main__":
    kind, args = sys.argv[1], sys.argv[2:]
    {"transmission": transmission, "deluge": deluge, "rtorrent": rtorrent}[kind](*args)
