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


class NodeCommandAvailabilityTest(unittest.TestCase):
    def test_direct_node_commands_keep_the_original_dispatch(self):
        script = pathlib.Path(__file__).resolve().parents[1] / "antimage/antimage-node-binary.sh"
        source = script.read_text()
        start = source.index("\ndispatch_command() {")
        end = source.index('\nif [ -z "${COMMAND:-}" ]', start)
        commands = {
            "up": "up_command", "down": "down_command",
            "restart": "restart_command", "status": "status_command",
            "logs": "logs_command", "edit": "edit_command",
            "update": "update_command", "core-update": "update_core_command",
        }
        stubs = ["ensure_script_matches_installed_mode() { :; }"]
        for handler in commands.values():
            stubs.append(f"{handler}() {{ printf '%s\\n' '{handler}'; }}")
        body = "\n".join(stubs) + source[start:end] + '\ndispatch_command "$1"'
        for command, handler in commands.items():
            with self.subTest(command=command):
                result = subprocess.run(["bash", "-c", body, "node-test", command],
                                        capture_output=True, text=True, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, handler + "\n")


if __name__ == "__main__":
    unittest.main()
