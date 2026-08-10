import { expect, test, type Page } from '@playwright/test';
import { mockApi } from './fixtures';
import { MAX_TOOLTIP_ROWS } from '../src/lib/chart';

/**
 * The hover readout.
 *
 * Hovering a line chart used to do nothing at all: uPlot's live readout is its
 * legend, and the legend is switched off so long pod names can wrap in HTML
 * rather than being clipped by the canvas. These hold the replacement to what
 * it is for — say what the value is, at the point under the cursor, for every
 * series at once, on the one chart being pointed at.
 */

async function open(page: Page, path = '/') {
	await mockApi(page);
	await page.goto(path);
	await expect(page.locator('h1')).toBeVisible();
	await page.locator('[data-panel]').first().waitFor();
	await page.locator('[data-chart] canvas').first().waitFor();
}

/** Hovers the middle of the first chart and returns its tooltip. */
async function hoverFirstChart(page: Page) {
	const chart = page.locator('.chart-wrap').first();
	const box = await chart.boundingBox();
	if (!box) throw new Error('the chart has no box');
	await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
	return chart.locator('.tip');
}

test('hovering a line chart reports the value under the cursor', async ({ page }) => {
	await open(page);
	const tip = await hoverFirstChart(page);

	await expect(tip).toBeVisible();
	// A timestamp, and at least one series reading.
	await expect(tip.locator('.tip-time')).not.toBeEmpty();
	expect(await tip.locator('.tip-row').count()).toBeGreaterThan(0);
	await expect(tip.locator('.tip-row .vl').first()).not.toBeEmpty();
});

test('the readout covers every series at that point in time, not just one', async ({ page }) => {
	await open(page);
	const tip = await hoverFirstChart(page);
	await expect(tip).toBeVisible();

	// One row per series drawn in this panel — the whole point of a shared time
	// axis is comparing the series against each other at one instant — up to
	// the cap, past which the readout would cover the chart it describes.
	const seriesCount = await page
		.locator('.chart-wrap')
		.first()
		.locator('..')
		.locator('.legend li')
		.count();
	const rows = await tip.locator('.tip-row').count();
	expect(rows).toBe(Math.min(seriesCount, MAX_TOOLTIP_ROWS));

	// Capped silently, ten rows out of forty read as "these are the pods".
	if (seriesCount > MAX_TOOLTIP_ROWS) {
		await expect(tip.locator('.tip-more')).toHaveText(`외 ${seriesCount - MAX_TOOLTIP_ROWS}개`);
	} else {
		await expect(tip.locator('.tip-more')).toBeHidden();
	}
});

test('a chart with more series than the cap does not bury itself', async ({ page }) => {
	// The pod resource panel is the one that provoked this: one line per pod
	// per metric, against a 220px chart.
	await open(page, '/infra/kubernetes');
	const chart = page.locator('[data-panel="pod-cpu"] .chart-wrap').first();
	const box = await chart.boundingBox();
	if (!box) throw new Error('the chart has no box');

	await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
	const tip = chart.locator('.tip');
	await expect(tip).toBeVisible();

	expect(await tip.locator('.tip-row').count()).toBeLessThanOrEqual(MAX_TOOLTIP_ROWS);
	const tipBox = await tip.boundingBox();
	expect(tipBox?.height ?? 0).toBeLessThan(box.height);
});

/**
 * cursor.sync moves the crosshair on every chart on the page, which is wanted.
 * A tooltip on every chart is not: the overview has six panels, and pointing at
 * one of them used to be able to pop a readout on all of them.
 */
test('only the chart under the pointer explains itself', async ({ page }) => {
	await open(page);
	await hoverFirstChart(page);

	const visible = page.locator('.tip:visible');
	await expect(visible).toHaveCount(1);
});

test('the readout disappears when the pointer leaves the chart', async ({ page }) => {
	await open(page);
	const tip = await hoverFirstChart(page);
	await expect(tip).toBeVisible();

	// Somewhere well away from any plot area.
	await page.mouse.move(2, 2);
	await expect(tip).toBeHidden();
});

test('a gap reads as a gap in the readout, never as a zero', async ({ page }) => {
	// The fixture puts a null in every fifth bucket. Sweeping the width of the
	// chart must land on one of them, and it has to render as a dash: the line
	// breaks there rather than drawing through it, and the numbers beside it
	// have to make the same distinction.
	await open(page);
	const chart = page.locator('.chart-wrap').first();
	const box = await chart.boundingBox();
	if (!box) throw new Error('the chart has no box');
	const tip = chart.locator('.tip');

	const readings = new Set<string>();
	for (let i = 1; i < 40; i++) {
		await page.mouse.move(box.x + (box.width * i) / 40, box.y + box.height / 2);
		for (const text of await tip.locator('.tip-row .vl').allInnerTexts()) {
			readings.add(text.trim());
		}
	}
	expect(readings).toContain('—');
});

test('the readout stays inside the panel at the right-hand edge', async ({ page }) => {
	await open(page);
	const chart = page.locator('.chart-wrap').first();
	const box = await chart.boundingBox();
	// The plot area, not the wrapper: the axis gutter beside it is not part of
	// the chart, and the cursor is deliberately inert there.
	const plot = await chart.locator('.u-over').boundingBox();
	if (!box || !plot) throw new Error('the chart has no box');
	const tip = chart.locator('.tip');

	await page.mouse.move(plot.x + plot.width - 2, plot.y + plot.height / 2);
	await expect(tip).toBeVisible();

	const tipBox = await tip.boundingBox();
	// It flips to the other side of the cursor rather than hanging off the
	// panel, where the panel's own overflow would cut it in half.
	expect((tipBox?.x ?? 0) + (tipBox?.width ?? 0)).toBeLessThanOrEqual(box.x + box.width + 1);
});

test('the axis gutter beside the plot shows no readout', async ({ page }) => {
	// uPlot's cursor is only meaningful over the plot area. Pointing at the
	// y-axis labels must not pin the readout to the nearest edge sample.
	await open(page);
	const chart = page.locator('.chart-wrap').first();
	const box = await chart.boundingBox();
	const plot = await chart.locator('.u-over').boundingBox();
	if (!box || !plot) throw new Error('the chart has no box');

	await page.mouse.move(box.x + 4, plot.y + plot.height / 2);
	await expect(chart.locator('.tip')).toBeHidden();
});
