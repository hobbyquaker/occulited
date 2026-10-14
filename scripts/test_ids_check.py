#!/usr/bin/env python3
"""Tests for scripts/ids-check.py: the identifier shapes and the allow-list (B-24).

    python3 scripts/test_ids_check.py
"""

import importlib.util
import pathlib
import tempfile
import unittest

_spec = importlib.util.spec_from_file_location("ids_check", pathlib.Path(__file__).resolve().parent / "ids-check.py")
ic = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ic)


class ShapeTest(unittest.TestCase):
    def test_find(self):
        for text, want in [
            ('serial: "3014F711A0000A1B2C3D4E5F"', [("SGTIN", "3014F711A0000A1B2C3D4E5F")]),
            ("sgtin=3014f711a0000a1b2c3d4e5f", [("SGTIN", "3014F711A0000A1B2C3D4E5F")]),
            ("Key stored for 3014-F711-A000-0A1B-2C3D-4E5F.", [("SGTIN", "3014F711A0000A1B2C3D4E5F")]),
            ("HmIP-RF.000A1B2C3D4E5F:4", [("HmIP address", "000A1B2C3D4E5F")]),
            ("BidCos-RF.NEQ0000001:1", [("BidCos serial", "NEQ0000001")]),
            ("rfd/NEQ0000001.dev", [("BidCos serial", "NEQ0000001")]),
            ("mac aa:bb:cc:dd:ee:ff up", [("MAC", "AA:BB:CC:DD:EE:FF")]),
            # a certificate fingerprint is a longer run of pairs, not a MAC
            ("96:BC:EC:06:26:49:11:22:33:44", []),
            # a longer hex run is no SGTIN, and a timestamp no address
            ("3014F711A0000A1B2C3D4E5F00", []),
            ("20260829155415", []),
            ("ABC0000001 NEQ00000011", []),
            # radio addresses (B-38): 0x and six digits, and hmip_address.conf's form without the 0x
            ("HM_HMRF_ADDRESS='0xff1234'", [("radio address", "0xFF1234")]),
            ("Adapter.1.Address=FF1234", [("radio address", "0xFF1234")]),
            ("adapter.address=0xFF1234", [("radio address", "0xFF1234")]),
            # shorter or longer hex runs are no address: an oemid, an SD serial, a MAC-less id
            ("oemid 0x5344 serial 0x82823176 0xFF12345", []),
        ]:
            with self.subTest(text=text):
                self.assertEqual(ic.find(text), want)

    def test_allow_list_is_case_free_and_skips_comments(self):
        # a directory of its own that goes away: a NamedTemporaryFile(delete=False) stayed behind (B-42)
        with tempfile.TemporaryDirectory() as d:
            f = pathlib.Path(d, "allow.txt")
            f.write_text("# a comment\n3014f711a0000a1b2c3d4e5f  # trailing\n\nNEQ0000001\n0Xff1234\n")
            allow = ic.load_allow(f)
        self.assertEqual(allow, {"3014F711A0000A1B2C3D4E5F", "NEQ0000001", "0xFF1234"})

    def test_go_numeric_literals_are_no_radio_address(self):
        # in Go only strings and comments are searched for the radio-address shape: a mask or a
        # range bound in the code is a number; the other shapes are searched everywhere
        a, b, c, d = "0x" + "C0FFEE", "0x" + "BEEF01", "0x" + "D00D02", "0x" + "FACE03"
        src = (f'const mask = {a} // the range ends at {b}\n'
               f'var s = "rf {c}" + `raw {d}`\n'
               f"var r = '\\'' + \"esc \\\" {a}\"\n/* block {d}\n */ x := {c}\n")
        with tempfile.TemporaryDirectory() as root:
            pathlib.Path(root, "x.go").write_text(src)
            pathlib.Path(root, "x.md").write_text(f"constant {a}\n")
            old_root = ic.ROOT
            ic.ROOT = pathlib.Path(root)
            try:
                seen = ic.scan(["x.go", "x.md"])
            finally:
                ic.ROOT = old_root
        got = {v: sorted(w) for v, (_, w) in seen.items()}
        self.assertEqual(got, {
            b: ["x.go:1"],
            c: ["x.go:2"],
            d: ["x.go:2", "x.go:4"],
            a: ["x.go:3", "x.md:1"],
        })

    def test_the_tree_is_clean(self):
        # the real check, as CI runs it: every identifier in the tracked files is on the list
        allow = ic.load_allow()
        bad = {v for v in ic.scan(ic.tracked()) if v not in allow}
        self.assertEqual(bad, set())

    def test_a_real_looking_value_is_refused(self):
        allow = ic.load_allow()
        # built from parts, so this file itself does not carry them for the tree check to find
        for value in ["3014F711A0000" + "FEDCBA98765", "3014-F711-A000-0" + "FED-CBA9-8765", "KEQ" + "7654321", "00:1A:22" + ":98:76:54",
                      "0x" + "7C3E91"]:
            with self.subTest(value=value):
                found = [v for _, v in ic.find(value)]
                self.assertEqual(found, [value.replace("-", "")])
                self.assertNotIn(value.replace("-", ""), allow)

    def test_an_unknown_radio_address_in_a_fixture_fails(self):
        # the B-38 case: a real network address in the UI stub or a test's string
        with tempfile.TemporaryDirectory() as root:
            pathlib.Path(root, "server.mjs").write_text("address: '0x" + "7C3E91',\n")
            old_root = ic.ROOT
            ic.ROOT = pathlib.Path(root)
            try:
                seen = ic.scan(["server.mjs"])
            finally:
                ic.ROOT = old_root
        self.assertEqual({v: s for v, (s, _) in seen.items()}, {"0x" + "7C3E91": "radio address"})
        self.assertNotIn("0x" + "7C3E91", ic.load_allow())


if __name__ == "__main__":
    unittest.main()
