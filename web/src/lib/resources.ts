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

/**
 * What a lookup that found nothing says.
 *
 * Saying it at all is the point: an empty result used to render as empty space,
 * which is also what a lookup nobody ran looks like, and what a lookup that
 * failed looked like on the fields with no error line. It lives here so the
 * picker and the settings page cannot come to spell it two ways.
 */
export function emptyFor(noun: string): string {
	return `조회했지만 이 리전에 ${noun}이(가) 없습니다. 리전과 IAM 권한을 확인하세요.`;
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
 * Whether a resource answers to an already-trimmed, already-lowered query.
 *
 * Every discovered attribute is searched, not a per-kind allowlist: `engine` is
 * RDS's, `scope` is the Web ACL's, and a list naming some of them is a list that
 * silently fails to find the rest — typing CLOUDFRONT used to match nothing.
 * The raw name and dimension are searched alongside the friendly name because
 * two namespaces can host the same application — `k8s-default-product-…` and
 * `k8s-staging-product-…` both shorten to `product` — so the friendly name is
 * not always enough to find the one that was meant.
 */
function matches(r: Resource, q: string): boolean {
	if (r.name.toLowerCase().includes(q) || r.id.toLowerCase().includes(q)) return true;
	for (const v of Object.values(r.extra ?? {})) {
		if (v.toLowerCase().includes(q)) return true;
	}
	return false;
}

/** Whether a resource answers to a typed query. */
export function matchesQuery(r: Resource, query: string): boolean {
	const q = query.trim().toLowerCase();
	return !q || matches(r, q);
}

/** Drops the resources that do not match, then the groups left empty. */
export function filterGroups(groups: ResourceGroup[], query: string): ResourceGroup[] {
	// Normalized once for the whole list rather than once per resource: this
	// runs on every keystroke, over every row.
	const q = query.trim().toLowerCase();
	if (!q) return groups;
	return groups
		.map((g) => ({ ...g, items: g.items.filter((r) => matches(r, q)) }))
		.filter((g) => g.items.length > 0);
}
