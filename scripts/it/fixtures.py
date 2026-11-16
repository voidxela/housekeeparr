#!/usr/bin/env python3
"""Create synthetic local payloads and .torrent files for the integration suite.

Nothing here touches the network. Trackers point at a closed loopback port, so
no peer can ever connect; payloads are random bytes generated locally.
Writes manifest.json describing what each torrent should look like.
"""
import hashlib, json, os, sys

PIECE = 256 * 1024
TRACKER = "http://127.0.0.1:9/announce/0123456789abcdef0123456789abcdef"  # fake passkey, closed port

def bencode(v):
    if isinstance(v, int): return b"i%de" % v
    if isinstance(v, bytes): return b"%d:%s" % (len(v), v)
    if isinstance(v, str): return bencode(v.encode())
    if isinstance(v, list): return b"l" + b"".join(bencode(x) for x in v) + b"e"
    if isinstance(v, dict):
        return b"d" + b"".join(bencode(k) + bencode(v[k]) for k in sorted(v, key=lambda k: k.encode() if isinstance(k, str) else k)) + b"e"
    raise TypeError(type(v))

def pieces(data):
    return b"".join(hashlib.sha1(data[i:i + PIECE]).digest() for i in range(0, len(data), PIECE))

def make(out, name, files, private=False):
    """files: list of (relative path, size). Returns (info_hash, payload dict)."""
    payload = {p: os.urandom(s) for p, s in files}
    if len(files) == 1 and files[0][0] == name:
        info = {"name": name, "length": files[0][1], "piece length": PIECE, "pieces": pieces(payload[name])}
    else:
        blob = b"".join(payload[p] for p, _ in files)
        info = {"name": name, "piece length": PIECE, "pieces": pieces(blob),
                "files": [{"length": s, "path": p.split("/")} for p, s in files]}
    if private:
        info["private"] = 1
    meta = {"announce": TRACKER, "info": info, "created by": "housekeeparr-it"}
    with open(os.path.join(out, name + ".torrent"), "wb") as f:
        f.write(bencode(meta))
    return hashlib.sha1(bencode(info)).hexdigest(), payload

def write(root, name, payload, single, keep=lambda path, data: data):
    for p, data in payload.items():
        path = os.path.join(root, p if single else os.path.join(name, p))
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(keep(p, data))

def main(out, save):
    os.makedirs(out, exist_ok=True); os.makedirs(save, exist_ok=True)
    m = []
    def add(key, name, files, expect, private=False, data="none", **extra):
        h, payload = make(out, name, files, private)
        single = len(files) == 1 and files[0][0] == name
        if data == "full":
            write(save, name, payload, single)
        elif data == "half":
            # Keep the first half of the pieces intact; zero the rest.
            write(save, name, payload, single, lambda p, d: d[: len(d) // 2] + b"\0" * (len(d) - len(d) // 2))
        m.append(dict(key=key, name=name, hash=h, expect=expect, private=private, data=data, files=[p for p, _ in files], **extra))
    MB = 1024 * 1024
    add("complete", "Example.Show.S01E01.1080p.WEB.h264-HKIT", [("Example.Show.S01E01.1080p.WEB.h264-HKIT", 4 * MB)], "complete", data="full")
    add("half", "Example.Show.S01E02.1080p.WEB.h264-HKIT", [("Example.Show.S01E02.1080p.WEB.h264-HKIT", 4 * MB)], "downloading", data="half")
    add("empty", "Example.Show.S01E03.1080p.WEB.h264-HKIT", [("Example.Show.S01E03.1080p.WEB.h264-HKIT", 4 * MB)], "downloading")
    add("private", "Example.Show.S01E04.1080p.WEB.h264-HKIT", [("Example.Show.S01E04.1080p.WEB.h264-HKIT", 2 * MB)], "downloading", private=True)
    add("deselected", "Example.Show.S02.1080p.WEB.h264-HKIT", [("e01.mkv", 2 * MB), ("e02.mkv", 2 * MB), ("sample.mkv", 1 * MB)], "downloading", deselect="sample.mkv")
    add("stopped", "Example.Show.S01E05.1080p.WEB.h264-HKIT", [("Example.Show.S01E05.1080p.WEB.h264-HKIT", 2 * MB)], "paused")
    # Verified, then stopped, payload deleted, and started again by the
    # harness, so the client reports its native missing-data error.
    add("errored", "Example.Show.S03E01.1080p.WEB.h264-HKIT", [("Example.Show.S03E01.1080p.WEB.h264-HKIT", 2 * MB)], "error", data="full")
    # Verified, client stopped cleanly, payload deleted, client restarted with
    # same config/session, and torrent started if stopped.
    add("errored-restart", "Example.Show.S03E02.1080p.WEB.h264-HKIT", [("Example.Show.S03E02.1080p.WEB.h264-HKIT", 2 * MB)], "error", data="full")
    # Verified, payload deleted while running, then force-rechecked.
    add("errored-recheck", "Example.Show.S03E03.1080p.WEB.h264-HKIT", [("Example.Show.S03E03.1080p.WEB.h264-HKIT", 2 * MB)], "error", data="full")
    # Dedicated fixture for probing rTorrent d.message across stop/start.
    add("rt-msg-probe", "Example.Show.S03E04.1080p.WEB.h264-HKIT", [("Example.Show.S03E04.1080p.WEB.h264-HKIT", 2 * MB)], "complete", data="full")
    # A movie for Radarr (qBittorrent category movies-radarr); data never arrives.
    add("movie", "Example.Movie.2024.1080p.WEB.h264-HKIT", [("Example.Movie.2024.1080p.WEB.h264-HKIT.mkv", 2 * MB)], "downloading", manager="radarr", data="half")
    add("movie2", "Another.Movie.2023.1080p.WEB.h264-HKIT", [("Another.Movie.2023.1080p.WEB.h264-HKIT.mkv", 2 * MB)], "downloading", manager="radarr", data="half")
    # A magnet for a random hash: metadata can never arrive.
    m.append(dict(key="magnet", name="Example.Show.S01E06.1080p.WEB.h264-HKIT", hash=hashlib.sha1(os.urandom(32)).hexdigest(), expect="metadata", private=False, files=[]))
    with open(os.path.join(out, "manifest.json"), "w") as f:
        json.dump(m, f, indent=1)

    # Managed releases: never added to a client by the harness. The
    # integration stand-in indexer (internal/integration/standin) offers them
    # to Sonarr/Radarr for fictional content its metadata stand-in serves, so
    # the managers grab them and create real grab history. Partial data, so
    # each grab stays incomplete.
    managed = []
    def release(key, name, size, **ident):
        h, payload = make(out, name, [(name + ".mkv", size)])
        write(save, name, payload, False, lambda p, d: d[: len(d) // 2] + b"\0" * (len(d) - len(d) // 2))
        managed.append(dict(key=key, name=name, hash=h, size=size, files=[name + ".mkv"], data="half", managed=ident))
    release("tv-e01", "Housekeeparr.Fixture.Show.S01E01.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=1)
    release("tv-e02", "Housekeeparr.Fixture.Show.S01E02.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=2)
    release("tv-e03", "Housekeeparr.Fixture.Show.S01E03.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=3)
    release("tv-e04", "Housekeeparr.Fixture.Show.S01E04.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=4)
    # Released only to the recovery workflow test.
    release("tv-e05", "Housekeeparr.Fixture.Show.S01E05.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=5)
    release("film-a", "Housekeeparr.Fixture.Film.2020.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999002, imdbId="tt9990002")
    release("film-b", "Housekeeparr.Second.Fixture.Film.2021.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999003, imdbId="tt9990003")
    release("film-c", "Housekeeparr.Third.Fixture.Film.2019.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999004, imdbId="tt9990004")
    release("film-d", "Housekeeparr.Fourth.Fixture.Film.2018.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999005, imdbId="tt9990005")
    release("film-e", "Housekeeparr.Fifth.Fixture.Film.2017.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999006, imdbId="tt9990006")
    with open(os.path.join(out, "managed.json"), "w") as f:
        json.dump(managed, f, indent=1)

if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
