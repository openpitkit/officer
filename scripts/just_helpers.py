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
# Please see https://openpit.dev and the OWNERS file for details.

"""Cross-platform helpers for Officer just recipes."""

from __future__ import annotations

import argparse
import atexit
import os
import re
import shutil
import subprocess
import sys
import tempfile
from collections.abc import Sequence
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BUILD_MODES = {"debug", "release"}
_WINDOWS_CGO_COMPILER_COMMANDS: dict[tuple[str, ...], str] = {}
_WINDOWS_CGO_WRAPPER_DIRS: set[Path] = set()


def _cleanup_windows_cgo_wrapper_dirs() -> None:
    for wrapper_dir in list(_WINDOWS_CGO_WRAPPER_DIRS):
        shutil.rmtree(wrapper_dir, ignore_errors=True)
    _WINDOWS_CGO_WRAPPER_DIRS.clear()
    _WINDOWS_CGO_COMPILER_COMMANDS.clear()


atexit.register(_cleanup_windows_cgo_wrapper_dirs)


def is_windows() -> bool:
    return os.name == "nt"


def build_mode(value: str) -> str:
    if value not in BUILD_MODES:
        modes = ", ".join(sorted(BUILD_MODES))
        raise SystemExit(f"unsupported build mode {value!r}; expected one of: {modes}")
    return value


def run(
    args: Sequence[str],
    *,
    cwd: Path | None = None,
    env: dict[str, str] | None = None,
    capture: bool = False,
) -> subprocess.CompletedProcess[str]:
    command = list(args)
    if not command:
        raise SystemExit("missing executable: empty command")
    try:
        return subprocess.run(
            command,
            cwd=cwd or ROOT,
            env=env,
            check=True,
            text=True,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
        )
    except FileNotFoundError as exc:
        executable = command[0] if command else str(exc.filename)
        raise SystemExit(
            f"missing executable: {executable}; install it or add it to PATH"
        ) from None


def clean_cli_value(value: object) -> object:
    if (
        isinstance(value, str)
        and len(value) >= 2
        and value[0] == value[-1]
        and value[0] in {'"', "'"}
    ):
        return value[1:-1]
    if isinstance(value, list):
        return [clean_cli_value(item) for item in value]
    return value


def clean_cli_args(args: argparse.Namespace) -> argparse.Namespace:
    for name, value in vars(args).items():
        setattr(args, name, clean_cli_value(value))
    return args


def normalize_go_args(args: Sequence[str]) -> list[str]:
    normalized: list[str] = []
    index = 0
    while index < len(args):
        arg = args[index]
        if (
            arg == "-gcflags=all=-N"
            and index + 1 < len(args)
            and args[index + 1] == "-l"
        ):
            normalized.append("-gcflags=all=-N -l")
            index += 2
            continue
        normalized.append(arg)
        index += 1
    return normalized


def runtime_library_name() -> str:
    if sys.platform == "darwin":
        return "libopenpit_ffi.dylib"
    if is_windows():
        return "openpit_ffi.dll"
    if sys.platform.startswith("linux"):
        return "libopenpit_ffi.so"
    raise SystemExit(f"unsupported OS for OpenPit runtime lookup: {sys.platform}")


def officer_binary() -> str:
    return "pit-officer.exe" if is_windows() else "pit-officer"


def resolve_checkout(path: str) -> Path:
    checkout = Path(path).expanduser()
    if not checkout.is_absolute():
        checkout = ROOT / checkout
    return checkout.resolve()


def local_runtime_library(pit_checkout: str, mode: str = "release") -> Path:
    profile = build_mode(mode)
    pit_dir = resolve_checkout(pit_checkout)
    runtime = pit_dir / "target" / profile / runtime_library_name()
    if runtime.is_file():
        return runtime
    candidates = sorted(pit_dir.glob(f"target/*/{profile}/{runtime_library_name()}"))
    if candidates:
        return candidates[0]
    raise SystemExit(f"OpenPit runtime library was not found under {pit_dir}/target")


def command_export_ci_env(_: argparse.Namespace) -> None:
    github_env = os.environ.get("GITHUB_ENV")
    if not github_env:
        raise SystemExit("GITHUB_ENV is not set")
    versions = ROOT / ".github" / "ci-versions.env"
    if not versions.is_file():
        raise SystemExit(f"{versions} not found")
    lines = [
        line
        for line in versions.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.lstrip().startswith("#")
    ]
    with Path(github_env).open("a", encoding="utf-8", newline="\n") as file:
        for line in lines:
            file.write(f"{line}\n")


def required_semgrep_version() -> str:
    requirements = ROOT / "checks" / "semgrep" / "requirements.txt"
    try:
        lines = requirements.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise SystemExit(
            f"could not read Semgrep requirements file {requirements}: {exc}"
        ) from None

    for line in lines:
        match = re.match(r"^\s*semgrep\s*==\s*([^\s;#]*)", line)
        if match is None:
            continue
        version = match.group(1)
        if version:
            return version
        raise SystemExit(
            f"Semgrep requirements file {requirements} has an empty version; "
            "expected semgrep==<version>"
        )

    raise SystemExit(
        f"Semgrep requirements file {requirements} has no Semgrep pin; "
        "expected semgrep==<version>"
    )


def installed_semgrep_version(venv_python: Path) -> str | None:
    if not venv_python.is_file():
        return None
    result = run(
        [
            str(venv_python),
            "-c",
            (
                "from importlib.metadata import PackageNotFoundError, version; "
                "\ntry:\n print(version('semgrep'))\n"
                "except PackageNotFoundError:\n pass"
            ),
        ],
        capture=True,
    )
    return result.stdout.strip() or None


def command_install_semgrep(_: argparse.Namespace) -> None:
    required_version = required_semgrep_version()
    venv_dir = ROOT / ".venv"
    venv_bin = venv_dir / ("Scripts" if is_windows() else "bin")
    venv_python = venv_bin / ("python.exe" if is_windows() else "python")
    installed_version = installed_semgrep_version(venv_python)
    if installed_version == required_version:
        print(f"Semgrep {required_version} already installed.")
        return

    run([sys.executable, "-m", "venv", str(venv_dir)])
    run(
        [
            str(venv_python),
            "-m",
            "pip",
            "install",
            "-r",
            str(ROOT / "requirements.txt"),
        ]
    )


def command_check_gofmt(args: argparse.Namespace) -> None:
    result = run(["gofmt", "-l", *args.paths], capture=True)
    unformatted = result.stdout.strip()
    if unformatted:
        print(unformatted)
        raise SystemExit(1)


def windows_cgo_compiler_command(compiler: Sequence[str]) -> str:
    compiler_key = tuple(compiler)
    cached = _WINDOWS_CGO_COMPILER_COMMANDS.get(compiler_key)
    if cached is not None:
        return cached
    wrapper_dir = Path(tempfile.mkdtemp(prefix="pit-officer-go-cgo-"))
    _WINDOWS_CGO_WRAPPER_DIRS.add(wrapper_dir)
    wrapper = wrapper_dir / "cc_wrapper.py"
    wrapper.write_text(
        "\n".join(
            [
                "from __future__ import annotations",
                "",
                "import subprocess",
                "import sys",
                "",
                'filtered = {"-mthreads", "-s"}',
                "args = [arg for arg in sys.argv[1:] if arg not in filtered]",
                "raise SystemExit(subprocess.call(args))",
                "",
            ]
        ),
        encoding="utf-8",
    )
    command = subprocess.list2cmdline([sys.executable, str(wrapper), *compiler])
    _WINDOWS_CGO_COMPILER_COMMANDS[compiler_key] = command
    return command


def go_env() -> dict[str, str]:
    env = os.environ.copy()
    env["CGO_ENABLED"] = "1"
    if is_windows():
        if "CC" not in env:
            if not shutil.which("clang"):
                raise SystemExit(
                    "missing cgo C compiler: install LLVM clang or set CC"
                )
            env["CC"] = os.environ.get(
                "PIT_WINDOWS_CGO_CC"
            ) or windows_cgo_compiler_command(["clang", "-fuse-ld=lld"])
        if "CXX" not in env:
            if not shutil.which("clang++"):
                raise SystemExit(
                    "missing cgo C++ compiler: install LLVM clang++ or set CXX"
                )
            env["CXX"] = os.environ.get(
                "PIT_WINDOWS_CGO_CXX"
            ) or windows_cgo_compiler_command(["clang++", "-fuse-ld=lld"])
    return env


def command_check_cgo_toolchain(_: argparse.Namespace) -> None:
    if os.environ.get("CGO_ENABLED") == "0":
        raise SystemExit("CGO_ENABLED=0 is not supported: OpenPit requires cgo")
    env = go_env()
    compiler = env.get("CC") or ("clang" if is_windows() else "cc")
    executable = compiler.split()[0]
    if not shutil.which(executable) and not Path(executable).is_file():
        raise SystemExit(f"missing cgo C compiler: {executable}")


def command_go(args: argparse.Namespace) -> None:
    run(
        ["go", *normalize_go_args(args.go_args)],
        cwd=ROOT / args.module_dir,
        env=go_env(),
    )


def command_go_tool(args: argparse.Namespace) -> None:
    run(
        args.tool_args,
        cwd=ROOT / args.module_dir,
        env=go_env(),
    )


def command_dylib_dev(args: argparse.Namespace) -> None:
    mode = build_mode(args.mode)
    pit_dir = resolve_checkout(args.pit_checkout)
    command = ["cargo", "build", "-p", "openpit-ffi", "--locked"]
    if mode == "release":
        command.append("--release")
    command.extend(["--manifest-path", str(pit_dir / "Cargo.toml")])
    run(command, cwd=pit_dir)


def dev_go_env(
    pit_checkout: str,
    workspace_file: Path,
    mode: str = "release",
) -> dict[str, str]:
    env = os.environ.copy()
    env.update(go_env())
    env["GOWORK"] = str(workspace_file)
    env["OPENPIT_RUNTIME_LIBRARY_PATH"] = str(
        local_runtime_library(pit_checkout, mode)
    )
    return env


def write_dev_workspace(pit_checkout: str, workspace_dir: Path) -> Path:
    pit_dir = resolve_checkout(pit_checkout)
    run(
        [
            "go",
            "work",
            "init",
            str(ROOT),
            str(ROOT / "framework"),
            str(pit_dir / "bindings" / "go"),
        ],
        cwd=workspace_dir,
    )
    return workspace_dir / "go.work"


def command_go_dev(args: argparse.Namespace) -> None:
    mode = build_mode(args.mode)
    with tempfile.TemporaryDirectory(prefix="pit-officer-go-work-") as temp_dir:
        work_dir = Path(temp_dir)
        workspace_file = write_dev_workspace(args.pit_checkout, work_dir)
        run(
            ["go", *normalize_go_args(args.go_args)],
            cwd=ROOT / args.module_dir,
            env=dev_go_env(args.pit_checkout, workspace_file, mode),
        )


def command_go_tool_dev(args: argparse.Namespace) -> None:
    mode = build_mode(args.mode)
    with tempfile.TemporaryDirectory(prefix="pit-officer-go-work-") as temp_dir:
        work_dir = Path(temp_dir)
        workspace_file = write_dev_workspace(args.pit_checkout, work_dir)
        run(
            args.tool_args,
            cwd=ROOT / args.module_dir,
            env=dev_go_env(args.pit_checkout, workspace_file, mode),
        )


def command_run_officer(args: argparse.Namespace) -> None:
    executable = ROOT / officer_binary()
    if not executable.is_file():
        raise SystemExit(f"{executable} not found; run the build recipe first")
    run([str(executable), *args.officer_args])


def command_run_officer_dev(args: argparse.Namespace) -> None:
    mode = build_mode(args.mode)
    executable = ROOT / officer_binary()
    if not executable.is_file():
        raise SystemExit(f"{executable} not found; run the build recipe first")
    env = os.environ.copy()
    env["OPENPIT_RUNTIME_LIBRARY_PATH"] = str(
        local_runtime_library(args.pit_checkout, mode)
    )
    run([str(executable), *args.officer_args], env=env)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    subparsers.add_parser("export-ci-env").set_defaults(func=command_export_ci_env)

    subparsers.add_parser("install-semgrep").set_defaults(func=command_install_semgrep)

    subparser = subparsers.add_parser("check-gofmt")
    subparser.add_argument("paths", nargs="+")
    subparser.set_defaults(func=command_check_gofmt)

    subparsers.add_parser("check-cgo-toolchain").set_defaults(
        func=command_check_cgo_toolchain
    )

    subparser = subparsers.add_parser("go")
    subparser.add_argument("module_dir")
    subparser.add_argument("go_args", nargs=argparse.REMAINDER)
    subparser.set_defaults(func=command_go)

    subparser = subparsers.add_parser("go-tool")
    subparser.add_argument("module_dir")
    subparser.add_argument("tool_args", nargs=argparse.REMAINDER)
    subparser.set_defaults(func=command_go_tool)

    subparser = subparsers.add_parser("dylib-dev")
    subparser.add_argument("mode", choices=sorted(BUILD_MODES))
    subparser.add_argument("pit_checkout")
    subparser.set_defaults(func=command_dylib_dev)

    subparser = subparsers.add_parser("go-dev")
    subparser.add_argument("mode", choices=sorted(BUILD_MODES))
    subparser.add_argument("pit_checkout")
    subparser.add_argument("module_dir")
    subparser.add_argument("go_args", nargs=argparse.REMAINDER)
    subparser.set_defaults(func=command_go_dev)

    subparser = subparsers.add_parser("go-tool-dev")
    subparser.add_argument("mode", choices=sorted(BUILD_MODES))
    subparser.add_argument("pit_checkout")
    subparser.add_argument("module_dir")
    subparser.add_argument("tool_args", nargs=argparse.REMAINDER)
    subparser.set_defaults(func=command_go_tool_dev)

    subparser = subparsers.add_parser("run-officer")
    subparser.add_argument("officer_args", nargs=argparse.REMAINDER)
    subparser.set_defaults(func=command_run_officer)

    subparser = subparsers.add_parser("run-officer-dev")
    subparser.add_argument("mode", choices=sorted(BUILD_MODES))
    subparser.add_argument("pit_checkout")
    subparser.add_argument("officer_args", nargs=argparse.REMAINDER)
    subparser.set_defaults(func=command_run_officer_dev)

    return parser


def main() -> None:
    args = clean_cli_args(build_parser().parse_args())
    try:
        args.func(args)
    except subprocess.CalledProcessError as exc:
        if exc.stdout:
            print(exc.stdout, end="", file=sys.stdout)
        if exc.stderr:
            print(exc.stderr, end="", file=sys.stderr)
        raise SystemExit(exc.returncode) from None


if __name__ == "__main__":
    main()
