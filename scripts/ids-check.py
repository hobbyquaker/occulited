#!/usr/bin/env python3
"""No real device identifiers in the repository (B-24).

occulited is public, and its tests, fixtures, the UI stub and the docs need device identifiers
that look real enough for the parsers. They must be invented: a real SGTIN, serial or MAC names a
device somebody owns. This check finds every value of the shapes below in the tracked files and
fails on any that is not listed in scripts/ids-allow.txt, the list of the invented ones.

  - SGTINs: 24 hex digits beginning with 30 (3014F711A0…, 30150377DC…), also in groups of four
    as the UI writes them (3014-F711-A000-…), listed without the dashes
  - HmIP device addresses: 14 hex digits beginning with 00
  - BidCos serials: three letters ending in EQ, seven digits (JEQ0000001)
  - MAC addresses: six colon-separated hex pairs (a longer run such as a certificate
    fingerprint is not one)
  - radio addresses: 0x and six hex digits, BidCos-RF's and HmIP's alike (0xFF1234), and the
    six digits hmip_address.conf writes without the 0x (Adapter.1.Address=FF1234); listed as
    0x and the digits. In Go source a numeric literal in the code (a mask, a range bound) is
    not one: only Go's strings and comments are searched for this shape (B-38)

A new test value goes into ids-allow.txt in the same commit, and it must be an invented one:
runs of zeros, counting digits, a letter pattern nobody reads as a real device's.

    python3 scripts/ids-check.py            # check the tracked files
    python3 scripts/ids-check.py --list     # print every value found, one per line, sorted
"""

import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
ALLOW = pathlib.Path(__file__).resolve().parent / "ids-allow.txt"

RADIO = "radio address"

SHAPES = [
    ("SGTIN", re.compile(r"(?<![0-9A-Za-z])30[0-9A-Fa-f]{22}(?![0-9A-Za-z])")),
    ("SGTIN", re.compile(r"(?<![0-9A-Za-z-])30[0-9A-Fa-f]{2}(?:-[0-9A-Fa-f]{4}){5}(?![0-9A-Za-z-])")),
    ("HmIP address", re.compile(r"(?<![0-9A-Za-z])00[0-9A-Fa-f]{12}(?![0-9A-Za-z])")),
    ("BidCos serial", re.compile(r"(?<![0-9A-Za-z])[A-Z]EQ[0-9]{7}(?![0-9])")),
    ("MAC", re.compile(r"(?<![0-9A-Fa-f:])(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}(?![0-9A-Fa-f:])")),
    (RADIO, re.compile(r"(?<![0-9A-Za-z])0[xX]([0-9A-Fa-f]{6})(?![0-9A-Za-z])")),
    (RADIO, re.compile(r"(?<![0-9A-Za-z])Adapter\.[0-9]+\.Address\s*=\s*([0-9A-Fa-f]{6})(?![0-9A-Za-z])", re.I)),
]

# generated or third-party content: npm's lock files carry integrity hashes, nothing of ours
SKIP = re.compile(r"(^|/)package-lock\.json$")


def norm(value):
    """The allow-list's spelling: upper case, a radio address as 0x and six upper-case digits."""
    value = value.upper()
    return "0x" + value[2:] if value.startswith("0X") else value


def find_at(text):
    """Every (offset, shape, value) in text, values normalised (norm) so the allow-list is case-free."""
    out = []
    for shape, rx in SHAPES:
        for m in rx.finditer(text):
            if shape == RADIO:
                value = "0x" + m.group(1).upper()
            elif shape == "SGTIN":
                value = m.group(0).replace("-", "").upper()
            else:
                value = m.group(0).upper()
            out.append((m.start(), shape, value))
    return sorted(out, key=lambda o: o[0])


def find(text):
    """Every (shape, value) in text, values normalised (norm) so the allow-list is case-free."""
    return [(shape, value) for _, shape, value in find_at(text)]


def go_code(text):
    """For Go source: a list of booleans, True where the character is code, False inside a string,
    a rune literal or a comment. A radio-address shape in the code is a numeric literal (a mask,
    a range bound), not an identifier; in a string or a comment it can be one."""
    mask = [True] * len(text)
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        two = text[i:i + 2]
        if two == "//":
            j = text.find("\n", i)
            j = n if j < 0 else j
        elif two == "/*":
            j = text.find("*/", i + 2)
            j = n if j < 0 else j + 2
        elif c == "`":
            j = text.find("`", i + 1)
            j = n if j < 0 else j + 1
        elif c in "\"'":
            j = i + 1
            while j < n and text[j] != c and text[j] != "\n":
                j += 2 if text[j] == "\\" else 1
            j = min(j + 1, n)
        else:
            i += 1
            continue
        for k in range(i, j):
            mask[k] = False
        i = j
    return mask


def load_allow(path=ALLOW):
    allow = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            allow.add(norm(line))
    return allow


def tracked():
    out = subprocess.run(["git", "-C", str(ROOT), "ls-files", "-z"], capture_output=True, check=True).stdout
    return [p for p in out.decode().split("\0") if p and not SKIP.search(p)]


def scan(files):
    """{value: (shape, [file:line, …])} over the files, which are read as UTF-8 where they can be."""
    seen = {}
    for f in files:
        p = ROOT / f
        try:
            text = p.read_text(encoding="utf-8")
        except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
            continue
        code = go_code(text) if f.endswith(".go") else None
        for at, shape, value in find_at(text):
            if shape == RADIO and code is not None and code[at]:
                continue
            seen.setdefault(value, (shape, []))[1].append(f"{f}:{text.count(chr(10), 0, at) + 1}")
    return seen


def main(argv):
    seen = scan(tracked())
    if "--list" in argv:
        for value in sorted(seen):
            print(value)
        return 0
    allow = load_allow()
    bad = {v: s for v, s in seen.items() if v not in allow}
    for value, (shape, where) in sorted(bad.items()):
        print(f"{shape} {value} is not in scripts/ids-allow.txt: {', '.join(where[:5])}")
    if bad:
        print(
            f"\n{len(bad)} identifier(s) not on the allow-list. Real device identifiers do not belong in "
            "this repository; replace them with invented ones and list those in scripts/ids-allow.txt.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
