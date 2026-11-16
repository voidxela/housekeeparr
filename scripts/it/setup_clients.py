#!/usr/bin/env python3
"""Harness-side setup of disposable torrent clients for the integration suite.

Adds the synthetic torrents from manifest.json to one disposable client and
applies the per-torrent setup (file deselection, recheck, stop). These writes
go directly to the disposable instance on loopback, never through
Housekeeparr. Nothing here contacts a public network.

Usage:
  setup_clients.py transmission URL USER PASS MANIFEST TORRENT_DIR SAVE_DIR
  setup_clients.py transmission-post-restart URL USER PASS MANIFEST SAVE_DIR
  setup_clients.py deluge URL PASSWORD DAEMON_PORT DAEMON_USER DAEMON_PASS MANIFEST TORRENT_DIR SAVE_DIR
  setup_clients.py deluge-post-restart URL PASSWORD DAEMON_PORT DAEMON_USER DAEMON_PASS MANIFEST SAVE_DIR
  setup_clients.py rtorrent SCGI_PORT MANIFEST TORRENT_DIR SAVE_DIR
  setup_clients.py rtorrent-post-restart SCGI_PORT MANIFEST SAVE_DIR
"""
import base64, json, os, socket, sys, time, urllib.request, xmlrpc.client


def load(manifest):
    with open(manifest) as f:
        return json.load(f)


def metainfo(tdir, t):
    with open("%s/%s.torrent" % (tdir, t["name"]), "rb") as f:
        return f.read()


def by_key(m, key):
    return [t for t in m if t["key"] == key]


def cat_for(t):
    return "movies-radarr" if t.get("manager") == "radarr" else "tv-sonarr"


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
        ddir = os.path.join(save, cat_for(t))
        if t["key"] == "magnet":
            c.call("torrent-add", filename=magnet(t), **{"download-dir": ddir})
        else:
            c.call("torrent-add", metainfo=base64.b64encode(metainfo(tdir, t)).decode(), **{"download-dir": ddir})
    time.sleep(2)
    for t in m:
        if t.get("deselect"):
            c.call("torrent-set", ids=[t["hash"]], **{"files-unwanted": [t["files"].index(t["deselect"])]})
        if t.get("data", "none") != "none":  # verify any torrent with local data
            c.call("torrent-verify", ids=[t["hash"]])
        if t["key"] == "stopped":
            c.call("torrent-stop", ids=[t["hash"]])
    time.sleep(3)


def transmission_post_restart(url, user, pw, manifest, save):
    c = Transmission(url, user, pw)
    m = load(manifest)
    hashes = [t["hash"] for t in by_key(m, "errored-restart")]
    if hashes:
        res = c.call("torrent-get", ids=hashes, fields=["hashString", "status", "error", "errorString"])
        for tor in res.get("torrents", []):
            if tor.get("status") == 0:
                c.call("torrent-start", ids=[tor["hashString"]])
    for t in by_key(m, "errored"):
        c.call("torrent-stop", ids=[t["hash"]])
        time.sleep(1)
        delete_payload(os.path.join(save, cat_for(t)), t)
        c.call("torrent-start", ids=[t["hash"]])
    for t in by_key(m, "errored-recheck"):
        delete_payload(os.path.join(save, cat_for(t)), t)
        c.call("torrent-verify", ids=[t["hash"]])
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


def deluge_connect(url, pw, dport, duser, dpass):
    c = DelugeWeb(url)
    if not c.call("auth.login", pw):
        raise RuntimeError("deluge-web login failed")
    if c.call("web.connected"):
        return c
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
            return c
        time.sleep(1)
    raise RuntimeError("deluge-web did not connect to the daemon")


def deluge(url, pw, dport, duser, dpass, manifest, tdir, save):
    c = deluge_connect(url, pw, dport, duser, dpass)
    label_ok = False
    try:
        methods = c.call("system.listMethods")
        if not any(m.startswith("label.") for m in methods):
            c.call("core.enable_plugin", "Label")
            time.sleep(1)
            methods = c.call("system.listMethods")
        if any(m.startswith("label.") for m in methods):
            existing = c.call("label.get_labels")
            for cat in ("tv-sonarr", "movies-radarr"):
                if cat not in existing:
                    c.call("label.add", cat)
            label_ok = True
    except Exception as exc:
        print("deluge Label plugin unavailable: %s" % exc, file=sys.stderr)
    flag_path = os.path.join(os.path.dirname(save), "deluge-label.ok")
    if label_ok:
        with open(flag_path, "w") as f:
            f.write("1\n")
    m = load(manifest)
    opts = {"download_location": save, "add_paused": False}
    for t in m:
        if t["key"] == "magnet":
            c.call("core.add_torrent_magnet", magnet(t), opts)
        else:
            c.call("core.add_torrent_file", t["name"] + ".torrent", base64.b64encode(metainfo(tdir, t)).decode(), opts)
        if label_ok:
            c.call("label.set_torrent", t["hash"], cat_for(t))
    time.sleep(2)
    for t in m:
        if t.get("deselect"):
            prio = [0 if f == t["deselect"] else 4 for f in t["files"]]
            c.call("core.set_torrent_options", [t["hash"]], {"file_priorities": prio})
        if t.get("data", "none") != "none":  # verify any torrent with local data
            c.call("core.force_recheck", [t["hash"]])
        if t["key"] == "stopped":
            c.call("core.pause_torrent", [t["hash"]])
    full_hashes = [t["hash"] for t in m if t.get("data") == "full"]
    deadline = time.time() + 30
    while time.time() < deadline:
        if all(c.call("core.get_torrent_status", h, ["state"]).get("state") == "Seeding" for h in full_hashes):
            break
        time.sleep(0.5)
    time.sleep(1)


def deluge_post_restart(url, pw, dport, duser, dpass, manifest, save):
    c = deluge_connect(url, pw, dport, duser, dpass)
    m = load(manifest)
    for t in by_key(m, "errored-restart"):
        st = c.call("core.get_torrent_status", t["hash"], ["state", "message"])
        print("deluge errored-restart after restart: %r" % (st,), file=sys.stderr)
        if st.get("state") == "Paused":
            c.call("core.resume_torrent", [t["hash"]])
    for t in by_key(m, "errored"):
        c.call("core.pause_torrent", [t["hash"]])
        time.sleep(1)
        delete_payload(save, t)
        c.call("core.resume_torrent", [t["hash"]])
    for t in by_key(m, "errored-recheck"):
        delete_payload(save, t)
        c.call("core.force_recheck", [t["hash"]])
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
        cmds = ["d.directory.set=" + save, "d.custom1.set=" + cat_for(t)]
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
    # Save the session only once every hash check has finished, so that no
    # download restarts with an interrupted check (see rtorrent_wait_idle).
    rtorrent_wait_idle(port, m)
    scgi_call(port, "session.save")


def rtorrent_wait_idle(port, m, timeout=90):
    """Wait until every listed download is loaded and none is hash checking.

    rTorrent opens a download's files while it hashes them. A payload deleted
    during a check is still read through the open descriptor, the check
    passes, and a d.check_hash sent meanwhile is ignored (observed on 0.9.8:
    the download stays started and complete over a "(deleted)" file). Fixed
    sleeps let a slow host hit that window, so wait for idleness instead.
    """
    hashes = [t["hash"].upper() for t in m if t["key"] != "magnet"]
    deadline = time.time() + timeout
    while True:
        busy = []
        for h in hashes:
            try:
                if scgi_call(port, "d.hashing", h) != 0:
                    busy.append(h)
            except xmlrpc.client.Fault:  # session still loading
                busy.append(h)
        if not busy:
            return
        if time.time() > deadline:
            raise SystemExit("rtorrent: downloads still loading or hash checking after %ds: %s" % (timeout, busy))
        time.sleep(0.5)


def rtorrent_await_recheck_failure(port, h, timeout=60):
    """Require that the forced check of a deleted payload actually failed."""
    deadline = time.time() + timeout
    while True:
        st = {k: scgi_call(port, k, h) for k in ("d.hashing", "d.state", "d.complete", "d.message")}
        if st["d.hashing"] == 0 and st["d.complete"] == 0 and st["d.state"] == 0 and st["d.message"]:
            return
        if time.time() > deadline:
            raise SystemExit("rtorrent errored-recheck fixture setup did not fail its check: %r" % (st,))
        time.sleep(0.5)


def rtorrent_post_restart(port, manifest, save):
    m = load(manifest)
    rtorrent_wait_idle(port, m)
    for t in by_key(m, "errored-restart"):
        h = t["hash"].upper()
        state = scgi_call(port, "d.state", h)
        msg = scgi_call(port, "d.message", h)
        print("rtorrent errored-restart after restart: state=%s message=%r" % (state, msg), file=sys.stderr)
        if state == 0:
            scgi_call(port, "d.start", h)
    for t in by_key(m, "errored"):
        h = t["hash"].upper()
        scgi_call(port, "d.stop", h)
        scgi_call(port, "d.close", h)
        time.sleep(1)
        delete_payload(save, t)
        scgi_call(port, "d.start", h)
    rtorrent_wait_idle(port, m)
    for t in by_key(m, "errored-recheck"):
        h = t["hash"].upper()
        delete_payload(save, t)
        scgi_call(port, "d.check_hash", h)
        rtorrent_await_recheck_failure(port, h)
    time.sleep(3)


if __name__ == "__main__":
    kind, args = sys.argv[1], sys.argv[2:]
    {
        "transmission": transmission,
        "transmission-post-restart": transmission_post_restart,
        "deluge": deluge,
        "deluge-post-restart": deluge_post_restart,
        "rtorrent": rtorrent,
        "rtorrent-post-restart": rtorrent_post_restart,
    }[kind](*args)
