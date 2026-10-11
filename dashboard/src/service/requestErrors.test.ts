import { afterEach, describe, expect, it } from "vitest";
import { clearRequestErrors, getRequestErrors, recordRequestError, requestErrorOperation, sanitizeRequestErrorMessage } from "./requestErrors";

afterEach(clearRequestErrors);

describe("request error capture", () => {
	it("returns a stable external-store snapshot until the records change", () => {
		const empty = getRequestErrors();
		expect(getRequestErrors()).toBe(empty);
		recordRequestError({ request_id: "req-stable", endpoint: "/users", method: "GET", status: 500, operation: "GET /users", sanitized_message: "safe", timestamp: new Date(0).toISOString() });
		const populated = getRequestErrors();
		expect(getRequestErrors()).toBe(populated);
		expect(populated).not.toBe(empty);
	});

	it("redacts credential-like values and bounds the message", () => {
		const message = sanitizeRequestErrorMessage("password=hunter2 Authorization: Bearer abc.def secret:shh");
		expect(message).not.toContain("hunter2");
		expect(message).not.toContain("abc.def");
		expect(message).not.toContain("shh");
	});

	it("stores only the structured safe record and caps history", () => {
		for (let index = 0; index < 105; index++) {
			recordRequestError({ request_id: `req-${index}`, endpoint: "/users", method: "GET", status: 500, operation: "GET /users", sanitized_message: "safe", timestamp: new Date(index).toISOString() });
		}
		expect(getRequestErrors()).toHaveLength(100);
		expect(getRequestErrors()[0].request_id).toBe("req-104");
		expect(requestErrorOperation("post", "/nodes/22/restart?token=x")).toBe("POST /nodes/:id/restart");
	});
});
