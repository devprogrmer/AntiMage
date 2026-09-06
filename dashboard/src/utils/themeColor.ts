export const updateThemeColor = (themeName: string, fallback?: string) => {
	const el = document.querySelector('meta[name="theme-color"]');
	const map: Record<string, string> = {
		dark: "#0b1118",
		light: "#f4f5f7",
		custom: fallback || "#0b1118",
	};
	const color = fallback || map[themeName] || map.dark;
	el?.setAttribute("content", color);
};
