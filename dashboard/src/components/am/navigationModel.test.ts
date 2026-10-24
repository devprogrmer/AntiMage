import { describe, expect, it } from "vitest";
import { AdminRole, AdminSection, AdminSudoScope } from "types/Admin";
import { buildAMNavigation } from "./navigationModel";

const admin = (role: AdminRole, sections: Partial<Record<AdminSection, boolean>> = {}, sudo: Partial<Record<AdminSudoScope, boolean>> = {}) => ({
	role,
	permissions: { sections, sudo, self_permissions: {} },
}) as never;

const paths = (role: AdminRole, sections?: Partial<Record<AdminSection, boolean>>, sudo?: Partial<Record<AdminSudoScope, boolean>>) => buildAMNavigation(admin(role, sections, sudo)).flatMap((group) => group.items.map((entry) => entry.path));

describe("AntiMage navigation model", () => {
	it("always uses the canonical navigation groups", () => {
		const groups = buildAMNavigation(admin(AdminRole.Standard, { [AdminSection.Services]: true, [AdminSection.Nodes]: true, [AdminSection.Integrations]: true }));
		expect(groups.map((group) => group.id)).toEqual(["command-center", "infrastructure", "access", "settings"]);
	});

	it("keeps maintenance items permission gated", () => {
		const standard = paths(AdminRole.Standard);
		expect(standard).toContain("/users");
		expect(standard).not.toContain("/diagnostics");
		expect(standard).not.toContain("/updates");
		const sudo = paths(AdminRole.Sudo, {}, { [AdminSudoScope.Maintenance]: true });
		expect(sudo).toContain("/updates");
		expect(sudo).toContain("/diagnostics");
	});

	it("keeps section items permission gated", () => {
		const withoutSections = paths(AdminRole.Standard);
		expect(withoutSections).not.toContain("/node-settings");
		expect(withoutSections).not.toContain("/xray-settings");
		const withSections = paths(AdminRole.Standard, { [AdminSection.Nodes]: true, [AdminSection.Xray]: true });
		expect(withSections).toContain("/node-settings");
		expect(withSections).toContain("/xray-settings");
	});
});
