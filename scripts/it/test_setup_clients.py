import unittest
from unittest.mock import patch

import setup_clients


class DelugeFixtureReadinessTest(unittest.TestCase):
    def test_unverified_full_payload_stops_setup(self):
        fixture = {"key": "complete", "hash": "fixture-hash", "name": "fixture", "data": "full"}

        def rpc(method, *_args):
            if method == "system.listMethods":
                return []
            if method == "core.get_torrent_status":
                return {"state": "Paused"}

        with (
            patch.object(setup_clients, "deluge_connect") as connect,
            patch.object(setup_clients, "load", return_value=[fixture]),
            patch.object(setup_clients, "metainfo", return_value=b"fixture"),
            patch.object(setup_clients.time, "sleep"),
            patch.object(setup_clients.time, "time", side_effect=[1, 2, 32]),
            patch.object(setup_clients.time, "monotonic", side_effect=[1, 32]),
        ):
            connect.return_value.call.side_effect = rpc
            with self.assertRaisesRegex(RuntimeError, r"complete.*Paused"):
                setup_clients.deluge("http://127.0.0.1", "p", 0, "u", "p", "", "", "")


if __name__ == "__main__":
    unittest.main()
