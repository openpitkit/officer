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

import contextlib
import copy
import io
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from collections.abc import Iterator
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import Mock, patch

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


class SemgrepScanTests(unittest.TestCase):
    VALID_RULE = """rules:
  - id: valid-rule
    pattern: fmt.Println(...)
    message: no printing
    languages: [go]
    severity: ERROR
"""
    BROKEN_RULE = """rules:
  - id: broken-rule
    pattern: "func $X( {"
    message: this pattern does not parse
    languages: [go]
    severity: ERROR
"""

    def setUp(self) -> None:
        bin_dir = (
            helpers.ROOT / ".venv" / ("Scripts" if helpers.is_windows() else "bin")
        )
        self.semgrep = bin_dir / ("semgrep.exe" if helpers.is_windows() else "semgrep")
        if not self.semgrep.is_file():
            self.fail(
                f"semgrep is not installed at {self.semgrep}; "
                "run just install-python-tools"
            )
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.rules = Path(temporary.name) / "rules"
        self.rules.mkdir()
        (self.rules / "valid.yaml").write_text(self.VALID_RULE, encoding="utf-8")
        self.target = Path(temporary.name) / "target.go"
        self.target.write_text("package target\n", encoding="utf-8")

    def scan(self) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                str(helpers.ROOT / "scripts" / "just_helpers.py"),
                "semgrep-scan",
                str(self.semgrep),
                "--config",
                f"{self.rules}/",
                "--error",
                "--timeout",
                "0",
                "--metrics=off",
                "--disable-version-check",
                str(self.target),
            ],
            cwd=helpers.ROOT,
            text=True,
            capture_output=True,
            timeout=300,
        )

    def test_valid_rules_pass(self) -> None:
        result = self.scan()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_a_rule_that_does_not_parse_fails_the_scan_and_is_named(self) -> None:
        (self.rules / "broken.yaml").write_text(self.BROKEN_RULE, encoding="utf-8")
        result = self.scan()
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("broken-rule", result.stdout + result.stderr)


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

    def test_release_runtime_state_next_to_the_default_path_does_not_block_reset(
        self,
    ) -> None:
        # The per-commit file is opened only by a non-release build, so the
        # state file of a running release build does not guard it.
        self.database.write_bytes(b"per-commit data")
        (self.root / "officer-runtime.json").write_text("{}", encoding="utf-8")

        def run(command: list[str], **_: object):
            if command == ["git", "rev-parse", "HEAD"]:
                return subprocess.CompletedProcess(command, 0, self.commit + "\n", "")
            self.assertEqual(command[1:], ["serve", "-init", str(self.config)])
            self.assertFalse(self.database.exists())
            self.assertEqual(self.backup.read_bytes(), b"per-commit data")
            return subprocess.CompletedProcess(command, 0)

        with (
            patch.object(helpers, "datetime") as clock,
            patch.object(helpers, "run", side_effect=run) as launch,
        ):
            clock.now.return_value.astimezone.return_value = self.timestamp
            self.command("run-officer", "--reset-init", str(self.config), "serve")
            self.assertEqual(launch.call_count, 2)
        self.assertEqual(self.release.read_bytes(), b"release data")

    def test_missing_config_refuses_reset_before_moving_anything(self) -> None:
        self.database.write_bytes(b"current database")
        self.config.unlink()
        with patch.object(helpers, "run") as run:
            with self.assertRaisesRegex(SystemExit, "initialization file not found"):
                self.command("run-officer", "--reset-init", str(self.config), "serve")
            run.assert_not_called()
        self.assertEqual(self.database.read_bytes(), b"current database")

    def test_explicit_database_path_resets_that_file_and_serves_it(self) -> None:
        chosen = self.root / "chosen.db"
        suffixes = ("", "-wal", "-shm", "-journal")
        backup = Path(f"{chosen}.backup-20261113T141516.123456+0200")
        for dev in (False, True):
            with self.subTest(dev=dev):
                self.database.write_bytes(b"per-commit data")
                for suffix in suffixes:
                    Path(f"{chosen}{suffix}").write_bytes(f"chosen{suffix}".encode())

                def run(
                    command: list[str],
                    *,
                    suffixes: tuple[str, ...] = suffixes,
                    **_: object,
                ):
                    self.assertEqual(
                        command,
                        [
                            str(self.root / helpers.officer_binary()),
                            "serve",
                            "-sqlite-path",
                            "chosen.db",
                            "-init",
                            str(self.config),
                        ],
                    )
                    for suffix in suffixes:
                        self.assertFalse(Path(f"{chosen}{suffix}").exists())
                        self.assertEqual(
                            Path(f"{backup}{suffix}").read_bytes(),
                            f"chosen{suffix}".encode(),
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
                            "-sqlite-path",
                            "chosen.db",
                        ]
                        if dev
                        else [
                            "run-officer",
                            "--reset-init",
                            str(self.config),
                            "serve",
                            "-sqlite-path",
                            "chosen.db",
                        ]
                    )
                    self.command(*arguments)
                    # Only the launch: no commit is looked up for an explicit path.
                    self.assertEqual(launch.call_count, 1)
                self.assertEqual(self.database.read_bytes(), b"per-commit data")
                self.assertEqual(self.release.read_bytes(), b"release data")
                for suffix in suffixes:
                    Path(f"{backup}{suffix}").unlink()

    def test_runtime_state_next_to_explicit_path_refuses_reset(self) -> None:
        # A release build writes officer-runtime.json, any other build the
        # non-release name; either one means an Officer has the file open.
        for state_name in ("officer-runtime.json", "officer-runtime.non-release.json"):
            with self.subTest(state=state_name):
                directory = self.root / f"chosen-{state_name}"
                directory.mkdir()
                chosen = directory / "officer.db"
                chosen.write_bytes(b"chosen data")
                (directory / state_name).write_text("{}", encoding="utf-8")
                with patch.object(helpers, "run") as run:
                    with self.assertRaisesRegex(SystemExit, "stop Officer"):
                        self.command(
                            "run-officer",
                            "--reset-init",
                            str(self.config),
                            "serve",
                            "-sqlite-path",
                            f"chosen-{state_name}/officer.db",
                        )
                    run.assert_not_called()
                self.assertEqual(chosen.read_bytes(), b"chosen data")
                self.assertEqual(list(directory.glob("*.backup-*")), [])

    def test_unsupported_serve_arguments_are_refused_before_moving_anything(
        self,
    ) -> None:
        self.database.write_bytes(b"current database")
        for serve_args in (
            ["migrate"],
            ["serve", "-x"],
            ["serve", "-sqlite-path"],
            ["serve", "-sqlite-path", ""],
            ["serve", "-sqlite-path=chosen.db"],
            ["serve", "-sqlite-path", "chosen.db", "extra"],
            ["serve", "extra", "-sqlite-path", "chosen.db"],
        ):
            with self.subTest(serve_args=serve_args):
                with patch.object(helpers, "run") as run:
                    with self.assertRaisesRegex(
                        SystemExit, r"serve \[-sqlite-path PATH\]"
                    ):
                        self.command(
                            "run-officer", "--reset-init", str(self.config), *serve_args
                        )
                    run.assert_not_called()
                self.assertEqual(self.database.read_bytes(), b"current database")
                self.assertEqual(list(self.root.glob("*.backup-*")), [])


class ServeRecipeTests(unittest.TestCase):
    def test_every_serve_recipe_forwards_sqlite_path_to_the_launcher(self) -> None:
        just = shutil.which("just")
        if just is None:
            self.fail("just is not on PATH; it is required to check the recipes")
        justfile = str(helpers.ROOT / "justfile")
        summary = subprocess.run(
            [just, "--justfile", justfile, "--summary"],
            cwd=helpers.ROOT,
            text=True,
            capture_output=True,
            check=True,
        )
        recipes = [
            name for name in summary.stdout.split() if name.startswith("run-serve")
        ]
        self.assertTrue(recipes, "no run-serve recipes found")
        database = "/path with space/officer.db"
        for recipe in recipes:
            with self.subTest(recipe=recipe):
                dry_run = subprocess.run(
                    [
                        just,
                        "--justfile",
                        justfile,
                        "--dry-run",
                        f"sqlite_path={database}",
                        recipe,
                    ],
                    cwd=helpers.ROOT,
                    text=True,
                    capture_output=True,
                    check=True,
                )
                launches = [
                    shlex.split(line)
                    for line in dry_run.stderr.splitlines()
                    if re.search(r"\brun-officer(-dev)?\b", line)
                ]
                self.assertEqual(len(launches), 1, dry_run.stderr)
                self.assertEqual(launches[0][-3:], ["serve", "-sqlite-path", database])
                self.assertEqual("--reset-init" in launches[0], "reset" in recipe)


class RecipeTestCase(unittest.TestCase):
    def dump(self) -> dict[str, dict[str, object]]:
        just = shutil.which("just")
        if just is None:
            self.fail("just is not on PATH; it is required to check the recipes")
        # pipeline.just imports the justfile, so its dump holds the CI recipes too.
        dump = subprocess.run(
            [
                just,
                "--justfile",
                str(helpers.ROOT / "pipeline.just"),
                "--dump",
                "--dump-format",
                "json",
            ],
            cwd=helpers.ROOT,
            text=True,
            capture_output=True,
            check=True,
        )
        return json.loads(dump.stdout)

    def recipes(self) -> dict[str, dict[str, object]]:
        return self.dump()["recipes"]

    def reachable(self, recipes: dict[str, dict[str, object]], *roots: str) -> set[str]:
        seen: set[str] = set()
        pending = list(roots)
        while pending:
            current = pending.pop()
            if current in seen:
                continue
            seen.add(current)
            pending.extend(
                dependency["recipe"] for dependency in recipes[current]["dependencies"]
            )
        return seen

    def body_text(self, recipe: dict[str, object]) -> str:
        return "\n".join(
            "".join(part for part in line if isinstance(part, str))
            for line in recipe["body"]
        )

    def body_commands(self, recipe: dict[str, object]) -> Iterator[list]:
        for line in recipe["body"]:
            expressions = {}
            text = ""
            for part in line:
                if isinstance(part, str):
                    text += part
                else:
                    # Keep each Just expression intact while splitting shell words.
                    token = f"\0{len(expressions)}\0"
                    (expression,) = part
                    expressions[token] = expression
                    text += token

            lexer = shlex.shlex(text, posix=True, punctuation_chars=";&|")
            lexer.whitespace_split = True
            try:
                words = list(lexer)
            except ValueError as error:
                raise ValueError(f"{recipe['name']}: {error}") from error
            command = []
            for word in words:
                if word and not word.strip(";&|"):
                    if command:
                        yield command
                        command = []
                else:
                    if not command:
                        word = word.lstrip("@-")
                    command.append(expressions.get(word, word))
            if command:
                yield command

    def just_call(self, command: list) -> bool:
        return command[0] in ("just", ["call", "just_executable"])

    def variables(self, expression: object) -> set[str]:
        # The JSON dump nests a variable as ["variable", name] inside the
        # fragments of a body line, the arguments of a call and an assignment.
        found: set[str] = set()
        pending = [expression]
        while pending:
            node = pending.pop()
            if isinstance(node, list):
                if len(node) == 2 and node[0] == "variable":
                    found.add(node[1])
                else:
                    pending.extend(node)
        return found


class FrontendRecipeTests(RecipeTestCase):
    # A whole word: the helper subcommand ensure-node is not the node binary.
    FRONTEND_TOOLCHAIN = re.compile(r"(?<![\w-])(?:npm|npx|node)(?![\w-])")
    INSTALL_RECIPE = "frontend-ci-install"
    # npm 11.19.1 docs: configuring-npm/package-lock-json.md and commands/npm-*.md.
    LOCKFILE_WRITING_SUBCOMMANDS = {
        "add",
        "ddp",
        "dedupe",
        "i",
        "in",
        "ins",
        "inst",
        "insta",
        "instal",
        "install",
        "install-test",
        "isnt",
        "isnta",
        "isntal",
        "isntall",
        "it",
        "link",
        "ln",
        "prune",
        "r",
        "remove",
        "rm",
        "shrinkwrap",
        "u",
        "udpate",
        "un",
        "uninstall",
        "unlink",
        "up",
        "update",
        "upgrade",
        "verison",
        "version",
    }

    def rewrites_lockfile(self, command: list) -> bool:
        while (
            command
            and isinstance(command[0], str)
            and re.match(r"[A-Za-z_][A-Za-z0-9_]*=", command[0])
        ):
            command = command[1:]
        if command and command[0] in ("npx", "npx.cmd"):
            command = command[1:]
            if command and command[0] in ("-y", "--yes"):
                command = command[1:]
        if not command or command[0] not in ("npm", "npm.cmd"):
            return False
        command = command[1:]
        while command and isinstance(command[0], str) and command[0].startswith("-"):
            option = command[0]
            command = command[2:] if option in ("--prefix", "-C") else command[1:]
            if option == "--":
                break
        return (
            bool(command)
            and isinstance(command[0], str)
            and (
                command[0] in self.LOCKFILE_WRITING_SUBCOMMANDS
                or command[:2] == ["audit", "fix"]
            )
        )

    def lockfile_rewriters(self, recipes: dict[str, dict[str, object]]) -> set[str]:
        return {
            name
            for name, recipe in recipes.items()
            if any(
                self.rewrites_lockfile(command)
                for command in self.body_commands(recipe)
            )
        }

    def test_lockfile_rewriting_install_is_only_invoked_by_name(self) -> None:
        recipes = self.recipes()
        rewriters = self.lockfile_rewriters(recipes)
        self.assertTrue(rewriters, "no recipe rewrites the frontend lockfile")
        for name in sorted(recipes.keys() - rewriters):
            with self.subTest(recipe=name):
                dependencies = self.reachable(recipes, name) - {name}
                installers = sorted(dependencies & rewriters)
                self.assertFalse(
                    installers,
                    f"{name} depends on {', '.join(installers)}, "
                    "which may rewrite the frontend lockfile",
                )
                for command in self.body_commands(recipes[name]):
                    if (
                        self.just_call(command)
                        and len(command) > 1
                        and isinstance(command[1], str)
                    ):
                        self.assertNotIn(
                            command[1],
                            rewriters,
                            f"{name} invokes {command[1]}, "
                            "which may rewrite the frontend lockfile",
                        )

    def test_every_recipe_running_the_frontend_toolchain_installs_its_dependencies(
        self,
    ) -> None:
        recipes = self.recipes()
        self.assertIn(self.INSTALL_RECIPE, recipes)
        rewriters = self.lockfile_rewriters(recipes)
        users = [
            name
            for name, recipe in recipes.items()
            if name not in rewriters
            and self.FRONTEND_TOOLCHAIN.search(self.body_text(recipe))
        ]
        self.assertTrue(users, "no recipe runs the frontend toolchain")
        for name in users:
            with self.subTest(recipe=name):
                self.assertIn(
                    self.INSTALL_RECIPE,
                    self.reachable(recipes, name),
                    f"{name} runs node, npm or npx without installing the "
                    f"frontend dependencies from the lockfile first",
                )


class PythonToolRecipeTests(RecipeTestCase):
    INSTALL_RECIPE = "install-python-tools"
    SHELL_QUOTES = re.compile(r'''\\.|'[^']*'|"(?:[^"\\]|\\.)*"''')

    def uses_path(self, expression: object, paths: set[str]) -> bool:
        if not isinstance(expression, list):
            return False
        if expression[0] == "call" and expression[1] in (
            "justfile_directory",
            "justfile_dir",
        ):
            return True
        if expression[0] == "variable":
            return expression[1] in paths
        return any(self.uses_path(part, paths) for part in expression[1:])

    def path_variables(self, assignments: dict[str, dict[str, object]]) -> set[str]:
        paths = {"python"}
        while True:
            derived = {
                name
                for name, assignment in assignments.items()
                if self.uses_path(assignment["value"], paths)
            }
            if derived <= paths:
                return paths
            paths.update(derived)

    def test_recipe_path_interpolations_are_quoted(self) -> None:
        dump = self.dump()
        paths = self.path_variables(dump["assignments"])
        path_count = 0
        for name, recipe in dump["recipes"].items():
            # Recipe parameters still use POSIX quote() on Windows.
            recipe_paths = paths - {
                parameter["name"] for parameter in recipe["parameters"]
            }
            for line_number, line in enumerate(recipe["body"], start=1):
                # Only literal fragments determine the quote boundaries.
                text = ""
                interpolations = []
                for fragment in line:
                    if isinstance(fragment, str):
                        text += fragment
                    else:
                        (expression,) = fragment
                        if self.uses_path(expression, recipe_paths):
                            interpolations.append((len(text), expression))
                        text += "x"
                spans = [
                    match.span()
                    for match in self.SHELL_QUOTES.finditer(text)
                    if match[0].startswith('"')
                ]
                for offset, expression in interpolations:
                    path_count += 1
                    with self.subTest(recipe=name, line=line_number):
                        self.assertTrue(
                            any(start < offset < end for start, end in spans),
                            f"{name} body line {line_number}: {expression!r} "
                            "needs double quotes",
                        )
        self.assertGreater(path_count, 0, "no recipe path interpolations found")

    def venv_variables(self, assignments: dict[str, dict[str, object]]) -> set[str]:
        # The venv exists only once install-python-tools has run, so every
        # variable that names something inside it is a venv tool.
        found = {"venv_dir", "venv_bin"}
        grown = True
        while grown:
            grown = False
            for name, assignment in assignments.items():
                if name not in found and self.variables(assignment["value"]) & found:
                    found.add(name)
                    grown = True
        return found

    def test_every_recipe_running_a_venv_tool_installs_the_venv(self) -> None:
        dump = self.dump()
        recipes = dump["recipes"]
        self.assertIn(self.INSTALL_RECIPE, recipes)
        venv = self.venv_variables(dump["assignments"])
        self.assertLess({"venv_dir", "venv_bin"}, venv)
        users = [
            name
            for name, recipe in recipes.items()
            if self.variables(recipe["body"]) & venv
        ]
        self.assertTrue(users, "no recipe runs a venv tool")
        for name in users:
            with self.subTest(recipe=name):
                self.assertIn(
                    self.INSTALL_RECIPE,
                    self.reachable(recipes, name),
                    f"{name} runs a venv interpreter or tool without creating "
                    f"the venv first",
                )


class LocalPitRecipeTests(RecipeTestCase):
    def fixed_mode_go_calls(self, recipe: dict[str, object]) -> list[tuple[list, int]]:
        dispatchers = ("_go-dev-mode", "_go-tool-dev-mode")
        prior_count = recipe["priors"]
        calls = [
            (dependency["arguments"], min(index, prior_count))
            for index, dependency in enumerate(recipe["dependencies"])
            if dependency["recipe"] in dispatchers
        ]
        for command in self.body_commands(recipe):
            if (
                self.just_call(command)
                and len(command) > 1
                and command[1] in dispatchers
            ):
                calls.append((command[2:], prior_count))
            elif (
                len(command) >= 3
                and command[1]
                in (["variable", "just_helper"], "scripts/just_helpers.py")
                and command[2] in ("go-dev", "go-tool-dev")
            ):
                calls.append((command[3:], prior_count))
        return [
            (arguments, prior_limit)
            for arguments, prior_limit in calls
            if arguments and isinstance(arguments[0], str)
        ]

    def test_go_against_local_pit_builds_the_library_first(self) -> None:
        recipes = self.recipes()
        subjects = 0
        for name, recipe in recipes.items():
            calls = self.fixed_mode_go_calls(recipe)
            if not calls:
                continue
            subjects += 1
            for (mode, *arguments), prior_limit in calls:
                with self.subTest(recipe=name, mode=mode):
                    if (
                        prior_limit < recipe["priors"]
                        and "parallel" in recipe["attributes"]
                    ):
                        prior_limit = 0
                    self.assertTrue(arguments, f"{name} must pass a checkout to Go")
                    checkout = arguments[0]
                    if isinstance(checkout, list) and checkout[:2] == ["call", "quote"]:
                        checkout = checkout[2]
                    library_recipe = f"dylib-{mode}-dev"
                    libraries = [
                        dependency["arguments"]
                        for dependency in recipe["dependencies"][:prior_limit]
                        if dependency["recipe"] == library_recipe
                    ]
                    self.assertTrue(
                        libraries,
                        f"{name} must list {library_recipe} as a direct prior "
                        "before running Go",
                    )
                    self.assertIn(
                        checkout,
                        [arguments[0] for arguments in libraries if arguments],
                        f"{name} must build {library_recipe} with Go checkout "
                        f"{checkout!r}",
                    )
        self.assertGreater(subjects, 0, "no recipe runs Go with a fixed local Pit mode")


class CIRecipeTests(RecipeTestCase):
    def test_ci_invokes_every_aggregate_and_runs_python_tests(self) -> None:
        recipes = self.recipes()
        aggregates = {name for name in recipes if name.startswith("ci-")}
        workflow = (helpers.ROOT / ".github/workflows/ci.yml").read_text(
            encoding="utf-8"
        )
        invoked = set(
            re.findall(
                r"^\s*(?:run:\s+)?just\s+--justfile\s+pipeline\.just\s+(ci-[\w-]+)\b",
                workflow,
                re.MULTILINE,
            )
        )
        missing = sorted(aggregates - invoked)
        self.assertFalse(missing, f"CI does not invoke: {', '.join(missing)}")
        self.assertTrue(
            any("test-python" in self.reachable(recipes, name) for name in aggregates),
            "no CI aggregate depends on test-python",
        )


class FrontendInstallTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.web = self.root / "web"
        self.web.mkdir()
        (self.web / "package.json").write_text(
            '{"name": "web", "version": "1.0.0"}\n', encoding="utf-8"
        )
        self.lockfile = self.web / "package-lock.json"
        self.lockfile.write_text('{"lockfileVersion": 3}\n', encoding="utf-8")
        self.versions = self.root / ".github" / "ci-versions.env"
        self.versions.parent.mkdir()
        self.versions.write_text("CI_NODE=22.1.0\n", encoding="utf-8")
        # The project-local runtime ensure-node installs, with a real npm file.
        self.node_dir = self.root / "node"
        self.node_bin_dir = helpers.node_executable(self.node_dir).parent
        self.node_bin_dir.mkdir(parents=True)
        self.npm = self.node_bin_dir / ("npm.cmd" if helpers.is_windows() else "npm")
        self.npm.write_text("", encoding="utf-8")
        self.npm.chmod(0o755)

    def install(self, side_effect: object = None) -> Mock:
        parser = helpers.build_parser()
        args = parser.parse_args(["frontend-ci-install", str(self.node_dir)])
        with (
            patch.object(helpers, "ROOT", self.root),
            patch.object(helpers, "run", side_effect=side_effect) as run,
            contextlib.redirect_stdout(io.StringIO()),
        ):
            args.func(args)
        return run

    def npm_ci(self, command: list[str], cwd: Path | None = None) -> None:
        self.assertEqual(command[1:], ["ci"])
        self.assertTrue(Path(command[0]).samefile(self.npm), command[0])
        (self.web / "node_modules").mkdir(exist_ok=True)

    def assert_npm_ci_ran(self, run: Mock) -> None:
        run.assert_called_once()
        command, options = run.call_args
        self.assertEqual(command[0][1:], ["ci"])
        self.assertTrue(Path(command[0][0]).samefile(self.npm), command[0][0])
        self.assertEqual(options, {"cwd": self.web})

    def test_nothing_installed_runs_npm_ci_in_web_then_skips_the_next_call(
        self,
    ) -> None:
        run = self.install(side_effect=self.npm_ci)
        self.assert_npm_ci_ran(run)

        run = self.install(side_effect=self.npm_ci)
        run.assert_not_called()

    def test_a_lockfile_changed_after_an_install_runs_npm_ci_again(self) -> None:
        self.install(side_effect=self.npm_ci)
        self.lockfile.write_text('{"lockfileVersion": 3, "packages": {}}\n')

        self.assert_npm_ci_ran(self.install(side_effect=self.npm_ci))

    def test_a_package_json_changed_after_an_install_runs_npm_ci_again(self) -> None:
        self.install(side_effect=self.npm_ci)
        (self.web / "package.json").write_text(
            '{"name": "web", "version": "1.0.1"}\n', encoding="utf-8"
        )

        self.assert_npm_ci_ran(self.install(side_effect=self.npm_ci))

    def test_a_node_version_changed_after_an_install_runs_npm_ci_again(self) -> None:
        self.install(side_effect=self.npm_ci)
        self.versions.write_text("CI_NODE=22.2.0\n", encoding="utf-8")

        self.assert_npm_ci_ran(self.install(side_effect=self.npm_ci))

    def test_wiped_node_modules_runs_npm_ci_again(self) -> None:
        self.install(side_effect=self.npm_ci)
        shutil.rmtree(self.web / "node_modules")

        self.assert_npm_ci_ran(self.install(side_effect=self.npm_ci))

    def test_failed_npm_ci_propagates_and_records_no_install(self) -> None:
        failure = subprocess.CalledProcessError(1, [str(self.npm), "ci"])

        def fail(command: list[str], cwd: Path | None = None) -> None:
            (self.web / "node_modules").mkdir(exist_ok=True)
            raise failure

        with self.assertRaises(subprocess.CalledProcessError):
            self.install(side_effect=fail)
        self.assertEqual(list((self.web / "node_modules").iterdir()), [])

        self.assert_npm_ci_ran(self.install(side_effect=self.npm_ci))

    def test_missing_project_npm_fails_naming_the_runtime_directory(self) -> None:
        self.npm.unlink()

        with self.assertRaisesRegex(
            SystemExit, f"npm in {re.escape(str(self.node_bin_dir))}"
        ):
            self.install()
        self.assertFalse((self.web / "node_modules").exists())


if __name__ == "__main__":
    unittest.main()
