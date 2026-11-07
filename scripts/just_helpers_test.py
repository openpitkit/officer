# Copyright The Pit Project Owners. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Please see https://officer.openpit.dev and the OWNERS file for details.

"""Release tooling tests with real files and no Git writes."""

from __future__ import annotations

import copy
import io
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import just_helpers as helpers


class ReleaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.original = json.loads(
            (helpers.ROOT / "server.json").read_text(encoding="utf-8")
        )
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "checkout"
        self.root.mkdir()
        root_patch = patch.object(helpers, "ROOT", self.root)
        root_patch.start()
        self.addCleanup(root_patch.stop)
        self.server_path = self.root / "server.json"
        self.write_server(self.original)

    def write_server(self, server: dict) -> None:
        self.server_path.write_text(json.dumps(server) + "\n", encoding="utf-8")

    def command(self, *arguments: str) -> None:
        args = helpers.build_parser().parse_args(arguments)
        args.func(args)

    def test_release_bumps_metadata_in_the_notes_commit(self) -> None:
        notes = self.root.parent / "notes.md"
        notes.write_text(
            "<!-- release-version: 0.3.1 -->\nRelease tooling fixes.\n",
            encoding="utf-8",
        )
        for packages in (
            None,
            [],
            [
                {"identifier": "first", "version": "0.1.0"},
                {"identifier": "second", "version": "0.2.0"},
            ],
        ):
            with self.subTest(packages=packages):
                server = copy.deepcopy(self.original)
                if packages is not None:
                    server["packages"] = packages
                self.write_server(server)

                def git_run(command: list[str], **_: object):
                    stdout = ""
                    if command[1] == "tag" and "--list" in command:
                        stdout = "v0.2.1\nv0.3.0\n"
                    elif command[1] == "branch":
                        stdout = "main\n"
                    elif command[1] == "commit":
                        helpers.check_release_version("0.3.1")
                        self.assertEqual(
                            (self.root / helpers.RELEASE_NOTES).read_bytes(),
                            notes.read_bytes(),
                        )
                    return subprocess.CompletedProcess(command, 0, stdout, "")

                with (
                    patch.object(helpers, "run", side_effect=git_run) as run,
                    patch.object(
                        helpers,
                        "git_succeeds",
                        side_effect=lambda *args: args[0] != "rev-parse",
                    ),
                    patch("sys.stdout", new_callable=io.StringIO),
                ):
                    self.command("release", "patch", str(notes))

                expected = copy.deepcopy(server)
                expected["version"] = "0.3.1"
                if "packages" in expected:
                    for package in expected["packages"]:
                        package["version"] = "0.3.1"
                self.assertEqual(json.loads(self.server_path.read_text()), expected)
                commands = [call.args[0] for call in run.call_args_list]
                self.assertIn(
                    ["git", "add", "--", helpers.RELEASE_NOTES, "server.json"],
                    commands,
                )
                self.assertEqual(
                    [command for command in commands if command[1] == "commit"],
                    [["git", "commit", "--message", "Release 0.3.1"]],
                )
                self.assertEqual(
                    commands[-1],
                    [
                        "git",
                        "tag",
                        "--annotate",
                        "v0.3.1",
                        "--message",
                        "Release 0.3.1",
                    ],
                )

    def test_version_check_accepts_the_repository_manifest(self) -> None:
        self.command("check-release-version", self.original["version"])

    def test_version_check_rejects_server_drift(self) -> None:
        server = copy.deepcopy(self.original)
        server["version"] = "0.1.0"
        self.write_server(server)
        with self.assertRaisesRegex(SystemExit, "server.json version.*0.3.0"):
            self.command("check-release-version", "0.3.0")

    def test_version_check_rejects_each_package_drift(self) -> None:
        for index in range(2):
            with self.subTest(index=index):
                server = copy.deepcopy(self.original)
                server["version"] = "0.3.0-rc.1"
                server["packages"] = [{"version": "0.3.0-rc.1"} for _ in range(2)]
                server["packages"][index]["version"] = "0.2.1"
                self.write_server(server)
                with self.assertRaisesRegex(SystemExit, rf"packages\[{index}\]"):
                    self.command("check-release-version", "0.3.0-rc.1")

    def test_publication_gate_rejects_drift_with_valid_notes(self) -> None:
        notes = self.root / helpers.RELEASE_NOTES
        notes.write_text(
            "<!-- release-version: 0.3.0 -->\nRelease tooling fixes.\n",
            encoding="utf-8",
        )
        server = copy.deepcopy(self.original)
        server["version"] = "0.2.1"
        self.write_server(server)
        with patch.object(helpers, "git_succeeds", return_value=True):
            with self.assertRaisesRegex(SystemExit, "server.json version.*0.3.0"):
                self.command("check-release-notes", "0.3.0")
            server["version"] = "0.3.0"
            self.write_server(server)
            self.command("check-release-notes", "0.3.0")


if __name__ == "__main__":
    unittest.main()
