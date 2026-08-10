import { expect, test, type Page } from '@playwright/test';
import { mockApi } from './fixtures';

/**
 * The initial-load skeleton.
 *
 * The WAF page runs seven Logs Insights queries and the server builds its
 * panels one after another, so the first response is slow enough that a single
 * line of text reads as a hang.
 *
 * The line these hold is which load gets a skeleton. An empty screen gets one.
 * A refresh does not: the numbers already on screen stay, because replacing
 * readable data with grey boxes to announce that newer data is coming is worse
 * than saying nothing at all.
 */

/**
 * Delays every page fetch.
 *
 * Registered after mockApi on purpose — Playwright matches the most recently
 * registered handler first, so this one runs and then hands off to the fixture
 * with `fallback()`. fixtures.ts stays untouched.
 */
async function slowPages(page: Page, ms: number) {
	await page.route('**/api/page/**', async (route) => {
		await new Promise((r) => setTimeout(r, ms));
		await route.fallback();
	});
}

test('the first load shows a skeleton until the data arrives', async ({ page }) => {
	await mockApi(page);
	await slowPages(page, 1500);
	await page.goto('/logs/waf');

	// Present before any panel is.
	const skeleton = page.locator('[data-skeleton]');
	await expect(skeleton).toBeVisible();
	await expect(page.locator('[data-panel]')).toHaveCount(0);

	// And it gives way to the real panels rather than sitting alongside them.
	await expect(page.locator('[data-panel]').first()).toBeVisible({ timeout: 10_000 });
	await expect(skeleton).toHaveCount(0);
});

test('the controls are usable while the skeleton is up', async ({ page }) => {
	// The skeleton stands in for the panels, not for the whole page: the range
	// picker is what an operator reaches for when a load is taking too long.
	await mockApi(page);
	await slowPages(page, 1500);
	await page.goto('/logs/waf');

	await expect(page.locator('[data-skeleton]')).toBeVisible();
	await expect(page.locator('h1')).toHaveText('WAF');
	await expect(page.getByTestId('time-range-picker')).toBeVisible();
});

/**
 * The regression line for the rule the skeleton must not break.
 */
test('a refresh keeps the numbers on screen instead of showing a skeleton', async ({ page }) => {
	await mockApi(page);
	await page.goto('/');
	const stat = page.locator('[data-stat="pod.requests.total"] .value [data-value]');
	await expect(stat).toHaveText('12,840');

	// Now make the next response slow, and refresh.
	await slowPages(page, 1500);
	await page.getByTestId('time-range-picker').getByRole('button', { name: '새로고침' }).click();

	// Mid-flight: no skeleton, and the previous reading is still legible.
	await expect(page.locator('[data-skeleton]')).toHaveCount(0);
	await expect(stat).toHaveText('12,840');
	await expect(page.locator('[data-panel]').first()).toBeVisible();
});

test('changing the range also keeps the old data rather than blanking', async ({ page }) => {
	await mockApi(page);
	await page.goto('/');
	await expect(page.locator('[data-panel]').first()).toBeVisible();

	await slowPages(page, 1500);
	await page.getByTestId('time-range-picker').locator('button', { hasText: '4h' }).click();

	await expect(page.locator('[data-skeleton]')).toHaveCount(0);
	await expect(page.locator('[data-panel]').first()).toBeVisible();
});
