import { describe, expect, it } from "vitest";
import { normalizeNodeUpdateChannel } from "./NodesContext";

describe("normalizeNodeUpdateChannel", () => {
	it.each([
		["latest", "stable"],
		["release", "stable"],
		["master", "stable"],
		["stable", "stable"],
		["development", "dev"],
		["dev-builds", "dev"],
		["dev", "dev"],
	])("maps %s to %s", (input, expected) => {
		expect(normalizeNodeUpdateChannel(input)).toBe(expected);
	});
});
