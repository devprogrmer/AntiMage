import useGetUser from "hooks/useGetUser";
import { QueryObserver, type QueryObserverOptions } from "react-query";
import { getSession } from "service/auth";
import { queryClient } from "utils/react-query";
import { expect, it, vi } from "vitest";

const useQuery = vi.hoisted(() => vi.fn());

vi.mock("react-query", async () => ({
	...(await vi.importActual<typeof import("react-query")>("react-query")),
	useQuery,
}));
vi.mock("react-router-dom", async () => {
	const router = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
	return {
		...router,
		createBrowserRouter: (routes: Parameters<typeof router.createMemoryRouter>[0]) =>
			router.createMemoryRouter(routes, {
				basename: "/dashboard",
				initialEntries: ["/dashboard/"],
			}),
	};
});
vi.mock("../service/auth", () => ({
	currentAdminQueryKey: ["current-admin"],
	getSession: vi.fn(),
}));
vi.mock("./Login", () => ({ Login: () => null }));

it("shares the route session with page controls and rejects a revoked session", async () => {
	const getCurrentSession = vi.mocked(getSession);
	getCurrentSession.mockResolvedValue({
		state: "active",
		admin: { username: "fixture", role: "full_access" },
	} as Awaited<ReturnType<typeof getSession>>);
	const { router } = await import("./Router");
	const waitForState = (predicate: () => boolean) =>
		predicate()
			? Promise.resolve()
			: new Promise<void>((resolve) => {
					const unsubscribe = router.subscribe(() => {
						if (!predicate()) return;
						unsubscribe();
						resolve();
					});
				});
	try {
		await waitForState(() => router.state.initialized);
		let unsubscribe: (() => void) | undefined;
		useQuery.mockImplementation((options: QueryObserverOptions) => {
			const observer = new QueryObserver(queryClient, options);
			unsubscribe = observer.subscribe(() => undefined);
			return observer.getCurrentResult();
		});
		try {
			const page = useGetUser();
			expect(page.userData.username).toBe("fixture");
			expect(page.getUserIsSuccess).toBe(true);
			expect(getCurrentSession).toHaveBeenCalledTimes(1);
		} finally {
			unsubscribe?.();
		}
		getCurrentSession.mockRejectedValueOnce({ status: 401 });
		router.revalidate();
		await waitForState(() => router.state.location.pathname === "/dashboard/login/");
		expect(getCurrentSession).toHaveBeenCalledTimes(2);
	} finally {
		router.dispose();
		queryClient.clear();
	}
});
