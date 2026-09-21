import { afterEach, describe, expect, it, vi } from "vitest";

import { importAntiMageBackup, importVPNUIBackup } from "./settings";

class UploadRequest {
	static current: UploadRequest;
	status = 200;
	response = {
		scope: "database",
		tables_restored: 1,
		rows_restored: 2,
		files_restored: [],
		warnings: [],
	};
	responseType = "";
	withCredentials = false;
	url = "";
	body: FormData | null = null;
	onload: (() => void) | null = null;
	onerror: (() => void) | null = null;
	upload = {
		onprogress: null as ((event: ProgressEvent) => void) | null,
		onload: null as (() => void) | null,
	};

	constructor() {
		UploadRequest.current = this;
	}

	open(_method: string, url: string) {
		this.url = url;
	}

	send(body?: Document | XMLHttpRequestBodyInit | null) {
		this.body = body instanceof FormData ? body : null;
		this.upload.onprogress?.({
			lengthComputable: true,
			loaded: 5,
			total: 10,
		} as ProgressEvent);
		this.upload.onload?.();
		this.onload?.();
	}
}

describe("importAntiMageBackup", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("reports upload progress before resolving the restore response", async () => {
		vi.stubGlobal("XMLHttpRequest", UploadRequest);
		const progress: number[] = [];

		const result = await importAntiMageBackup(
			new File(["backup"], "test.rbbackup"),
			(percent) => progress.push(percent),
		);

		expect(progress).toEqual([0, 50, 100]);
		expect(UploadRequest.current.url).toBe("/api/settings/backup/import");
		expect(UploadRequest.current.withCredentials).toBe(true);
		expect(result.rows_restored).toBe(2);
	});
});

describe("importVPNUIBackup", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("uploads the source database with destination and duplicate policy", async () => {
		vi.stubGlobal("XMLHttpRequest", UploadRequest);
		UploadRequest.prototype.response = {
			detected: 1,
			imported: 1,
			skipped: 0,
			renamed: 0,
			warnings: [],
		} as any;

		await importVPNUIBackup(new File(["sqlite"], "vpn-ui.db"), 12, "rename");

		expect(UploadRequest.current.url).toBe("/api/settings/backup/import/vpn-ui");
		expect(UploadRequest.current.body?.get("service_id")).toBe("12");
		expect(UploadRequest.current.body?.get("duplicate_policy")).toBe("rename");
		expect((UploadRequest.current.body?.get("file") as File).name).toBe("vpn-ui.db");
	});
});
