import { describe, expect, it } from "vitest";

import en from "../../public/statics/locales/en.json";
import fa from "../../public/statics/locales/fa.json";
import ru from "../../public/statics/locales/ru.json";
import zh from "../../public/statics/locales/zh.json";

describe("global settings save message", () => {
	it("does not report unrelated Telegram settings when any settings are saved", () => {
		for (const message of [
			en["settings.savedSuccess"],
			fa["settings.savedSuccess"],
			ru["settings.savedSuccess"],
			zh["settings.savedSuccess"],
		]) {
			expect(message.toLowerCase()).not.toContain("telegram");
		}
	});
});
