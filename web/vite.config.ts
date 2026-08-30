import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { defineConfig } from 'vitest/config';
import { playwright } from '@vitest/browser-playwright';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';

const webDir = dirname(fileURLToPath(import.meta.url));

// Record which node_modules packages the shipped SPA actually pulls in, so
// scripts/licenses.mjs can attribute exactly what ships and nothing more.
//
// The install tree cannot answer this. package-lock marks vite, rolldown and
// lightningcss as non-dev (140 packages), so a lockfile walk would credit build
// tooling that never reaches a user.
//
// Only the client build counts. SvelteKit runs two — the SSR build exists to
// prerender, and adapter-static ships its HTML plus the client assets, never the
// server bundle itself. The two graphs differ: the SSR build externalises plain
// JS dependencies (d3-* and friends come through as bare specifiers), so
// recording it would both miss what ships and credit what does not.
//
// generateBundle is the source, not buildEnd — getModuleIds() hands back
// unresolved external ids that carry no path to a package.
//
// The output goes outside ../internal/web/dist on purpose: anything written in
// there is embedded into both engines and served.
function recordBundledModules() {
	const ids = new Set<string>();
	let ssr = false;
	return {
		name: 'record-bundled-modules',
		apply: 'build' as const,
		configResolved(config: { build?: { ssr?: boolean | string } }) {
			ssr = Boolean(config.build?.ssr);
		},
		generateBundle(_options: unknown, bundle: Record<string, { moduleIds?: string[] }>) {
			if (ssr) return;
			for (const chunk of Object.values(bundle)) {
				for (const id of chunk.moduleIds ?? []) ids.add(id);
			}
		},
		closeBundle() {
			if (ssr || ids.size === 0) return;
			const out = resolve(webDir, '.licenses/spa-modules.json');
			mkdirSync(dirname(out), { recursive: true });
			writeFileSync(out, JSON.stringify([...ids].sort(), null, '\t'));
		}
	};
}

export default defineConfig({
	plugins: [
		recordBundledModules(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// The build lands directly where the Go binary embeds from, so there
			// is no copy step between `web:build` and `go:build` to forget.
			// `fallback` makes this a single-page app: the Go handler serves
			// index.html for any path it has no file for, which is what lets
			// /logs/pod survive a hard refresh.
			adapter: adapter({
				pages: '../internal/web/dist',
				assets: '../internal/web/dist',
				fallback: 'index.html',
				strict: false
			})
		})
	],
	server: {
		// In development the UI runs on Vite and the API on the Go process.
		proxy: {
			'/api': {
				target: 'http://127.0.0.1:8080',
				changeOrigin: false
			}
		}
	},
	test: {
		expect: { requireAssertions: true },
		projects: [
			{
				extends: './vite.config.ts',
				test: {
					name: 'client',
					browser: {
						enabled: true,
						provider: playwright(),
						instances: [{ browser: 'chromium', headless: true }]
					},
					include: ['src/**/*.svelte.{test,spec}.{js,ts}'],
					exclude: ['src/lib/server/**']
				}
			},

			{
				extends: './vite.config.ts',
				test: {
					name: 'server',
					environment: 'node',
					include: ['src/**/*.{test,spec}.{js,ts}'],
					exclude: ['src/**/*.svelte.{test,spec}.{js,ts}']
				}
			}
		]
	}
});
