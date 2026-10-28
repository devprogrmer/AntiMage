import {
	AdminRole,
	AdminSection,
	AdminSudoScope,
	SelfPermissionToggle,
} from "types/Admin";
import type { UserApi } from "types/User";

export type AMNavigationItem = {
	id: string;
	labelKey: string;
	path: string;
};

export type AMNavigationGroup = {
	id: string;
	labelKey: string;
	items: AMNavigationItem[];
};

type NavigationAdmin = Pick<UserApi, "role" | "permissions">;

const isElevated = (admin: NavigationAdmin) =>
	admin.role === AdminRole.FullAccess || admin.role === AdminRole.Sudo;

const hasSection = (admin: NavigationAdmin, section: AdminSection) =>
	isElevated(admin) || Boolean(admin.permissions.sections?.[section]);

const hasSelf = (admin: NavigationAdmin, permission: SelfPermissionToggle) =>
	admin.role === AdminRole.FullAccess ||
	Boolean(admin.permissions.self_permissions?.[permission]);

const item = (id: string, labelKey: string, path: string): AMNavigationItem =>
	({ id, labelKey, path });

export const buildAMNavigation = (
	admin: NavigationAdmin,
): AMNavigationGroup[] => {
	const fullAccess = admin.role === AdminRole.FullAccess;
	const elevated = isElevated(admin);
	const canRecentActions =
		fullAccess ||
		(admin.role === AdminRole.Sudo &&
			Boolean(admin.permissions.sudo?.[AdminSudoScope.Xray]));
	const canUseMaintenance =
		fullAccess ||
		(admin.role === AdminRole.Sudo &&
			Boolean(admin.permissions.sudo?.[AdminSudoScope.Maintenance]));
	const canUseSettings = hasSection(admin, AdminSection.Integrations);
	const canUseXray = hasSection(admin, AdminSection.Xray);
	const canUseNodes = hasSection(admin, AdminSection.Nodes);
	const canUseHosts = hasSection(admin, AdminSection.Hosts);
	const canUseServices = hasSection(admin, AdminSection.Services);
	const canUseUsage = hasSection(admin, AdminSection.Usage);
	const canUseAdmins = hasSection(admin, AdminSection.Admins);
	const canUsePHPMyAdmin = fullAccess || (admin.role === AdminRole.Sudo && Boolean(admin.permissions.sudo?.[AdminSudoScope.PHPMyAdmin]));
	const groups: AMNavigationGroup[] = [
		{
			id: "command-center",
			labelKey: "am.nav.commandCenter",
			items: [item("overview", "am.nav.overview", "/")],
		},
		{
			id: "infrastructure",
			labelKey: "am.nav.infrastructure",
			items: [
				...(canUseNodes
					? [item("nodes", "am.nav.nodes", "/node-settings")]
					: []),
				...(canUseServices
					? [item("services", "am.nav.services", "/services")]
					: []),
				...(elevated
					? [item("load-balancing", "am.nav.loadBalancing", "/haproxy")]
					: []),
				...(canUseUsage
					? [item("usage", "am.nav.usage", "/usage")]
					: []),
			],
		},
		{
			id: "access",
			labelKey: "am.nav.access",
			items: [
				item("users", "am.nav.users", "/users"),
				item("bulk-actions", "am.nav.bulkActions", "/bulk-actions"),
				...(canUseAdmins
					? [item("admins", "am.nav.admins", "/admins")]
					: []),
				...(hasSelf(admin, SelfPermissionToggle.SelfMyAccount)
					? [item("my-account", "am.nav.myAccount", "/myaccount")]
					: []),
			],
		},
		{
			id: "protocols",
			labelKey: "am.nav.protocols",
			items: [
				...(canUseXray
					? [
							item("xray", "am.nav.xray", "/xray-settings"),
							item("xray-logs", "am.nav.xrayLogs", "/xray-logs"),
							item("access-insights", "am.nav.accessInsights", "/access-insights"),
						]
					: []),
			],
		},
		{
			id: "delivery",
			labelKey: "am.nav.delivery",
			items: canUseHosts
				? [item("hosts", "am.nav.hosts", "/hosts")]
				: [],
		},
		{
			id: "operations",
			labelKey: "am.nav.operations",
			items: [
				...(canUseMaintenance
					? [item("diagnostics", "am.nav.diagnostics", "/diagnostics")]
					: []),
				...(canUseMaintenance
					? [item("operation-history", "am.nav.operationHistory", "/operations")]
					: []),
				...(canUseMaintenance
					? [item("updates", "am.nav.updates", "/updates")]
					: []),
				...(canRecentActions
					? [item("recent-actions", "am.nav.eventHistory", "/recent-actions")]
					: []),
			],
		},
		{
			id: "settings",
			labelKey: "am.nav.settingsGroup",
			items: [
				...(canUseSettings
					? [item("settings", "am.nav.integrations", "/settings")]
					: []),
				...(elevated
					? [
							item("external-apps", "am.nav.externalApps", "/external-apps"),
							item("api-docs", "am.nav.apiDocs", "/api-docs"),
						]
					: []),
				...(canUsePHPMyAdmin
					? [item("phpmyadmin", "phpmyadmin.menu", "/phpmyadmin")]
					: []),
				item("tutorials", "am.nav.guides", "/tutorials"),
			],
		},
	];
	return groups.filter((group) => group.items.length > 0);
};
