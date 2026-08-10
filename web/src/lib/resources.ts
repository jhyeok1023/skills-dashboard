import type { Resource } from './types';

/**
 * Arranging a discovered resource list for reading.
 *
 * A cluster that runs one application per target group produces a list far too
 * long to scan as a flat column of checkboxes, and the names are generated —
 * `k8s-default-checkout-d6d507c878` — so neither the application nor the load
 * balancer is legible from the raw list. Grouping and filtering happen here so
 * the picker component stays about rendering.
 *
 * Nothing here derives a *value*. The application name is `extra.friendlyName`,
 * which the backend computed with domain.FriendlyTargetGroupName; recomputing
 * it in TypeScript is how the two spellings would come to disagree.
 */

export interface ResourceGroup {
	/** The load balancer's CloudWatch dimension, or '' when there is none. */
	key: string;
	label: string;
	items: Resource[];
}

/** UNGROUPED heads the resources attached to no load balancer at all. */
export const UNGROUPED_LABEL = '연결된 로드 밸런서 없음';

/** The application name to show for a resource. */
export function appName(r: Resource): string {
	return r.extra?.friendlyName || r.name;
}

function groupLabelFor(r: Resource): string {
	return r.extra?.loadBalancerName || r.extra?.loadBalancer || UNGROUPED_LABEL;
}

/**
 * Files each resource under the load balancer it is attached to.
 *
 * The unattached group is kept and sorted last rather than dropped. A target
 * group registered with no load balancer publishes no AWS/ApplicationELB
 * metrics at all, so hiding it would let an operator pick it from somewhere
 * else and then stare at a permanently empty chart with nothing to explain it.
 */
export function groupByLoadBalancer(resources: Resource[]): ResourceGroup[] {
	const byKey = new Map<string, ResourceGroup>();
	for (const r of resources) {
		const key = r.extra?.loadBalancer ?? '';
		let group = byKey.get(key);
		if (!group) {
			group = { key, label: groupLabelFor(r), items: [] };
			byKey.set(key, group);
		}
		group.items.push(r);
	}

	return [...byKey.values()].sort((a, b) => {
		if (a.key === '') return 1;
		if (b.key === '') return -1;
		return a.label.localeCompare(b.label);
	});
}

/**
 * Whether a resource answers to a typed query.
 *
 * The raw dimension is searched alongside the friendly name because two
 * namespaces can host the same application — `k8s-default-product-…` and
 * `k8s-staging-product-…` both shorten to `product` — so the friendly name is
 * not always enough to find the one that was meant.
 */
export function matchesQuery(r: Resource, query: string): boolean {
	const q = query.trim().toLowerCase();
	if (!q) return true;
	return [r.extra?.friendlyName, r.name, r.id, r.extra?.loadBalancerName, r.extra?.engine]
		.filter((s): s is string => Boolean(s))
		.some((s) => s.toLowerCase().includes(q));
}

/** Drops the resources that do not match, then the groups left empty. */
export function filterGroups(groups: ResourceGroup[], query: string): ResourceGroup[] {
	if (!query.trim()) return groups;
	return groups
		.map((g) => ({ ...g, items: g.items.filter((r) => matchesQuery(r, query)) }))
		.filter((g) => g.items.length > 0);
}
