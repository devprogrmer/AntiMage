import type { ReactNode } from "react";
import { ChakraProvider } from "@chakra-ui/react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
	role: "full_access",
	backups: true,
	maintenance: true,
	mode: "binary",
	runtimeError: false,
	runtimeLoading: false,
}));
vi.mock("locales/i18n", () => ({
	default: { t: (key: string) => key, language: "en", dir: () => "ltr" },
}));
vi.mock("hooks/useGetUser", () => ({
	default: () => ({
		userData: {
			role: state.role,
			permissions: {
				sections: {},
				sudo: { backups: state.backups, maintenance: state.maintenance },
			},
		},
		getUserIsSuccess: true,
		getUserIsPending: false,
	}),
}));
vi.mock("contexts/DashboardContext", () => ({
	useDashboard: Object.assign(() => ({ version: "dev-test" }), {
		setState: vi.fn(),
	}),
}));
vi.mock("contexts/NodesContext", () => ({
	useNodesQuery: () => ({ data: [] }),
}));
vi.mock("react-router-dom", () => ({
	useNavigate: () => vi.fn(),
	useLocation: () => ({ pathname: "/backup" }),
	NavLink: ({ to, children }: { to: string; children: ReactNode }) => (
		<a href={to}>{children}</a>
	),
}));
vi.mock("react-i18next", async () => ({
	...(await vi.importActual<object>("react-i18next")),
	useTranslation: () => ({
		t: (key: string) => key,
		i18n: { dir: () => "ltr", language: "en" },
	}),
}));
vi.mock("react-query", () => ({
	useQueryClient: () => ({ invalidateQueries: vi.fn() }),
	useMutation: () => ({ mutate: vi.fn(), isLoading: false }),
	useQuery: (input: { queryKey?: string } | string | string[]) => ({
		data:
			typeof input === "object" &&
			!Array.isArray(input) &&
			input.queryKey === "statistics-query-key"
				? {
						version: "dev-test",
						channel: "dev",
						memory: {},
						disk: {},
						swap: {},
						cpu_history: [],
						network_history: [],
					}
				: Array.isArray(input)
					? { panel: { mode: state.mode } }
					: { actions: [] },
		isSuccess: !state.runtimeError,
		isError: state.runtimeError,
		isLoading: state.runtimeLoading,
		isFetching: false,
		refetch: vi.fn(),
	}),
}));

import { AppSidebar } from "./AppSidebar";
import { Statistics } from "./Statistics";
import { BackupRestorePage } from "../pages/BackupRestorePage";

describe("rendered dashboard maintenance access", () => {
	beforeEach(() => {
		state.role = "full_access";
		state.backups = true;
		state.maintenance = true;
		state.mode = "binary";
		state.runtimeError = false;
		state.runtimeLoading = false;
	});
	const render = () =>
		renderToStaticMarkup(
			<ChakraProvider>
				<Statistics />
			</ChakraProvider>,
		);
	it("shows backup, update and restart controls in the active dashboard", () => {
		const html = render();
		expect(html).toContain("settings.backup.tabTitle");
		expect(html).toContain("settings.panel.updateAction");
		expect(html).toContain("settings.panel.restartAction");
	});
	it("keeps maintenance and backup permissions independent", () => {
		state.role = "sudo";
		state.maintenance = false;
		const html = render();
		expect(html).toContain("settings.backup.tabTitle");
		expect(html).not.toContain("settings.panel.updateAction");
		expect(html).not.toContain("settings.panel.restartAction");
	});
	it("does not offer host controls to standard administrators", () => {
		state.role = "standard";
		const html = render();
		expect(html).not.toContain("settings.backup.tabTitle");
		expect(html).not.toContain("settings.panel.updateAction");
		expect(html).not.toContain("settings.panel.restartAction");
	});
	it("does not grant backups with maintenance permission alone", () => {
		state.role = "sudo";
		state.backups = false;
		const html = render();
		expect(html).not.toContain("settings.backup.tabTitle");
		expect(html).toContain("settings.panel.updateAction");
	});
	it("provides an independent backup page for backup-only administrators", () => {
		state.role = "sudo";
		state.maintenance = false;
		const html = renderToStaticMarkup(
			<ChakraProvider>
				<BackupRestorePage />
			</ChakraProvider>,
		);
		expect(html).toContain("settings.backup.import");
		expect(html).toContain("settings.backup.exportTitle");
		expect(html).not.toContain("settings.backup.noPermission");
	});
	it("denies direct backup-page access to standard administrators", () => {
		state.role = "standard";
		const html = renderToStaticMarkup(
			<ChakraProvider>
				<BackupRestorePage />
			</ChakraProvider>,
		);
		expect(html).toContain("settings.backup.noPermission");
		expect(html).not.toContain("settings.backup.importHint");
	});
	it("explains unavailable backup actions on non-binary installations", () => {
		state.mode = "docker";
		const html = renderToStaticMarkup(
			<ChakraProvider>
				<BackupRestorePage />
			</ChakraProvider>,
		);
		expect(html).toContain("settings.backup.binaryOnlyNotice");
	});
	it("explains runtime-check failures without offering unsafe actions", () => {
		state.runtimeError = true;
		state.mode = "";
		const html = renderToStaticMarkup(
			<ChakraProvider>
				<BackupRestorePage />
			</ChakraProvider>,
		);
		expect(html).toContain("settings.backup.runtimeCheckFailed");
	});
	it.each([
		"full_access",
		"sudo",
	])("keeps backup navigation visible to %s", (role) => {
		state.role = role;
		state.maintenance = false;
		const html = renderToStaticMarkup(
			<ChakraProvider>
				<AppSidebar collapsed={false} />
			</ChakraProvider>,
		);
		expect(html).toContain('href="/backup"');
		expect(html).toContain("settings.backup.tabTitle");
	});
	it.each([
		"standard",
		"reseller",
		"sudo",
	])("hides backup navigation from unpermitted %s", (role) => {
		state.role = role;
		state.backups = false;
		const html = renderToStaticMarkup(
			<ChakraProvider>
				<AppSidebar collapsed={false} />
			</ChakraProvider>,
		);
		expect(html).not.toContain('href="/backup"');
	});
});
