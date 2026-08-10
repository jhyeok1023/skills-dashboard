import { expect, test, type Page } from '@playwright/test';
import { mockApi } from './fixtures';

/**
 * Expanding a panel.
 *
 * The reference implementation had a compact grid variant and a bigger dialog
 * variant, and they disagreed — eight bars in one, thirty in the other, with
 * the bar scale computed from whatever was on screen. These hold the expanded
 * view to being the same panel drawn larger: same series, same rows, same
 * numbers, only more room.
 */

async function open(page: Page, path = '/logs/waf') {
	await mockApi(page);
	await page.goto(path);
	await expect(page.locator('h1')).toBeVisible();
	await page.locator('[data-panel]').first().waitFor();
}

function card(page: Page) {
	return page.locator('[data-panel="waf-traffic"]');
}

function expanded(page: Page) {
	return page.locator('[data-panel-expanded="waf-traffic"]');
}

test('a panel can be opened larger and closed again', async ({ page }) => {
	await open(page);
	await expect(expanded(page)).toBeHidden();

	await card(page).getByRole('button', { name: '크게 보기' }).click();
	await expect(expanded(page)).toBeVisible();

	await expanded(page).getByRole('button', { name: '닫기' }).click();
	await expect(expanded(page)).toBeHidden();
});

test('escape closes it', async ({ page }) => {
	await open(page);
	await card(page).getByRole('button', { name: '크게 보기' }).click();
	await expect(expanded(page)).toBeVisible();

	await page.keyboard.press('Escape');
	await expect(expanded(page)).toBeHidden();
});

test('clicking the backdrop closes it', async ({ page }) => {
	await open(page);
	await card(page).getByRole('button', { name: '크게 보기' }).click();
	const panel = expanded(page);
	await expect(panel).toBeVisible();

	// Just inside the viewport's top-left, which is backdrop rather than panel.
	await page.mouse.click(4, 4);
	await expect(panel).toBeHidden();
});

test('clicking inside it does not close it', async ({ page }) => {
	await open(page);
	await card(page).getByRole('button', { name: '크게 보기' }).click();
	const panel = expanded(page);
	await expect(panel).toBeVisible();

	await panel.locator('h2').click();
	await expect(panel).toBeVisible();
});

// The whole reason the expanded view is one snippet with one argument.
test('the expanded panel shows exactly what the card shows', async ({ page }) => {
	await open(page);
	const small = card(page);
	const stats = await small.locator('.stats > *').count();
	const legend = await small.locator('.legend li').count();
	const rows = await small.locator('tbody tr').count();

	await small.getByRole('button', { name: '크게 보기' }).click();
	const big = expanded(page);
	await expect(big).toBeVisible();

	expect(await big.locator('.stats > *').count()).toBe(stats);
	expect(await big.locator('.legend li').count()).toBe(legend);
	expect(await big.locator('tbody tr').count()).toBe(rows);
});

test('the chart is drawn taller than it was in the card', async ({ page }) => {
	await open(page);
	const small = card(page);
	const smallChart = await small.locator('[data-chart]').first().boundingBox();

	await small.getByRole('button', { name: '크게 보기' }).click();
	await expect(expanded(page)).toBeVisible();
	const bigChart = await expanded(page).locator('[data-chart]').first().boundingBox();

	expect(bigChart?.height ?? 0).toBeGreaterThan(smallChart?.height ?? 0);
});

/**
 * A modal that froze the data would be a modal you could read a stale number
 * off without being told it was stale.
 *
 * The refresh button itself is unreachable while the dialog is open, and that
 * is right — showModal() makes the page behind it inert on purpose. What has to
 * keep running is the poll, which does not need the page to be reachable.
 */
test('auto-refresh keeps running while a panel is expanded', async ({ page }) => {
	let pageRequests = 0;
	await page.clock.install();
	await mockApi(page);
	await page.route('**/api/page/**', async (route) => {
		pageRequests++;
		await route.fallback();
	});

	await page.goto('/logs/waf?range=1h&period=1m&refresh=30');
	await expect(page.locator('h1')).toBeVisible();
	await page.locator('[data-panel]').first().waitFor();

	await card(page).getByRole('button', { name: '크게 보기' }).click();
	await expect(expanded(page)).toBeVisible();

	const before = pageRequests;
	await page.clock.fastForward('00:31');
	await expect.poll(() => pageRequests).toBeGreaterThan(before);
	// And it is still open, having re-rendered against the newer payload.
	await expect(expanded(page)).toBeVisible();
});
