import subprocess
import unittest

import git_gate


class TestParseArgs(unittest.TestCase):

    def test_project_only(self):
        args = git_gate.parse_args(["--project", "git.testing/project"])
        self.assertEqual(args.project, "git.testing/project")

    def test_project_and_ref(self):
        args = git_gate.parse_args(
            ["--project", "git.testing/project",
             "--project-ref", "v1"])
        self.assertEqual(args.project, "git.testing/project")
        self.assertEqual(args.project_ref, "v1")

    def test_merging_other(self):
        args = git_gate.parse_args(
            ["--project", "git.testing/project",
             "--merge-url", "git.testing/proposed"])
        self.assertEqual(args.project, "git.testing/project")
        self.assertEqual(args.merge_url, "git.testing/proposed")
        self.assertEqual(args.merge_ref, "HEAD")

    def test_merging_other_ref(self):
        args = git_gate.parse_args(
            ["--project", "git.testing/project",
             "--merge-url", "git.testing/proposed", "--merge-ref", "feature"])
        self.assertEqual(args.project, "git.testing/project")
        self.assertEqual(args.merge_url, "git.testing/proposed")
        self.assertEqual(args.merge_ref, "feature")

    def test_project_with_deps(self):
        args = git_gate.parse_args(
            ["--project", "git.testing/project",
             "-d", "git.testing/a", "-d", "git.testing/b"])
        self.assertEqual(args.project, "git.testing/project")
        self.assertEqual(args.dependency, ["git.testing/a", "git.testing/b"])


class TestSubcommandError(unittest.TestCase):

    def test_subcommand_error(self):
        proc_error = subprocess.CalledProcessError(1, ["git"])
        err = git_gate.SubcommandError("git", "clone", proc_error)
        self.assertEqual(str(err), "Subprocess git clone failed with code 1")
