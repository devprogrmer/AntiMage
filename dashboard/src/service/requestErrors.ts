export type RequestErrorRecord = {
	request_id: string | null;
	endpoint: string;
	method: string;
	status: number | null;
	operation: string;
	sanitized_message: string;
	timestamp: string;
};

const records: RequestErrorRecord[] = [];
let snapshot: RequestErrorRecord[] = [];
const listeners = new Set<() => void>();
const MAX_RECORDS = 100;

const publish = () => {
	snapshot = records.slice();
	listeners.forEach((listener) => {
		listener();
	});
};

const redact = (value: string) => value
	.replace(/(bearer\s+)[a-z0-9._~+/-]+=*/gi, "$1[redacted]")
	.replace(/(password|private[_ -]?key|token|secret|authorization|cookie)(\s*[=:]\s*)[^\s,;]+/gi, "$1$2[redacted]")
	.replace(/([?&](?:token|key|secret|password|auth)=)[^&#\s]+/gi, "$1[redacted]")
	.slice(0, 500);

export const sanitizeRequestErrorMessage = (value: unknown) => {
	if (typeof value !== "string") return "Request failed";
	return redact(value.replace(/[\r\n\t]+/g, " ")).trim() || "Request failed";
};

export const recordRequestError = (record: RequestErrorRecord) => {
	records.unshift({ ...record, sanitized_message: sanitizeRequestErrorMessage(record.sanitized_message) });
	if (records.length > MAX_RECORDS) records.length = MAX_RECORDS;
	publish();
};

export const getRequestErrors = () => snapshot;
export const subscribeRequestErrors = (listener: () => void) => {
	listeners.add(listener);
	return () => listeners.delete(listener);
};

export const clearRequestErrors = () => {
	records.length = 0;
	publish();
};

export const requestErrorOperation = (method: string, endpoint: string) => {
	const path = endpoint.split("?")[0].replace(/\/[0-9]+(?=\/|$)/g, "/:id");
	return `${method.toUpperCase()} ${path}`;
};
