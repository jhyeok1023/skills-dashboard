import { expect, test, type Page } from '@playwright/test';
import { mockApi } from './fixtures';

/**
 * The traffic check.
 *
 * Every other screen reads CloudWatch, which lags by minutes and cannot tell an
 * empty panel apart from a dead service. This one asks the service — and only
 * when asked to. That last part is what these hold: nothing on the page, and
 * nothing anywhere else in the app, may send a probe on its own.
 */

async function open(page: Page) {
	await mockApi(page);
	await page.goto('/check');
	await expect(page.locator('h1')).toBeVisible();
}

test('no probe is sent until the operator asks for one', async ({ page }) => {
	let probes = 0;
	await mockApi(page);
	await page.route('**/api/check', async (route) => {
		probes++;
		await route.fallback();
	});

	// Every screen, not just this one: a badge in the top bar would probe on
	// each page load, which is traffic nobody asked to send.
	for (const path of ['/', '/logs/waf', '/check', '/settings']) {
		await page.goto(path);
		await expect(page.locator('h1')).toBeVisible();
	}
	expect(probes).toBe(0);

	await page.goto('/check');
	await page.getByRole('button', { name: '지금 점검' }).click();
	await expect.poll(() => probes).toBe(1);
});

test('a probe reports the status, the time it took and what it was compared against', async ({
	page
}) => {
	await open(page);
	await page.getByRole('button', { name: '지금 점검' }).click();

	const latest = page.getByTestId('latest');
	await expect(latest).toBeVisible();
	await expect(latest).toContainText('정상');
	await expect(latest).toContainText('200');
	await expect(latest).toContainText('143ms');
	// A red result cannot be read without knowing what "healthy" meant.
	await expect(page.locator('.panel')).toContainText('2xx');
});

test('a failed probe reads as a failure, not as an error page', async ({ page }) => {
	await mockApi(page);
	await page.route('**/api/check', (route) =>
		route.fulfill({
			status: 200,
			contentType: 'application/json',
			body: JSON.stringify({
				url: 'https://api.example.com/health',
				ok: false,
				elapsedMs: 10_002,
				at: new Date().toISOString(),
				error: 'dial tcp 203.0.113.7:443: i/o timeout',
				expect: '2xx'
			})
		})
	);
	await page.goto('/check');
	await page.getByRole('button', { name: '지금 점검' }).click();

	// The endpoint failing and the target failing are different facts.
	const latest = page.getByTestId('latest');
	await expect(latest).toContainText('응답 없음');
	await expect(latest).toContainText('i/o timeout');
	await expect(page.locator('.card.error')).toHaveCount(0);
});

test('with no target configured the page says so instead of offering a button', async ({
	page
}) => {
	await mockApi(page);
	await page.route('**/api/config', async (route) => {
		const res = await route.fetch();
		const body = await res.json();
		body.check = { url: '', expectStatus: 0 };
		await route.fulfill({ response: res, json: body });
	});
	await page.goto('/check');

	await expect(page.getByText('점검할 주소가 없습니다')).toBeVisible();
	await expect(page.getByRole('button', { name: '지금 점검' })).toHaveCount(0);
});
