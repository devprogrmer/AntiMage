import { type FetchOptions, $fetch as ohMyFetch } from "ofetch";
import { recordRequestError, requestErrorOperation } from "./requestErrors";

// The embedded dashboard always talks to the gateway under /api. Keep an
// explicit environment override for hosted deployments, but make the
// standalone build correct without requiring a .env file.
const configuredBaseURL = import.meta.env.VITE_BASE_API || "/api";

const getDevProxyBaseURL = (baseURL: string) => {
	try {
		const parsed = new URL(baseURL);
		return parsed.pathname && parsed.pathname !== "/" ? parsed.pathname : "/api";
	} catch {
		return baseURL;
	}
};

export const apiBaseURL =
	import.meta.env.DEV && /^https?:\/\//i.test(configuredBaseURL)
		? getDevProxyBaseURL(configuredBaseURL)
		: configuredBaseURL;

export const $fetch = ohMyFetch.create({
	baseURL: apiBaseURL,
	credentials: "include",
	onResponseError({ request, options, response }) {
		const rawURL = typeof request === "string" ? request : request.url;
		let endpoint = rawURL;
		try {
			const parsed = new URL(rawURL, window.location.origin);
			const safePath = parsed.pathname.split("/").map((segment) => segment.length >= 16 || /^[a-f0-9]{16,}$/i.test(segment) ? "[redacted]" : segment).join("/");
			endpoint = `${safePath}${parsed.search ? "?[redacted]" : ""}`;
		} catch {
			endpoint = rawURL.split("?")[0];
		}
		const method = String(options.method || (typeof request === "string" ? "GET" : request.method)).toUpperCase();
		const payload = response._data as { request_id?: unknown } | undefined;
		const message = `HTTP ${response.status} request failed`;
		recordRequestError({
			request_id: response.headers.get("x-request-id") || (typeof payload?.request_id === "string" ? payload.request_id : null),
			endpoint,
			method,
			status: response.status,
			operation: requestErrorOperation(method, endpoint),
			sanitized_message: message,
			timestamp: new Date().toISOString(),
		});
	},
});

export const fetcher = <T = any>(
	url: string,
	ops: FetchOptions<"json"> = {},
) => {
	const method = String(ops.method || "GET").toUpperCase();
	ops.credentials = "include";
	if (method === "GET") {
		ops.cache = "no-store";
	}
	return $fetch<T>(url, ops);
};

export const fetch = fetcher;
