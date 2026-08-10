import { defineConfig } from 'vitest/config';
import { playwright } from '@vitest/browser-playwright';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';

export default defineConfig({
	plugins: [
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
