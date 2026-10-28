#!/usr/bin/env python3
"""Tests for scripts/catalog-resolved-publish.sh: the branch catalog-resolved is appended to, never force-pushed,
and only when the answers changed or the file nears its six hours (occulited task 124).

    python3 scripts/test_catalog_resolved_publish.py
"""

import datetime
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).resolve().parent / "catalog-resolved-publish.sh"
ENV = dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.org", GIT_COMMITTER_NAME="t",
           GIT_COMMITTER_EMAIL="t@example.org", GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1")


def git(repo, *args):
    return subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True, text=True,
                          env=ENV).stdout.strip()


def stamp(hours_ago):
    t = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=hours_ago)
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


class PublishTest(unittest.TestCase):
    def setUp(self):
        self.dir = pathlib.Path(tempfile.mkdtemp())
        self.remote = self.dir / "remote.git"
        git(self.dir, "init", "-q", "--bare", str(self.remote))
        # a force push to the branch would be refused, as GitHub's ruleset refuses it
        git(self.remote, "config", "receive.denyNonFastForwards", "true")

    def tearDown(self):
        shutil.rmtree(self.dir)

    def publish(self, body, **env):
        f = self.dir / "new.json"
        f.write_text(json.dumps(body))
        return subprocess.run(["sh", str(SCRIPT), str(f), str(self.remote)], capture_output=True, text=True,
                              env=dict(ENV, SOURCE_SHA="abc123", **env))

    def log(self):
        return git(self.remote, "log", "--format=%s", "catalog-resolved").split("\n")

    def published(self):
        return json.loads(git(self.remote, "show", "catalog-resolved:catalog-resolved.json"))

    def test_first_run_creates_the_branch(self):
        r = self.publish({"format": 1, "generated_at": stamp(0), "repos": {}})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.log(), ["catalog-resolved.json: the first file (from abc123)"])
        self.assertEqual(git(self.remote, "ls-tree", "--name-only", "catalog-resolved"), "catalog-resolved.json")

    def test_unchanged_answers_commit_nothing(self):
        self.publish({"format": 1, "generated_at": stamp(1), "repos": {"o/a": {"stars": 1}}})
        r = self.publish({"format": 1, "generated_at": stamp(0), "repos": {"o/a": {"stars": 1}}})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("unchanged", r.stdout)
        self.assertEqual(len(self.log()), 1)

    def test_changed_answers_append_a_commit(self):
        self.publish({"format": 1, "generated_at": stamp(1), "repos": {"o/a": {"stars": 1}}})
        r = self.publish({"format": 1, "generated_at": stamp(0), "repos": {"o/a": {"stars": 2}}})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.log()[0], "catalog-resolved.json: changed answers (from abc123)")
        self.assertEqual(len(self.log()), 2)
        self.assertEqual(self.published()["repos"]["o/a"]["stars"], 2)

    def test_an_old_file_is_re_stamped(self):
        self.publish({"format": 1, "generated_at": stamp(3.5), "repos": {}})
        r = self.publish({"format": 1, "generated_at": stamp(0), "repos": {}})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(self.log()[0].startswith("catalog-resolved.json: unchanged answers, re-stamped"))
        self.assertEqual(len(self.log()), 2)

    def test_an_unreachable_remote_fails_without_creating_anything(self):
        f = self.dir / "new.json"
        f.write_text("{}")
        r = subprocess.run(["sh", str(SCRIPT), str(f), str(self.dir / "missing.git")], capture_output=True,
                           text=True, env=ENV)
        self.assertNotEqual(r.returncode, 0)
        self.assertFalse((self.dir / "missing.git").exists())

    def test_no_file_fails(self):
        r = subprocess.run(["sh", str(SCRIPT), str(self.dir / "none.json"), str(self.remote)], capture_output=True,
                           text=True, env=ENV)
        self.assertNotEqual(r.returncode, 0)


if __name__ == "__main__":
    unittest.main()
