#!/usr/bin/env python3
"""Tests for scripts/block-space-check.py: the space Svelte 5 drops after a block tag (occulited B-107).

    python3 scripts/test_block_space_check.py
"""

import importlib.util
import pathlib
import unittest

_spec = importlib.util.spec_from_file_location(
    "block_space_check", pathlib.Path(__file__).resolve().parent / "block-space-check.py"
)
bc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(bc)


class FindingsTest(unittest.TestCase):
    def test_flagged(self):
        for text in [
            "<p>{a}{#if b} <a href=x>y</a>{/if}</p>",  # B-106's shape
            "<td>{d}{#if by} · {by}{/if}</td>",  # the separator
            "{#if x} — {t('a')}{:else} — {t('b')}{/if}",  # line start, and the {:else}
            "<b>{n}{#if a}x{:else if b} <i>y</i>{/if}</b>",  # {:else if}
            "<p>{#if f({a: 1})} <i>z</i>{/if}</p>",  # braces inside the condition
            "<p>{#if s === '}'} <i>z</i>{/if}</p>",  # a brace in a string
        ]:
            with self.subTest(text=text):
                self.assertTrue(bc.findings(text), text)

    def test_not_flagged(self):
        for text in [
            "<p>{a}{#if b}{' '}<a href=x>y</a>{/if}</p>",
            "<td>{d}{#if by}{' · '}{by}{/if}</td>",
            "{#if x}\n    <p>y</p>\n{/if}",  # the content on its own lines
            "{#if x}   ",  # trailing blanks only
            "<p>{#each xs as x} {x}{/each}</p>",  # not an if block
            "<script>\n  const s = `{#if a} b`;\n</script>",  # script text
            "<style>\n  .a { color: red }\n</style>",
        ]:
            with self.subTest(text=text):
                self.assertEqual(bc.findings(text), [], text)

    def test_position(self):
        self.assertEqual(bc.findings("x\n<p>{a}{#if b} <i>c</i>{/if}</p>"), [(2, 7, "<p>{a}{#if b} <i>c</i>{/if}</p>")])


if __name__ == "__main__":
    unittest.main()
