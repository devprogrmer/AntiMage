import type { CreateToastFnReturn } from "@chakra-ui/react";
import i18n from "locales/i18n";
import type { FieldValues, UseFormReturn } from "react-hook-form";

type PanelErrorKind =
	| "network"
	| "authentication"
	| "permission"
	| "validation"
	| "conflict"
	| "server"
	| "unknown";

const classifyPanelError = (
	status: number | undefined,
	message: string,
): PanelErrorKind => {
	const value = message.toLowerCase();
	if (
		/failed to fetch|network error|connection refused|timed? ?out|timeout|load failed/.test(
			value,
		)
	) {
		return "network";
	}
	if (
		status === 401 ||
		/unauthorized|session expired|not authenticated/.test(value)
	) {
		return "authentication";
	}
	if (status === 403 || /forbidden|permission denied|not allowed/.test(value)) {
		return "permission";
	}
	if (
		status === 400 ||
		status === 422 ||
		/invalid|validation|required field/.test(value)
	) {
		return "validation";
	}
	if (status === 409 || /conflict|already exists|already in use/.test(value)) {
		return "conflict";
	}
	if (status !== undefined && status >= 500) return "server";
	return "unknown";
};

const errorResponse = (error: unknown) => {
	if (!error || typeof error !== "object" || !("response" in error)) {
		return { status: undefined, body: undefined as unknown };
	}
	const response = (
		error as { response?: { status?: number; _data?: unknown; data?: unknown } }
	).response;
	return {
		status: response?.status,
		body: response?._data ?? response?.data,
	};
};

const errorMessage = (error: unknown, body: unknown) => {
	if (typeof body === "string") return body;
	if (body && typeof body === "object") {
		const value = body as { detail?: unknown; message?: unknown };
		if (typeof value.detail === "string") return value.detail;
		if (typeof value.message === "string") return value.message;
	}
	if (error instanceof Error && error.message.trim()) return error.message;
	return i18n.t("panelDiagnostics.genericMessage");
};

const diagnosticDescription = (kind: PanelErrorKind, message: string) =>
	[
		i18n.t(`panelDiagnostics.${kind}.cause`),
		"",
		[1, 2, 3]
			.map((step) => `• ${i18n.t(`panelDiagnostics.${kind}.step${step}`)}`)
			.join("\n"),
		"",
		`${i18n.t("panelDiagnostics.technicalDetails")}: ${message}`,
	].join("\n");

export const generateErrorMessage = (
	e: unknown,
	toast: CreateToastFnReturn,
	form?: UseFormReturn<FieldValues | any>,
) => {
	const { status, body } = errorResponse(e);
	const detail =
		body && typeof body === "object"
			? (body as { detail?: unknown }).detail
			: undefined;
	if (detail && typeof detail === "object" && form) {
		Object.entries(detail as Record<string, unknown>).forEach(
			([errorKey, value]) => {
				if (typeof value === "string") {
					form.setError(errorKey, { message: value });
				}
			},
		);
		return;
	}
	const message = errorMessage(e, body);
	const kind = classifyPanelError(status, message);
	return toast({
		title: i18n.t(`panelDiagnostics.${kind}.title`),
		description: diagnosticDescription(kind, message),
		status: "error",
		isClosable: true,
		position: "top",
		duration: 12000,
	});
};

export const generateSuccessMessage = (
	message: string,
	toast: CreateToastFnReturn,
) => {
	return toast({
		title: message,
		status: "success",
		isClosable: true,
		position: "top",
		duration: 3000,
	});
};

