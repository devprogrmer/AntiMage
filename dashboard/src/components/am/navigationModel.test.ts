import { afterEach, describe, expect, it, vi } from "vitest";
import { AdminRole, AdminSection, AdminSudoScope } from "types/Admin";
import { buildAMNavigation, navigationPreferenceKey, readNavigationMode, writeNavigationMode } from "./navigationModel";

const admin = (role: AdminRole, sections: Partial<Record<AdminSection, boolean>> = {}, sudo: Partial<Record<AdminSudoScope, boolean>> = {}) => ({
	role,
	permissions: { sections, sudo, self_permissions: {} },
}) as never;

const paths = (role: AdminRole, mode: "simple" | "advanced", sections?: Partial<Record<AdminSection, boolean>>, sudo?: Partial<Record<AdminSudoScope, boolean>>) => buildAMNavigation(admin(role, sections, sudo), mode).flatMap((group) => group.items.map((entry) => entry.path));

afterEach(() => { vi.unstubAllGlobals(); });

describe("AntiMage navigation model", () => {
	it("shows the simple workspace without granting additional access", () => {
		expect(paths(AdminRole.Standard, "simple", { [AdminSection.Services]: true, [AdminSection.Nodes]: true, [AdminSection.Integrations]: true })).toEqual(["/", "/users", "/services", "/node-settings", "/settings"]);
	});

	it("keeps advanced-only items permission gated", () => {
		const standard = paths(AdminRole.Standard, "advanced");
		expect(standard).toContain("/users");
		expect(standard).not.toContain("/diagnostics");
		expect(standard).not.toContain("/updates");
		const sudo = paths(AdminRole.Sudo, "advanced", {}, { [AdminSudoScope.Maintenance]: true });
		expect(sudo).toContain("/updates");
		expect(sudo).toContain("/diagnostics");
	});

	it("keeps the preference isolated by normalized admin username", () => {
		const values = new Map<string, string>();
		vi.stubGlobal("window", { localStorage: { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value) } });
		expect(navigationPreferenceKey(" Alice ")).toBe(navigationPreferenceKey("alice"));
		writeNavigationMode("alice", "advanced");
		expect(readNavigationMode("alice")).toBe("advanced");
	});
});
