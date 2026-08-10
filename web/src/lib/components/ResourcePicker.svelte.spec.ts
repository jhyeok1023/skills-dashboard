import { describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import ResourcePicker from './ResourcePicker.svelte';
import type { Resource } from '$lib/types';

/**
 * The defect this component exists to remove was that four different outcomes
 * all looked identical on screen: a lookup nobody ran, a lookup that found
 * nothing, a lookup that failed, and a lookup whose list was cut short. Each
 * assertion below is one of those four telling itself apart from the others.
 */

const BASE = {
	label: '타겟 그룹',
	noun: '타겟 그룹',
	selected: [] as string[],
	loading: false,
	error: '',
	onDiscover: () => {},
	onToggle: () => {},
	onRemove: () => {}
};

function tg(app: string, lbName?: string): Resource {
	const name = `k8s-default-${app}-d6d507c878`;
	return {
		id: `targetgroup/${name}/abc`,
		name,
		extra: {
			friendlyName: app,
			...(lbName ? { loadBalancer: `app/${lbName}/aaa`, loadBalancerName: lbName } : {})
		}
	};
}

describe('ResourcePicker outcomes', () => {
	it('says the lookup has not been run yet', async () => {
		const screen = render(ResourcePicker, { ...BASE, resources: undefined });
		await expect.element(screen.getByText(/자동 조회를 눌러/)).toBeInTheDocument();
	});

	it('says a lookup that found nothing found nothing', async () => {
		// This is the state that used to render as empty space, which is also
		// what "never pressed" and "the call failed" rendered as.
		const screen = render(ResourcePicker, { ...BASE, resources: [] });
		await expect
			.element(screen.getByText(/이 리전에 타겟 그룹이\(가\) 없습니다/))
			.toBeInTheDocument();
		expect(screen.container.textContent).not.toContain('자동 조회를 눌러');
	});

	it('reports a failed lookup', async () => {
		const screen = render(ResourcePicker, {
			...BASE,
			resources: undefined,
			error: 'DescribeDBProxies: AccessDeniedException'
		});
		await expect.element(screen.getByText(/AccessDeniedException/)).toBeInTheDocument();
	});

	it('reports a list that was cut short', async () => {
		const screen = render(ResourcePicker, {
			...BASE,
			resources: [tg('checkout', 'public-alb')],
			truncated: true
		});
		await expect.element(screen.getByText(/중간에서 끊었습니다/)).toBeInTheDocument();
	});

	it('reports a scope that was discarded', async () => {
		const screen = render(ResourcePicker, {
			...BASE,
			resources: [],
			partial: ['CLOUDFRONT 스코프 조회 실패: denied']
		});
		await expect.element(screen.getByText(/CLOUDFRONT 스코프 조회 실패/)).toBeInTheDocument();
	});

	it('disables the button and names the state while a lookup runs', async () => {
		const screen = render(ResourcePicker, { ...BASE, resources: undefined, loading: true });
		const button = screen.getByRole('button', { name: '조회 중…' });
		await expect.element(button).toBeInTheDocument();
		await expect.element(button).toBeDisabled();
	});
});

describe('ResourcePicker rows', () => {
	it('labels a row by application and shows the exact dimension under it', async () => {
		const r = tg('checkout', 'public-alb');
		const screen = render(ResourcePicker, {
			...BASE,
			resources: [r],
			nameOf: (x: Resource) => x.extra?.friendlyName ?? x.name,
			detailOf: (x: Resource) => x.id
		});

		await expect.element(screen.getByText('checkout', { exact: true })).toBeInTheDocument();
		// The raw dimension stays: two namespaces can host the same app name.
		await expect.element(screen.getByText(r.id, { exact: true })).toBeInTheDocument();
	});

	it('groups the rows under the load balancer each one is attached to', async () => {
		const screen = render(ResourcePicker, {
			...BASE,
			grouped: true,
			resources: [tg('checkout', 'public-alb'), tg('search', 'internal-alb')]
		});

		await expect.element(screen.getByText('public-alb')).toBeInTheDocument();
		await expect.element(screen.getByText('internal-alb')).toBeInTheDocument();
	});

	it('selects and clears one load balancer at a time', async () => {
		const toggled: string[] = [];
		const screen = render(ResourcePicker, {
			...BASE,
			grouped: true,
			// Headings are sorted by name, so "public-alb" is the first group and
			// its 전체 선택 is the first such button on the page.
			resources: [tg('checkout', 'public-alb'), tg('cart', 'public-alb'), tg('search', 'zz-alb')],
			onToggle: (r: Resource) => toggled.push(r.extra?.friendlyName ?? r.name)
		});

		await screen.getByRole('button', { name: '전체 선택' }).first().click();

		// Only the heading's own rows, never the whole list.
		expect(toggled.sort()).toEqual(['cart', 'checkout']);
	});

	it('offers a way to remove a saved id the lookup did not return', async () => {
		// A selection made before a permission was revoked would otherwise be
		// stuck in the config with no control to reach it.
		const onRemove = vi.fn();
		const screen = render(ResourcePicker, {
			...BASE,
			resources: [tg('checkout', 'public-alb')],
			selected: ['targetgroup/k8s-default-gone-1111111111/zzz'],
			onRemove
		});

		await expect
			.element(screen.getByText('targetgroup/k8s-default-gone-1111111111/zzz'))
			.toBeInTheDocument();
		await screen.getByRole('button', { name: '제거' }).click();
		expect(onRemove).toHaveBeenCalledWith('targetgroup/k8s-default-gone-1111111111/zzz');
	});
});
