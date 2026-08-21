import { describe, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import DataTable from './DataTable.svelte';
import type { Table } from '$lib/types';

const UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 curl-detail-probe';

/** One WAF-shaped row: a couple of columns, and two that only the detail shows. */
function table(overrides: Partial<Table> = {}): Table {
	return {
		columns: [
			{ key: 'uri', label: '경로', mono: true },
			{ key: 'action', label: '처리' },
			{ key: 'userAgent', label: 'User-Agent', detail: true, mono: true },
			{ key: 'responseCode', label: '응답 코드', detail: true }
		],
		rows: [
			{
				uri: '/v1/user',
				action: 'BLOCK',
				userAgent: UA,
				responseCode: '403 · WAF 기본 차단 응답 (로그에 코드가 기록되지 않음)'
			}
		],
		total: 1,
		truncated: false,
		limit: 300,
		...overrides
	};
}

describe('DataTable row detail', () => {
	it('keeps detail columns out of the header', async () => {
		const screen = render(DataTable, { table: table() });

		await expect.element(screen.getByText('경로')).toBeInTheDocument();
		// A User-Agent given a column of its own squeezes every other one flat.
		const head = screen.container.querySelector('thead');
		expect(head?.querySelectorAll('th')).toHaveLength(3); // expander + 경로 + 처리
		expect(head?.textContent).not.toContain('User-Agent');
		expect(head?.textContent).not.toContain('응답 코드');
	});

	it('reveals the detail values on request and says so to assistive technology', async () => {
		const screen = render(DataTable, { table: table() });
		const button = screen.getByRole('button', { name: /상세/ });

		await expect.element(button).toHaveAttribute('aria-expanded', 'false');
		expect(screen.container.textContent).not.toContain(UA);

		await button.click();

		await expect.element(button).toHaveAttribute('aria-expanded', 'true');
		const detail = screen.container.querySelector('tr.detail');
		expect(detail?.textContent).toContain(UA);
		// The honest answer about the response code is the whole point of the
		// detail on a WAF row, so it has to survive the trip through `cell()`
		// rather than being coerced into a number.
		expect(detail?.textContent).toContain('기록되지 않음');
	});

	it('removes the detail row rather than hiding it', async () => {
		// A panel renders twice — in its card and in its expand dialog — and the
		// two are asserted to hold the same number of rows. A detail row left in
		// the DOM under `display: none` would break that quietly.
		const screen = render(DataTable, { table: table() });
		const button = screen.getByRole('button', { name: /상세/ });

		await button.click();
		expect(screen.container.querySelectorAll('tr.detail')).toHaveLength(1);

		await button.click();
		expect(screen.container.querySelectorAll('tr.detail')).toHaveLength(0);
	});

	it('offers no expander when a table declares nothing to reveal', async () => {
		// An aggregate row is already its own summary. The view decides this from
		// the payload alone, never from a panel's name.
		const aggregate = table({
			columns: [
				{ key: 'rule', label: '규칙' },
				{ key: 'count', label: '건수', numeric: true }
			],
			rows: [{ rule: 'sqli', count: 60 }],
			total: 60
		});
		const screen = render(DataTable, { table: aggregate });

		expect(screen.container.querySelector('td.expander')).toBeNull();
		expect(screen.container.querySelector('.reveal')).toBeNull();
	});

	it('finds a row by a value only the detail shows', async () => {
		// Detail values are formatted with every other cell and land in the row's
		// search text, so the filter reaches a User-Agent that has no column.
		const rows = [
			{ uri: '/a', action: 'ALLOW', userAgent: UA, responseCode: 'x' },
			{ uri: '/b', action: 'ALLOW', userAgent: 'kube-probe/1.29', responseCode: 'x' }
		];
		const screen = render(DataTable, {
			table: table({ rows, total: 2 }),
			initialRows: 1
		});

		await screen.getByRole('searchbox').fill('curl-detail-probe');

		const body = screen.container.querySelector('tbody');
		expect(body?.textContent).toContain('/a');
		expect(body?.textContent).not.toContain('/b');
	});
});
