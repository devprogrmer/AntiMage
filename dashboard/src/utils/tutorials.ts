export const normalizeTutorialLang = (lang?: string | null) => {
	const normalized = (lang || "en").toLowerCase();
	if (normalized.startsWith("fa")) return "fa";
	if (normalized.startsWith("ru")) return "ru";
	return "en";
};

const normalizeDashboardRoot = (root: string) => {
	const normalized = root.replace(/\/+$/, "");
	return normalized === "/" ? "" : normalized;
};

const tutorialContentPath = "tutorial-content";

export const getTutorialsUrl = (
	dashboardRoot: string,
	lang?: string | null,
	page = "",
) => {
	const root = normalizeDashboardRoot(dashboardRoot);
	const locale = normalizeTutorialLang(lang);
	const localePath = locale === "en" ? "" : `${locale}/`;
	return `${root}/${tutorialContentPath}/${localePath}docs/${page.replace(/^\/+/, "")}`;
};

export const getTutorialManifestUrl = (dashboardRoot: string) =>
	`${normalizeDashboardRoot(dashboardRoot)}/${tutorialContentPath}/manifest.json`;

export const getTutorialSeenKey = (lang?: string | null) =>
	`am-tutorials-seen-${normalizeTutorialLang(lang)}`;
