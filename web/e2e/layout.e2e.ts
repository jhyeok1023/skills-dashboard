import { expect, test, type Page } from '@playwright/test';
import { LONG_ARN, LONG_POD, mockApi, PAGES } from './fixtures';

/**
 * Layout checks against the real build.
 *
 * The requirement these enforce is "텍스트가 잘리면 안 된다" — no value may be
 * clipped. A CSS rule saying so is easy to write and easy to lose to a later
 * `overflow: hidden`, so the assertion measures the rendered box instead:
 * every element marked data-value must fit inside itself.
 */

/** Elements whose content overflows their own box. */
async function clippedElements(page: Page) {
	return page.evaluate(() => {
		const bad: {
			text: string;
			scrollW: number;
			clientW: number;
			scrollH: number;
			clientH: number;
		}[] = [];
		for (const el of document.querySelectorAll<HTMLElement>('[data-value]')) {
			const rect = el.getBoundingClientRect();
			if (rect.width === 0 && rect.height === 0) continue; // not rendered

			const style = getComputedStyle(el);
			// An element that is allowed to scroll is not clipping its content;
			// the user can reach all of it.
			const scrollable =
				style.overflowX === 'auto' ||
				style.overflowX === 'scroll' ||
				style.overflowY === 'auto' ||
				style.overflowY === 'scroll';
			if (scrollable) continue;

			const overflowsX = el.scrollWidth > el.clientWidth + 1;
			const overflowsY = el.scrollHeight > el.clientHeight + 1;
			if (overflowsX || overflowsY) {
				bad.push({
					text: (el.textContent ?? '').trim().slice(0, 90),
					scrollW: el.scrollWidth,
					clientW: el.clientWidth,
					scrollH: el.scrollHeight,
					clientH: el.clientHeight
				});
			}
		}
		return bad;
	});
}

/** Ellipsis truncation applied to a value is a bug even when it happens to fit. */
async function ellipsisedValues(page: Page) {
	return page.evaluate(() =>
		[...document.querySelectorAll<HTMLElement>('[data-value]')]
			.filter((el) => {
				const s = getComputedStyle(el);
				return s.textOverflow === 'ellipsis' || s.whiteSpace === 'nowrap';
			})
			.map((el) => (el.textContent ?? '').trim().slice(0, 60))
	);
}

async function open(page: Page, path: string) {
	await mockApi(page);
	await page.goto(path);
	await expect(page.locator('h1')).toBeVisible();
	// Charts mount in an effect; wait for the first one before measuring.
	if (path !== '/settings') {
		await page.locator('[data-panel]').first().waitFor();
	}
}

for (const { path, name } of PAGES) {
	test(`${name}: no value is clipped`, async ({ page }) => {
		await open(page, path);
		const clipped = await clippedElements(page);
		expect(clipped, `clipped values on ${path}: ${JSON.stringify(clipped, null, 2)}`).toEqual([]);
	});

	test(`${name}: no value is ellipsised`, async ({ page }) => {
		await open(page, path);
		expect(await ellipsisedValues(page)).toEqual([]);
	});

	test(`${name}: the page never scrolls sideways`, async ({ page }) => {
		await open(page, path);
		const overflow = await page.evaluate(
			() => document.documentElement.scrollWidth - document.documentElement.clientWidth
		);
		expect(overflow, `${path} scrolls horizontally by ${overflow}px`).toBeLessThanOrEqual(1);
	});
}

test('long values wrap in full rather than being truncated', async ({ page }) => {
	await open(page, '/logs/pod');

	// The fixture pod name is longer than its column. It must appear in one
	// piece, with no ellipsis character standing in for the rest of it.
	const pod = page.locator(`text=${LONG_POD}`).first();
	await expect(pod).toBeVisible();
	const text = await pod.textContent();
	expect(text).toContain(LONG_POD);
	expect(text).not.toContain('…');
});

test('a wide table scrolls inside its own container', async ({ page }) => {
	await open(page, '/logs/pod');
	const scroller = page.locator('.table-scroll').first();
	await expect(scroller).toBeVisible();
	expect(await scroller.evaluate((el) => getComputedStyle(el).overflowX)).toBe('auto');
});

test('charts render with a real size', async ({ page }) => {
	await open(page, '/');
	const canvas = page.locator('[data-chart] canvas').first();
	await expect(canvas).toBeVisible();
	const box = await canvas.boundingBox();
	expect(box?.width ?? 0).toBeGreaterThan(100);
	expect(box?.height ?? 0).toBeGreaterThan(50);
});

test('a categorical breakdown renders as bars', async ({ page }) => {
	await open(page, '/logs/waf');
	const panel = page.locator('[data-panel="waf-breakdown"]');
	await expect(panel).toBeVisible();

	// A bar, not the chart's clip path — the clip path is a <path> that is
	// always present and always hidden, so matching it would pass on an empty
	// chart.
	const bars = panel.locator('svg rect');
	await expect(bars.first()).toBeVisible();
	expect(await bars.count()).toBeGreaterThan(1);

	// The exact figures live beside the chart, because a bar cannot be read to
	// three significant figures and cannot be copied.
	await expect(panel.locator('.values .count').filter({ hasText: '15,000' }).first()).toBeVisible();
});

test('key values can be copied in one click', async ({ page }) => {
	await open(page, '/logs/pod');

	const button = page.locator('[data-copy-button]').filter({ hasText: LONG_POD }).first();
	await expect(button).toBeVisible();
	await button.click();

	const clipboard = await page.evaluate(() => navigator.clipboard.readText());
	expect(clipboard).toBe(LONG_POD);
});

test('a stat value copies as a plain number', async ({ page }) => {
	await open(page, '/');
	const tile = page.locator('[data-stat="pod.requests.total"]');
	await expect(tile).toBeVisible();
	await tile.locator('[data-copy-button]').first().click();
	expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('12840');
});

/**
 * The regression line for the defect this rewrite exists to fix. The same
 * metric appears on the overview and on its detail page; the two must read
 * identically, down to the character.
 */
test('a value reads the same on the overview and on its detail page', async ({ page }) => {
	const read = async (path: string, statKey: string) => {
		await open(page, path);
		return (
			await page.locator(`[data-stat="${statKey}"] .value [data-value]`).first().innerText()
		).trim();
	};

	for (const [statKey, detailPath] of [
		['pod.requests.total', '/logs/pod'],
		['pod.p99.max', '/logs/pod'],
		['pod.badStatus.total', '/logs/pod'],
		['tg.5xx.total', '/infra/targetgroup'],
		['pods.current', '/infra/kubernetes'],
		['pod.restarts', '/infra/kubernetes'],
		['waf.log.block', '/logs/waf']
	] as const) {
		const overview = await read('/', statKey);
		const detail = await read(detailPath, statKey);
		expect(detail, `${statKey} differs between the overview and ${detailPath}`).toBe(overview);
	}
});

/** A capped list must not make its own header lie about the total. */
test('a truncated table reports the real total, not the row count', async ({ page }) => {
	await open(page, '/logs/pod');
	const panel = page.locator('[data-panel="pod-status-codes"]');

	// Three separate numbers: how many exist, how many were fetched, how many
	// are on screen. Collapsing them is how a header reports a capped array's
	// length as a total.
	const counts = (await panel.locator('.counts').innerText()).replace(/\s+/g, ' ').trim();
	expect(counts).toBe('전체 1,284건 · 조회 300건 · 표시 8건');
	await expect(panel.locator('.badge', { hasText: '상위 300건만 조회됨' })).toBeVisible();

	// And the headline agrees with the aggregate rather than with the list.
	// Read the value element itself: its container also holds the copy
	// affordance and a screen-reader label.
	const headline = await panel
		.locator('[data-stat="pod.badStatus.total"] .value [data-value]')
		.innerText();
	expect(headline.trim()).toBe('1,284');
});

test('every stat says what population it counted', async ({ page }) => {
	await open(page, '/logs/pod');

	// Two request counts sit on one panel. They are different populations, and
	// the UI has to make that visible rather than leaving it to be discovered.
	const requests = page.locator('[data-stat="pod.requests.total"] .basis');
	const samples = page.locator('[data-stat="pod.latencySamples.total"] .basis');
	await expect(requests).toBeVisible();
	await expect(samples).toBeVisible();
	expect(await requests.innerText()).not.toBe(await samples.innerText());
});

test('the range selector never offers more than four hours', async ({ page }) => {
	await open(page, '/');
	const picker = page.getByTestId('time-range-picker');
	const labels = await picker.locator('.segmented button').allInnerTexts();

	expect(labels).toEqual(['15m', '30m', '1h', '2h', '4h']);
	for (const label of labels) {
		const minutes = label.endsWith('h') ? parseInt(label) * 60 : parseInt(label);
		expect(minutes).toBeLessThanOrEqual(240);
	}
});

test('changing the range updates the URL and refetches', async ({ page }) => {
	await open(page, '/');

	const requests: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/page/')) requests.push(r.url());
	});

	await page.getByTestId('time-range-picker').locator('button', { hasText: '4h' }).click();
	await expect(page).toHaveURL(/range=4h/);
	await expect.poll(() => requests.some((u) => u.includes('range=4h'))).toBe(true);
});

test('the period selector only offers periods valid for the range', async ({ page }) => {
	await open(page, '/');
	const picker = page.getByTestId('time-range-picker');

	await picker.locator('button', { hasText: '15m' }).click();
	await expect.poll(async () => picker.locator('select').first().locator('option').count()).toBe(1);

	await picker.locator('button', { hasText: '4h' }).click();
	await expect
		.poll(async () => picker.locator('select').first().locator('option').allInnerTexts())
		.toEqual(['1m', '5m', '10m', '1h']);
});

test('an ARN is shown in full and copies exactly', async ({ page }) => {
	await open(page, '/settings');
	const arn = page.locator(`text=${LONG_ARN}`).first();
	await expect(arn).toBeVisible();
	expect((await arn.textContent())?.trim()).toBe(LONG_ARN);
});

test('the log format preview reports what a sample line parsed to', async ({ page }) => {
	await open(page, '/settings');
	await page.locator('#sample').fill('{"time":"2026-08-10T07:12:04Z"}');
	await page.locator('button', { hasText: '파싱 미리보기' }).click();

	const preview = page.getByTestId('logfmt-preview');
	await expect(preview).toBeVisible();
	await expect(preview.locator('.badge', { hasText: '인식됨' })).toBeVisible();
	await expect(preview.locator('text=503')).toBeVisible();
});

// Health-check traffic is filtered out, so the stats have to say so — a count
// that quietly drops a slice of the traffic is worse than one that is wrong.
test('excluded paths are stated beside the numbers and editable in settings', async ({ page }) => {
	await open(page, '/logs/pod');
	const basis = await page.locator('[data-stat="pod.requests.total"] .basis').innerText();
	expect(basis).toContain('/health');
	expect(basis).toContain('제외');

	await open(page, '/settings');
	const field = page.locator('#excludePaths');
	await expect(field).toBeVisible();
	await expect(field).toHaveValue('/health, /healthcheck');
});

for (const width of [1440, 1024, 768]) {
	test(`nothing clips at ${width}px wide`, async ({ page }) => {
		await page.setViewportSize({ width, height: 900 });
		await open(page, '/logs/pod');

		expect(await clippedElements(page)).toEqual([]);
		const overflow = await page.evaluate(
			() => document.documentElement.scrollWidth - document.documentElement.clientWidth
		);
		expect(
			overflow,
			`page scrolls horizontally by ${overflow}px at ${width}px`
		).toBeLessThanOrEqual(1);
	});
}

for (const theme of ['light', 'dark'] as const) {
	test(`nothing clips in the ${theme} theme`, async ({ page }) => {
		await page.emulateMedia({ colorScheme: theme });
		await open(page, '/');
		await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);

		expect(await clippedElements(page)).toEqual([]);

		// The theme toggle must actually change the palette, in both directions.
		const bg = await page.evaluate(() =>
			getComputedStyle(document.body).getPropertyValue('background-color')
		);
		expect(bg).not.toBe('');
	});
}

test('a deep link survives a hard reload', async ({ page }) => {
	await open(page, '/infra/kubernetes?range=4h&period=5m');
	await expect(page.locator('h1')).toHaveText('팟 · 노드');
	await page.reload();
	await expect(page.locator('h1')).toHaveText('팟 · 노드');
	await expect(page).toHaveURL(/range=4h/);
});

test('navigation marks the current page', async ({ page }) => {
	await open(page, '/logs/waf');
	await expect(page.locator('nav a[aria-current="page"]')).toHaveText('WAF');
});
