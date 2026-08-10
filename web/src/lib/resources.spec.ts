import { describe, expect, it } from 'vitest';
import {
	UNGROUPED_LABEL,
	appName,
	filterGroups,
	groupByLoadBalancer,
	matchesQuery
} from './resources';
import type { Resource } from './types';

function tg(app: string, lb?: { dim: string; name?: string }, ns = 'default'): Resource {
	const name = `k8s-${ns}-${app}-d6d507c878`;
	return {
		id: `targetgroup/${name}/abc`,
		name,
		extra: {
			friendlyName: app,
			...(lb ? { loadBalancer: lb.dim, ...(lb.name ? { loadBalancerName: lb.name } : {}) } : {})
		}
	};
}

const PUBLIC = { dim: 'app/public-alb/aaa', name: 'public-alb' };
const INTERNAL = { dim: 'app/internal-alb/bbb', name: 'internal-alb' };

describe('groupByLoadBalancer', () => {
	it('files each target group under the load balancer it is attached to', () => {
		const groups = groupByLoadBalancer([
			tg('checkout', PUBLIC),
			tg('search', INTERNAL),
			tg('cart', PUBLIC)
		]);

		expect(groups.map((g) => g.label)).toEqual(['internal-alb', 'public-alb']);
		expect(groups.find((g) => g.label === 'public-alb')?.items.map(appName)).toEqual([
			'checkout',
			'cart'
		]);
	});

	it('keeps the unattached group and sorts it last', () => {
		// A target group behind no load balancer publishes no ApplicationELB
		// metrics at all. Hiding it would let it be selected from elsewhere and
		// then plot as a permanently empty chart with nothing to explain it.
		const groups = groupByLoadBalancer([tg('orphan'), tg('checkout', PUBLIC)]);

		expect(groups).toHaveLength(2);
		expect(groups[groups.length - 1].label).toBe(UNGROUPED_LABEL);
		expect(groups[groups.length - 1].items.map(appName)).toEqual(['orphan']);
	});

	it('falls back to the dimension when the load balancer has no name', () => {
		const groups = groupByLoadBalancer([tg('checkout', { dim: 'app/nameless/ccc' })]);
		expect(groups[0].label).toBe('app/nameless/ccc');
	});
});

describe('appName', () => {
	it('uses the name the backend derived rather than deriving one again', () => {
		expect(appName(tg('checkout', PUBLIC))).toBe('checkout');
	});

	it('falls back to the raw name when there is no friendly one', () => {
		expect(appName({ id: 'x', name: 'manual-tg' })).toBe('manual-tg');
	});
});

describe('matchesQuery', () => {
	const checkout = tg('checkout', PUBLIC);

	it('matches on the application name', () => {
		expect(matchesQuery(checkout, 'check')).toBe(true);
	});

	it('matches on the raw dimension, which two namespaces need to be told apart', () => {
		// k8s-default-product-… and k8s-staging-product-… both shorten to
		// "product", so the friendly name alone cannot find the right one.
		const staging = tg('product', PUBLIC, 'staging');
		expect(matchesQuery(staging, 'k8s-staging')).toBe(true);
		expect(matchesQuery(tg('product', PUBLIC), 'k8s-staging')).toBe(false);
	});

	it('matches on the load balancer name', () => {
		expect(matchesQuery(checkout, 'internal')).toBe(false);
		expect(matchesQuery(checkout, 'public-alb')).toBe(true);
	});

	it('ignores case and accepts an empty query', () => {
		expect(matchesQuery(checkout, 'CHECKOUT')).toBe(true);
		expect(matchesQuery(checkout, '   ')).toBe(true);
	});
});

describe('filterGroups', () => {
	it('drops the headings left with nothing under them', () => {
		const groups = groupByLoadBalancer([tg('checkout', PUBLIC), tg('search', INTERNAL)]);
		const filtered = filterGroups(groups, 'checkout');

		expect(filtered).toHaveLength(1);
		expect(filtered[0].label).toBe('public-alb');
		expect(filtered[0].items.map(appName)).toEqual(['checkout']);
	});

	it('returns the groups untouched for an empty query', () => {
		const groups = groupByLoadBalancer([tg('checkout', PUBLIC)]);
		expect(filterGroups(groups, '')).toBe(groups);
	});
});
