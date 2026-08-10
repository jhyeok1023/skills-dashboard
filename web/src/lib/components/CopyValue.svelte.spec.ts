import { describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import CopyValue from './CopyValue.svelte';

const LONG = 'product-api-deployment-7d9f8c6b5a-x2m4q-with-a-very-long-suffix-that-will-not-fit';

describe('CopyValue', () => {
	it('shows the whole value with no truncation applied', async () => {
		const screen = render(CopyValue, { value: LONG });
		const el = screen.getByText(LONG);
		await expect.element(el).toBeInTheDocument();

		const node = el.element() as HTMLElement;
		const style = getComputedStyle(node);
		// A truncated pod name cannot be read and cannot be copied.
		expect(style.textOverflow).not.toBe('ellipsis');
		expect(style.whiteSpace).not.toBe('nowrap');
	});

	it('copies the displayed value', async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });

		const screen = render(CopyValue, { value: '1,284', copy: '1284' });
		await screen.getByRole('button').first().click();

		// What is copied is the raw value, not the formatted one: a pasted
		// "1,284" is useless in a query.
		expect(writeText).toHaveBeenCalledWith('1284');
		vi.unstubAllGlobals();
	});

	it('falls back when the async clipboard API is unavailable', async () => {
		// The dashboard is served over plain http. Browsers usually treat
		// localhost as a secure context, but not always and not in every
		// embedded webview, so the button must still work without the async
		// clipboard API rather than failing silently.
		vi.stubGlobal('navigator', { ...navigator, clipboard: undefined });
		const exec = vi
			.spyOn(document, 'execCommand')
			.mockImplementation(() => true) as unknown as ReturnType<typeof vi.fn>;

		const screen = render(CopyValue, { value: 'fallback-value' });
		await screen.getByRole('button').first().click();

		expect(exec).toHaveBeenCalledWith('copy');
		vi.restoreAllMocks();
		vi.unstubAllGlobals();
	});

	it('renders an inline variant with a separate button', async () => {
		const screen = render(CopyValue, { value: 'inline-value', inline: true });
		await expect.element(screen.getByText('inline-value')).toBeInTheDocument();
		await expect.element(screen.getByRole('button')).toBeInTheDocument();
	});

	it('labels the button for assistive technology', async () => {
		const screen = render(CopyValue, { value: 'x', inline: true, label: '팟 이름' });
		await expect.element(screen.getByRole('button', { name: '팟 이름 복사' })).toBeInTheDocument();
	});
});
