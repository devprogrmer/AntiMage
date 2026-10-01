import { describe, expect, it } from "vitest";
import { normalizeNodeUpdateChannel } from "./NodesContext";

describe("normalizeNodeUpdateChannel", () => {
	it("maps the dashboard latest channel to the node stable channel", () => {
		expect(normalizeNodeUpdateChannel("latest")).toBe("stable");
		expect(normalizeNodeUpdateChannel("dev")).toBe("dev");
	});
});
