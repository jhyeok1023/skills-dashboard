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

test('the row that was pressed is the row that unfolds', async ({ page }) => {
	// Rows 1 through 4 of the fixture are byte-identical — real access logs
	// produce runs like that — so keying the open row by its content alone
	// unfolded the first of them whichever one was pressed, leaving the pressed
	// control reading aria-expanded="false" and looking dead.
	await open(page);
	const panel = card(page);
	const buttons = panel.getByRole('button', { name: '상세' });

	await buttons.nth(3).click();

	await expect(buttons.nth(3)).toHaveAttribute('aria-expanded', 'true');
	await expect(buttons.nth(1)).toHaveAttribute('aria-expanded', 'false');
	await expect(panel.locator('tr.detail')).toHaveCount(1);
});

test('an aggregate table offers nothing to unfold', async ({ page }) => {
	// Its rows are already summaries; unfolding one would repeat the table back.
	// The breakdown, not the blocked list: this fixture page renders the
	// breakdown, and a locator for a panel that is not on the page passes this
	// assertion no matter what the component does.
	await open(page);
	const breakdown = page.locator('[data-panel="waf-breakdown"]');

	await expect(breakdown.locator('table')).toBeVisible();
	await expect(breakdown.getByRole('button', { name: '상세' })).toHaveCount(0);
});

/**
 * The same expander on the pod side, which is where a 404 or a 403 is actually
 * answered for. The fixture is a default install — no User-Agent field named —
 * because that is the configuration in which this used to offer nothing at all.
 */
async function openPod(page: Page) {
	await mockApi(page);
	await page.goto('/logs/pod');
	await expect(page.locator('h1')).toBeVisible();
	await page.locator('[data-panel]').first().waitFor();
}

test('a bad response unfolds without a User-Agent field being configured', async ({ page }) => {
	await openPod(page);
	const panel = page.locator('[data-panel="pod-status-codes"]');

	await expect(panel.locator('tr.detail')).toHaveCount(0);
	await panel.getByRole('button', { name: '상세' }).first().click();

	const detail = panel.locator('tr.detail');
	await expect(detail).toHaveCount(1);
	// From the Kubernetes envelope, so present whatever the log format says.
	await expect(detail).toContainText('컨테이너');
	await expect(detail).toContainText('product-api');
	await expect(detail).toContainText('네임스페이스');
});

test('a status code unfolds into the paths that produced it', async ({ page }) => {
	await openPod(page);
	const panel = page.locator('[data-panel="pod-status-breakdown"]');

	// A path list cannot fit in a cell, so it is not in one.
	await expect(panel.locator('thead')).not.toContainText('상위 경로');

	// The 404 row, not whichever row happens to be first: the point of the panel
	// is that each code answers for itself.
	await panel
		.getByRole('row')
		.filter({ hasText: '404' })
		.getByRole('button', { name: '상세' })
		.click();

	const detail = panel.locator('tr.detail');
	await expect(detail).toHaveCount(1);
	await expect(detail).toContainText('/favicon.ico (80건)');
	// Cut, and saying so. A silent cut reads as "these are the paths".
	await expect(detail).toContainText('외 21개');
});

test('the pod counts above the table do not move when a code is unfolded', async ({ page }) => {
	await openPod(page);
	const panel = page.locator('[data-panel="pod-status-breakdown"]');
	const counts = panel.locator('.counts');
	const before = await counts.textContent();

	await panel.getByRole('button', { name: '상세' }).first().click();
	await expect(panel.locator('tr.detail')).toHaveCount(1);

	expect(await counts.textContent()).toBe(before);
});
