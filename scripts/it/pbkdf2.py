#!/usr/bin/env python3
"""Print a qBittorrent WebUI\\Password_PBKDF2 value for a password (stdin)."""
import base64, hashlib, os, sys
pw = sys.stdin.read().rstrip("\n").encode()
salt = os.urandom(16)
key = hashlib.pbkdf2_hmac("sha512", pw, salt, 100000, 64)
print('"@ByteArray(%s:%s)"' % (base64.b64encode(salt).decode(), base64.b64encode(key).decode()))
