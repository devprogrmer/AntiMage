import path from "node:path";
import { fileURLToPath } from "node:url";
import { copyFile } from "node:fs/promises";

import react from "@vitejs/plugin-react";
import { visualizer } from "rollup-plugin-visualizer";
import { build, loadEnv, splitVendorChunkPlugin } from "vite";
import svgr from "vite-plugin-svgr";
import tsconfigPaths from "vite-tsconfig-paths";

const dashboardDir = path.resolve(
	path.dirname(fileURLToPath(import.meta.url)),
	"..",
);

const tutorialDirectoryIndex = {
	name: "tutorial-directory-index",
	configureServer(server) {
		server.middlewares.use((request, _response, next) => {
			const url = new URL(request.url || "/", "http://localhost");
			if (
				url.pathname.startsWith("/tutorial-content/") &&
				url.pathname.endsWith("/")
			) {
				request.url = `${url.pathname}index.html${url.search}`;
			}
			next();
		});
	},
};

const getApiProxyConfig = (baseAPI) => {
	if (!baseAPI || !/^https?:\/\//i.test(baseAPI)) {
		return undefined;
	}

	try {
		const parsed = new URL(baseAPI);
		const proxyPath =
			parsed.pathname && parsed.pathname !== "/"
				? parsed.pathname.replace(/\/$/, "")
				: "/api";
		const target = `${parsed.protocol}//${parsed.host}`;
		const rewrite =
			parsed.pathname && parsed.pathname !== "/"
				? undefined
				: (requestPath) => requestPath.replace(/^\/api(?=\/|$)/, "");

		return {
			proxyPath,
			options: {
				target,
				changeOrigin: true,
				secure: true,
				rewrite,
			},
		};
	} catch {
		return undefined;
	}
};

const mode = process.env.NODE_ENV || "production";
const env = loadEnv(mode, dashboardDir, "");
const apiProxy = getApiProxyConfig(env.VITE_BASE_API);

await build({
	root: dashboardDir,
	configFile: false,
	envDir: dashboardDir,
	plugins: [
		tutorialDirectoryIndex,
		tsconfigPaths({ root: dashboardDir }),
		react({
			include: "**/*.tsx",
		}),
		svgr(),
		...(env.ANALYZE === "true" ? [visualizer()] : []),
		splitVendorChunkPlugin(),
	],
	resolve: {
		alias: {
			"@": path.join(dashboardDir, "src"),
		},
	},
	server: apiProxy
		? {
				proxy: {
					[apiProxy.proxyPath]: apiProxy.options,
				},
			}
		: undefined,
	build: {
		outDir: "build",
		assetsDir: "statics",
		emptyOutDir: true,
		rollupOptions: {
			onwarn(warning, warn) {
				if (
					typeof warning.message === "string" &&
					warning.message.includes(
						"Module level directives cause errors when bundled",
					)
				) {
					return;
				}
				warn(warning);
			},
		},
	},
});

await copyFile(
	path.join(dashboardDir, "build", "index.html"),
	path.join(dashboardDir, "build", "404.html"),
);
