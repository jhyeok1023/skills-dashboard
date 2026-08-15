import { expect, test, type Page } from '@playwright/test';
import { LONG_USER_AGENT, mockApi } from './fixtures';

/**
 * Unfolding one request.
 *
 * A WAF row is a real request, but the table can only afford eight columns of
 * it. These hold the detail to answering the questions that come next — who
 * sent this, calling itself what, and what the WAF actually replied — without
 * the row itself becoming something the table counts.
 */

async function open(page: Page) {
	await mockApi(page);
	await page.goto('/logs/waf');
	await expect(page.locator('h1')).toBeVisible();
	await page.locator('[data-panel]').first().waitFor();
}

function card(page: Page) {
	return page.locator('[data-panel="waf-traffic"]');
}

test('a request unfolds into what the columns had no room for', async ({ page }) => {
	await open(page);
	const panel = card(page);

	// Nothing of the detail is on screen until it is asked for.
	await expect(panel.locator('tr.detail')).toHaveCount(0);
	await expect(panel.getByText(LONG_USER_AGENT)).toHaveCount(0);

	await panel.getByRole('button', { name: '상세' }).first().click();

	const detail = panel.locator('tr.detail');
	await expect(detail).toHaveCount(1);
	await expect(detail).toContainText(LONG_USER_AGENT);
	// The honest answer about the response code, in full, not a number.
	await expect(detail).toContainText('기록되지 않음');
	// And the columns the table already showed, so the request is in one place.
	await expect(detail).toContainText('경로');
});

test('the counts above the table do not move when a row is unfolded', async ({ page }) => {
	await open(page);
	const panel = card(page);
	const counts = panel.locator('.counts');
	const before = await counts.textContent();

	await panel.getByRole('button', { name: '상세' }).first().click();
	await expect(panel.locator('tr.detail')).toHaveCount(1);

	// A detail row is not a request. Counting it would put the header's "표시
	// N건" one ahead of the rows it claims to describe.
	expect(await counts.textContent()).toBe(before);
});

test('unfolding one row does not unfold the identical rows beside it', async ({ page }) => {
	// The fixture is 300 rows that differ only in their action, so a detail keyed
	// by anything less than the whole row would open in several places at once.
	await open(page);
	const panel = card(page);

	await panel.getByRole('button', { name: '상세' }).first().click();
	await expect(panel.locator('tr.detail')).toHaveCount(1);
});

test('an aggregate table offers nothing to unfold', async ({ page }) => {
	// Its rows are already summaries; unfolding one would repeat the table back.
	await open(page);
	const blocked = page.locator('[data-panel="waf-blocked"]');

	await expect(blocked.getByRole('button', { name: '상세' })).toHaveCount(0);
});
