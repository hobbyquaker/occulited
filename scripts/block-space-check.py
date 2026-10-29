#!/usr/bin/env python3
"""occulited B-107: Svelte 5 drops the whitespace at the start of an {#if}/{:else} block.

`text{#if x} <a>link</a>{/if}` renders "textlink": the space written after the block's tag is
trimmed (B-106: "ist verfügbar.Release-Notizen", B-107: a dozen more). Written as an expression,
`{#if x}{' '}<a>…`, it stays. svelte-check does not see it, so this check flags a block tag
({#if …}, {:else}, {:else if …}) followed on the same line by whitespace and then something to
render. A tag at the end of its line (the block's content on the next lines) is not flagged.

    python3 scripts/block-space-check.py [files...]   (default: every .svelte file under ui/src)
"""
import pathlib
import re
import sys

OPEN = re.compile(r'\{(#if\b|:else\b)')


def tag_end(line, i):
    """The index after the block tag starting at line[i] ('{'), braces and quotes balanced; -1 if
    the tag does not end on this line."""
    depth = 0
    quote = None
    j = i
    while j < len(line):
        c = line[j]
        if quote:
            if c == '\\':
                j += 2
                continue
            if c == quote:
                quote = None
        elif c in '\'"`':
            quote = c
        elif c == '{':
            depth += 1
        elif c == '}':
            depth -= 1
            if depth == 0:
                return j + 1
        j += 1
    return -1


def findings(text):
    """(line number, column, the line) of every block tag followed by trimmed whitespace."""
    out = []
    in_script = False
    for n, line in enumerate(text.split('\n'), 1):
        s = line.strip()
        if s.startswith('<script') or s.startswith('<style'):
            in_script = True
        if in_script:
            if '</script>' in line or '</style>' in line:
                in_script = False
            continue
        for m in OPEN.finditer(line):
            end = tag_end(line, m.start())
            if end < 0:
                continue
            rest = line[end:]
            if rest[:1] in (' ', '\t') and rest.strip() != '':
                out.append((n, m.start() + 1, line.strip()))
    return out


def main(argv):
    root = pathlib.Path(__file__).resolve().parent.parent
    files = [pathlib.Path(a) for a in argv] or sorted((root / 'ui' / 'src').rglob('*.svelte'))
    bad = 0
    for f in files:
        for n, col, line in findings(f.read_text(encoding='utf-8')):
            rel = f.relative_to(root) if f.is_absolute() and root in f.parents else f
            print(f'{rel}:{n}:{col}: whitespace after a block tag is dropped by Svelte - write it as {{\' \'}}: {line[:140]}')
            bad += 1
    if bad:
        print(f'{bad} block tag(s) lose their space (occulited B-107)')
        return 1
    print('no block tag loses its space (B-107)')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
