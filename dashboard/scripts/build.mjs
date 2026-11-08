import path from "node:path";
import { fileURLToPath } from "node:url";
import { copyFile, readFile, readdir, writeFile } from "node:fs/promises";
import { promisify } from "node:util";
import { gzip } from "node:zlib";

import react from "@vitejs/plugin-react";
import { visualizer } from "rollup-plugin-visualizer";
import { build, loadEnv, splitVendorChunkPlugin } from "vite";
import svgr from "vite-plugin-svgr";
import tsconfigPaths from "vite-tsconfig-paths";

const dashboardDir = path.resolve(
	path.dirname(fileURLToPath(import.meta.url)),
	"..",
);

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
const compress = promisify(gzip);

await build({
	root: dashboardDir,
	configFile: false,
	envDir: dashboardDir,
	plugins: [
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

const assetsDir = path.join(dashboardDir, "build", "statics");
for (const name of await readdir(assetsDir, { recursive: true })) {
	if (!/\.(?:js|css|json|svg)$/.test(name)) continue;
	const file = path.join(assetsDir, name);
	const original = await readFile(file);
	if (original.length < 1024) continue;
	const compressed = await compress(original);
	if (compressed.length < original.length) {
		await writeFile(file + ".gz", compressed);
	}
}
