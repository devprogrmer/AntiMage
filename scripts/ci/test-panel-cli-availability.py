#!/usr/bin/env python3
"""Exercise the actual installer CLI dispatcher without systemd or Docker."""
import pathlib
import subprocess
import unittest


class PanelCLIAvailabilityTest(unittest.TestCase):
    def dispatch(self, binary, up):
        script = pathlib.Path(__file__).resolve().parents[1] / "antimage/antimage-binary.sh"
        source = script.read_text()
        start = source.index("\ncli_command() {")
        end = source.index("\nup_command() {", start)
        stubs = "\n".join([
            "is_antimage_installed() { return 0; }",
            f"is_binary_install() {{ return {0 if binary else 1}; }}",
            f"is_antimage_up() {{ return {0 if up else 1}; }}",
            "detect_compose() { :; }",
            "colorized_echo() { :; }",
            "antimage_cli() { printf '%s\\n' \"$@\"; }",
        ])
        return subprocess.run(
            ["bash", "-c", stubs + source[start:end] + '\ncli_command admin list'],
            capture_output=True, text=True, check=False,
        )

    def test_binary_cli_works_while_panel_stopped(self):
        result = self.dispatch(binary=True, up=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "admin\nlist\n")

    def test_running_binary_cli_still_works(self):
        result = self.dispatch(binary=True, up=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "admin\nlist\n")

    def test_stopped_container_is_not_used_for_exec(self):
        result = self.dispatch(binary=False, up=False)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "")

    def test_running_container_keeps_existing_dispatch(self):
        result = self.dispatch(binary=False, up=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "admin\nlist\n")


if __name__ == "__main__":
    unittest.main()
