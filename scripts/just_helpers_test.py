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

"""Recipe helper tests with real files and no Git writes."""

from __future__ import annotations

import copy
import io
import json
import os
import re
import subprocess
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
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
                        stdout = "release\n"
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
                        "refs/heads/release",
                        "--message",
                        "Release 0.3.1",
                    ],
                )

    def test_release_rejects_other_branches_before_writing(self) -> None:
        notes = self.root.parent / "notes.md"
        notes.write_text(
            "<!-- release-version: 0.3.1 -->\nRelease tooling fixes.\n",
            encoding="utf-8",
        )
        original_server = self.server_path.read_bytes()
        for branch, shown in (("main\n", "main"), ("", "a detached HEAD")):
            with self.subTest(branch=branch):

                def git_run(command: list[str], branch: str = branch, **_: object):
                    stdout = ""
                    if command[1] == "tag" and "--list" in command:
                        stdout = "v0.2.1\nv0.3.0\n"
                    elif command[1] == "branch":
                        stdout = branch
                    return subprocess.CompletedProcess(command, 0, stdout, "")

                with (
                    patch.object(helpers, "run", side_effect=git_run) as run,
                    patch.object(
                        helpers,
                        "git_succeeds",
                        side_effect=lambda *args: args[0] != "rev-parse",
                    ),
                    patch("sys.stdout", new_callable=io.StringIO),
                    self.assertRaisesRegex(
                        SystemExit,
                        f"a release is cut on release, not on {re.escape(shown)}$",
                    ),
                ):
                    self.command("release", "patch", str(notes))

                self.assertEqual(self.server_path.read_bytes(), original_server)
                commands = [call.args[0] for call in run.call_args_list]
                self.assertEqual(commands, [["git", "branch", "--show-current"]])

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


class ResetServeTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.commit = "a" * 40
        self.release = self.root / "custom.db"
        self.database = Path(f"{self.release}.non-release-{self.commit}")
        self.config = self.root / "setup.json"
        self.config.write_text("{}", encoding="utf-8")
        self.release.write_bytes(b"release data")
        (self.root / helpers.officer_binary()).touch()
        for mock in (
            patch.object(helpers, "ROOT", self.root),
            patch.dict(
                os.environ, {"PIT_OFFICER_SQLITE_PATH": str(self.release)}, clear=True
            ),
        ):
            mock.start()
            self.addCleanup(mock.stop)
        self.timestamp = datetime(
            2026, 11, 13, 14, 15, 16, 123456, tzinfo=timezone(timedelta(hours=2))
        )
        self.backup = Path(f"{self.database}.backup-20261113T141516.123456+0200")

    def command(self, *arguments: str) -> None:
        args = helpers.build_parser().parse_args(arguments)
        args.func(args)

    def test_reset_moves_database_and_sidecars_before_launching_init(self) -> None:
        for dev in (False, True):
            with self.subTest(dev=dev):
                suffixes = ("", "-wal", "-shm", "-journal")
                for suffix in suffixes:
                    Path(f"{self.database}{suffix}").write_bytes(
                        f"contents{suffix}".encode()
                    )

                def run(
                    command: list[str],
                    *,
                    dev: bool = dev,
                    suffixes: tuple[str, ...] = suffixes,
                    **kwargs: object,
                ):
                    if command == ["git", "rev-parse", "HEAD"]:
                        return subprocess.CompletedProcess(
                            command, 0, self.commit + "\n", ""
                        )
                    self.assertEqual(command[1:], ["serve", "-init", str(self.config)])
                    for suffix in suffixes:
                        self.assertFalse(Path(f"{self.database}{suffix}").exists())
                        backup = Path(f"{self.backup}{suffix}")
                        self.assertEqual(
                            backup.read_bytes(), f"contents{suffix}".encode()
                        )
                    if dev:
                        self.assertEqual(
                            kwargs["env"]["OPENPIT_RUNTIME_LIBRARY_PATH"],
                            str(self.root / "runtime"),
                        )
                    return subprocess.CompletedProcess(command, 0)

                with (
                    patch.object(helpers, "datetime") as clock,
                    patch.object(helpers, "run", side_effect=run) as launch,
                    patch.object(
                        helpers,
                        "local_runtime_library",
                        return_value=self.root / "runtime",
                    ),
                ):
                    clock.now.return_value.astimezone.return_value = self.timestamp
                    arguments = (
                        [
                            "run-officer-dev",
                            "--reset-init",
                            str(self.config),
                            "debug",
                            "../pit",
                            "serve",
                        ]
                        if dev
                        else ["run-officer", "--reset-init", str(self.config), "serve"]
                    )
                    self.command(*arguments)
                    self.assertEqual(launch.call_count, 2)
                self.assertEqual(self.release.read_bytes(), b"release data")
                for suffix in suffixes:
                    Path(f"{self.backup}{suffix}").unlink()

    def test_missing_database_still_starts_init(self) -> None:
        with patch.object(helpers, "run") as run:
            run.return_value.stdout = self.commit
            self.command("run-officer", "--reset-init", str(self.config), "serve")
            self.assertEqual(
                run.call_args.args[0][1:], ["serve", "-init", str(self.config)]
            )
        self.assertEqual(list(self.root.glob("*.backup-*")), [])

    def test_backup_collision_preserves_both_files_and_does_not_launch(self) -> None:
        self.database.write_bytes(b"current database")
        self.backup.write_bytes(b"previous backup")
        with (
            patch.object(helpers, "datetime") as clock,
            patch.object(helpers, "run") as run,
        ):
            clock.now.return_value.astimezone.return_value = self.timestamp
            run.return_value.stdout = self.commit
            with self.assertRaisesRegex(
                SystemExit, "backup destination already exists"
            ):
                self.command("run-officer", "--reset-init", str(self.config), "serve")
            self.assertEqual(run.call_count, 1)
        self.assertEqual(self.database.read_bytes(), b"current database")
        self.assertEqual(self.backup.read_bytes(), b"previous backup")

    def test_runtime_state_refuses_reset_before_moving_anything(self) -> None:
        self.database.write_bytes(b"current database")
        (self.root / "officer-runtime.non-release.json").write_text(
            "{}", encoding="utf-8"
        )
        with patch.object(helpers, "run") as run:
            with self.assertRaisesRegex(SystemExit, "stop Officer"):
                self.command("run-officer", "--reset-init", str(self.config), "serve")
            run.assert_not_called()
        self.assertEqual(self.database.read_bytes(), b"current database")

    def test_missing_config_refuses_reset_before_moving_anything(self) -> None:
        self.database.write_bytes(b"current database")
        self.config.unlink()
        with patch.object(helpers, "run") as run:
            with self.assertRaisesRegex(SystemExit, "initialization file not found"):
                self.command("run-officer", "--reset-init", str(self.config), "serve")
            run.assert_not_called()
        self.assertEqual(self.database.read_bytes(), b"current database")


if __name__ == "__main__":
    unittest.main()
