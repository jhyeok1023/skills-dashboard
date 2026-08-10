import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
	testDir: 'e2e',
	testMatch: '**/*.e2e.{ts,js}',
	// The dashboard is a single-page app; `preview` serves the same build the
	// Go binary embeds, so what is asserted here is what ships.
	webServer: {
		command:
			'node .yarn/releases/yarn-4.18.0.cjs build && node .yarn/releases/yarn-4.18.0.cjs preview',
		port: 4173,
		// The build runs first, so the default 60s is not enough on a cold cache.
		timeout: 240_000,
		reuseExistingServer: !process.env.CI
	},
	use: {
		baseURL: 'http://localhost:4173',
		// The copy-to-clipboard assertions read the clipboard back.
		permissions: ['clipboard-read', 'clipboard-write']
	},
	projects: [
		{
			name: 'desktop',
			use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } }
		}
	],
	reporter: process.env.CI ? 'list' : [['list']],
	forbidOnly: !!process.env.CI
});
