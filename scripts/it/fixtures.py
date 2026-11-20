#!/usr/bin/env python3
"""Create synthetic local payloads and .torrent files for the integration suite.

Nothing here touches the network. Trackers point at a closed loopback port, so
no peer can ever connect; payloads are random bytes generated locally.
Writes manifest.json describing what each torrent should look like.
"""
import hashlib, json, os, struct, sys

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

def mkv(seconds, size):
    """A minimal Matroska file that ffprobe reads as an h264 video of the
    given duration (no decodable frames), padded to size bytes. Sonarr's
    sample detection needs a plausible runtime; random bytes are rejected
    as "Unable to determine if file is a sample"."""
    def vsize(n):
        for l in range(1, 9):
            if n < (1 << (7 * l)) - 1:
                return bytes([(1 << (8 - l)) | (n >> (8 * (l - 1)))]) + n.to_bytes(l, "big")[1:]
        raise ValueError(n)
    el = lambda i, p: i + vsize(len(p)) + p
    u = lambda i, v: el(i, v.to_bytes(max(1, (v.bit_length() + 7) // 8), "big"))
    s = lambda i, v: el(i, v.encode())
    ebml = el(b"\x1a\x45\xdf\xa3", u(b"\x42\x86", 1) + u(b"\x42\xf7", 1) + u(b"\x42\xf2", 4) + u(b"\x42\xf3", 8) + s(b"\x42\x82", "matroska") + u(b"\x42\x87", 4) + u(b"\x42\x85", 2))
    info = el(b"\x15\x49\xa9\x66", u(b"\x2a\xd7\xb1", 1000000) + el(b"\x44\x89", struct.pack(">d", seconds * 1000.0)) + s(b"\x4d\x80", "hk") + s(b"\x57\x41", "hk"))
    track = el(b"\xae", u(b"\xd7", 1) + u(b"\x73\xc5", 1) + u(b"\x83", 1) + s(b"\x86", "V_MPEG4/ISO/AVC") + el(b"\xe0", u(b"\xb0", 16) + u(b"\xba", 16)))
    # An audio track too: Sonarr rejects files without one ("No audio
    # tracks detected", observed on 4.0.20).
    track += el(b"\xae", u(b"\xd7", 2) + u(b"\x73\xc5", 2) + u(b"\x83", 2) + s(b"\x86", "A_AC3") + s(b"\x22\xb5\x9c", "eng") +
                el(b"\xe1", el(b"\xb5", struct.pack(">d", 48000.0)) + u(b"\x9f", 2)))
    blocks = b"".join(el(b"\x1f\x43\xb6\x75", u(b"\xe7", tc) + el(b"\xa3", b"\x81\x00\x00\x80" + b"\x00" * 16)) for tc in (0, int(seconds * 1000)))
    head = ebml
    body = info + el(b"\x16\x54\xae\x6b", track) + blocks
    pad = size - len(head) - len(body) - 20
    seg = el(b"\x18\x53\x80\x67", body + el(b"\xec", os.urandom(max(pad, 0))))
    return head + seg

def make(out, name, files, private=False, tracker=TRACKER, contents=None):
    """files: list of (relative path, size). Returns (info_hash, payload dict)."""
    payload = {p: (contents[p] if contents and p in contents else os.urandom(s)) for p, s in files}
    if len(files) == 1 and files[0][0] == name:
        info = {"name": name, "length": files[0][1], "piece length": PIECE, "pieces": pieces(payload[name])}
    else:
        blob = b"".join(payload[p] for p, _ in files)
        info = {"name": name, "piece length": PIECE, "pieces": pieces(blob),
                "files": [{"length": s, "path": p.split("/")} for p, s in files]}
    if private:
        info["private"] = 1
    meta = {"announce": tracker, "info": info, "created by": "housekeeparr-it"}
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
    def release(key, name, size, private=False, tracker=TRACKER, **ident):
        h, payload = make(out, name, [(name + ".mkv", size)], private, tracker)
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
    # Released only to the automatic-dispatch test.
    release("tv-e06", "Housekeeparr.Fixture.Show.S01E06.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=6)
    release("film-f", "Housekeeparr.Sixth.Fixture.Film.2016.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999007, imdbId="tt9990007")
    # Private torrents (info.private = 1), for the private-recovery tests.
    release("tv-e08-private", "Housekeeparr.Fixture.Show.S01E08.1080p.WEB-DL.x264-HKFIX", 4 * MB, private=True, kind="tv", tvdbId=999001, season=1, episode=8)
    release("film-h-private", "Housekeeparr.Eighth.Fixture.Film.2014.1080p.WEB-DL.x264-HKFIX", 4 * MB, private=True, kind="movie", tmdbId=999009, imdbId="tt9990009")
    # Announcing to the stand-in's rejecting tracker (HK_IT_REJECT_ANNOUNCE,
    # loopback, inside the namespace), for the preserved-data failure tests.
    reject = os.environ.get("HK_IT_REJECT_ANNOUNCE", TRACKER)
    release("tv-e09-unregistered", "Housekeeparr.Fixture.Show.S01E09.1080p.WEB-DL.x264-HKFIX", 4 * MB, tracker=reject, kind="tv", tvdbId=999001, season=1, episode=9)
    release("tv-e10-unregistered-private", "Housekeeparr.Fixture.Show.S01E10.1080p.WEB-DL.x264-HKFIX", 4 * MB, private=True, tracker=reject, kind="tv", tvdbId=999001, season=1, episode=10)
    release("film-j-unregistered-private", "Housekeeparr.Tenth.Fixture.Film.2012.1080p.WEB-DL.x264-HKFIX", 4 * MB, private=True, tracker=reject, kind="movie", tmdbId=999011, imdbId="tt9990011")
    # Searched for only by the proactive-search tests.
    release("tv-e11-search", "Housekeeparr.Fixture.Show.S01E11.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=1, episode=11)
    release("film-k-search", "Housekeeparr.Eleventh.Fixture.Film.2025.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="movie", tmdbId=999012, imdbId="tt9990012")
    # Released only to the automatic preserved-data test.
    release("tv-e12-unregistered-auto", "Housekeeparr.Fixture.Show.S01E12.1080p.WEB-DL.x264-HKFIX", 4 * MB, tracker=reject, kind="tv", tvdbId=999001, season=1, episode=12)
    release("film-i-unregistered", "Housekeeparr.Ninth.Fixture.Film.2013.1080p.WEB-DL.x264-HKFIX", 4 * MB, tracker=reject, kind="movie", tmdbId=999010, imdbId="tt9990010")
    # Multi-episode releases (one file covering two episodes of the
    # fictional season 3), for the multi-episode queue probe: both episodes
    # monitored; the second unmonitored; the first already has a library
    # file (library-seed below).
    release("tv-s03e01e02-multi", "Housekeeparr.Fixture.Show.S03E01E02.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=3, episode=1)
    release("tv-s03e03e04-multi", "Housekeeparr.Fixture.Show.S03E03E04.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=3, episode=3)
    release("tv-s03e05e06-multi", "Housekeeparr.Fixture.Show.S03E05E06.1080p.WEB-DL.x264-HKFIX", 4 * MB, kind="tv", tvdbId=999001, season=3, episode=6)
    seed = os.path.join(out, "library-seed")
    os.makedirs(seed, exist_ok=True)
    with open(os.path.join(seed, "Housekeeparr.Fixture.Show.S03E05.720p.HDTV.x264-HKFIX.mkv"), "wb") as f:
        f.write(mkv(25 * 60, 2 * MB))
    # A season pack with two of the season's three episodes, fully
    # downloaded (complete in the client) as playable-looking Matroska files,
    # for the partial-pack probe: Sonarr imports E01-E02 and E03 stays
    # expected.
    pname = "Housekeeparr.Fixture.Show.S02.1080p.WEB-DL.x264-HKFIX"
    pfiles = [("Housekeeparr.Fixture.Show.S02E%02d.1080p.WEB-DL.x264-HKFIX.mkv" % e, 2 * MB) for e in (1, 2)]
    contents = {p: mkv(25 * 60, s) for p, s in pfiles}
    pfiles = [(p, len(contents[p])) for p, _ in pfiles]  # declare the exact sizes
    h, payload = make(out, pname, pfiles, contents=contents)
    write(save, pname, payload, False)
    managed.append(dict(key="tv-s02-pack", name=pname, hash=h, size=4 * MB, files=[p for p, _ in pfiles], data="full",
                        managed=dict(kind="tv", tvdbId=999001, season=2, episode=0)))
    # Magnet-only releases for random hashes: no .torrent and no payload, so
    # their metadata can never arrive (the metadata-recovery tests).
    def magnet(key, name, **ident):
        managed.append(dict(key=key, name=name, hash=hashlib.sha1(os.urandom(32)).hexdigest(), size=4 * MB, files=[], data="none",
                            magnet=True, managed=ident))
    magnet("tv-e07-magnet", "Housekeeparr.Fixture.Show.S01E07.1080p.WEB-DL.x264-HKFIX", kind="tv", tvdbId=999001, season=1, episode=7)
    magnet("film-g-magnet", "Housekeeparr.Seventh.Fixture.Film.2015.1080p.WEB-DL.x264-HKFIX", kind="movie", tmdbId=999008, imdbId="tt9990008")
    with open(os.path.join(out, "managed.json"), "w") as f:
        json.dump(managed, f, indent=1)

if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
