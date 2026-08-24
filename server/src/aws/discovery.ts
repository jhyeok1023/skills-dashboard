// 설정 화면이 고를 수 있는 AWS 리소스를 찾는다. internal/awsx/discovery.go 의
// 이식이다.

import {
	DescribeLogGroupsCommand,
	type CloudWatchLogsClient
} from '@aws-sdk/client-cloudwatch-logs';
import {
	DescribeNodegroupCommand,
	ListClustersCommand,
	ListNodegroupsCommand,
	type EKSClient
} from '@aws-sdk/client-eks';
import {
	DescribeLoadBalancersCommand,
	DescribeTargetGroupsCommand,
	type ElasticLoadBalancingV2Client,
	type LoadBalancer,
	type TargetGroup
} from '@aws-sdk/client-elastic-load-balancing-v2';
import { DescribeDBProxiesCommand, type RDSClient } from '@aws-sdk/client-rds';
import { ListWebACLsCommand, type Scope, type WAFV2Client } from '@aws-sdk/client-wafv2';

import type { Resource } from '../contract.ts';
import {
	friendlyTargetGroupName,
	loadBalancerDimension,
	targetGroupDimension
} from '../domain/dimensions.ts';
import { sendOptions } from './client.ts';

/**
 * 모든 List 순회의 상한. 발견은 설정 화면 뒤에서 도므로, 요청을 붙들고 있는
 * 무한 순회보다 유계이고 어쩌면 불완전한 목록이 낫다.
 */
const maxDiscoveryPages = 20;

/**
 * Listing 은 발견 결과와, 순회가 중간에 끊겼는지를 함께 담는다.
 *
 * 잘렸다는 말을 하지 않는 잘린 목록은 짧은 목록보다 나쁘다. 운영자가 찾는
 * 리소스가 그냥 제시되지 않을 뿐이고, 화면은 그들이 읽는 목록을 의심할 이유를
 * 주지 않는다. 여기의 모든 순회가 유계이므로, 모든 순회는 자기가 상한에 닿았다고
 * 말할 수 있어야 한다.
 */
export interface Listing {
	resources: Resource[];
	truncated: boolean;
}

/** sorted 는 순회를 끝맺는다: 이름 순 리소스와, 페이지 상한이 일찍 멈췄는지. */
function sorted(rs: Resource[], truncated: boolean): Listing {
	rs.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
	return { resources: rs, truncated };
}

/**
 * describeLoadBalancers 는 리전의 로드밸런서를 페이지 끝까지 훑는다. 로드밸런서
 * 목록과 타겟 그룹 목록이 둘 다 이것을 필요로 하는데, 같은 API 를 두 번 페이징하는
 * 것은 어긋나기 마련인 중복이다.
 */
async function describeLoadBalancers(
	api: ElasticLoadBalancingV2Client,
	signal?: AbortSignal
): Promise<{ items: LoadBalancer[]; truncated: boolean }> {
	const items: LoadBalancer[] = [];
	let marker: string | undefined;
	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new DescribeLoadBalancersCommand(marker === undefined ? {} : { Marker: marker }),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`DescribeLoadBalancers: ${message(err)}`);
		}
		items.push(...(resp.LoadBalancers ?? []));
		if (resp.NextMarker === undefined || resp.NextMarker === '') {
			return { items, truncated: false };
		}
		marker = resp.NextMarker;
	}
	return { items, truncated: true };
}

/** describeTargetGroups 는 리전의 타겟 그룹을 페이지 끝까지 훑는다. */
async function describeTargetGroups(
	api: ElasticLoadBalancingV2Client,
	signal?: AbortSignal
): Promise<{ items: TargetGroup[]; truncated: boolean }> {
	const items: TargetGroup[] = [];
	let marker: string | undefined;
	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new DescribeTargetGroupsCommand(marker === undefined ? {} : { Marker: marker }),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`DescribeTargetGroups: ${message(err)}`);
		}
		items.push(...(resp.TargetGroups ?? []));
		if (resp.NextMarker === undefined || resp.NextMarker === '') {
			return { items, truncated: false };
		}
		marker = resp.NextMarker;
	}
	return { items, truncated: true };
}

/**
 * loadBalancers 는 리전의 로드밸런서를 나열한다.
 *
 * ID 가 ARN 이 아니라 CloudWatch 차원 값이다. 설정 화면에서 하나를 고르면 메트릭
 * SEARCH 가 실제로 쓸 수 있는 것이 쓰이게 하려는 것이다. 그 칸에 붙여넣은 ARN 은
 * 값 정규식을 통과한 뒤 아무것에도 맞지 않는다. 이 목록이 존재하는 이유가 그것이다.
 */
export async function loadBalancers(
	api: ElasticLoadBalancingV2Client,
	signal?: AbortSignal
): Promise<Listing> {
	const { items, truncated } = await describeLoadBalancers(api, signal);
	return sorted(
		items.map((lb) => {
			const arn = lb.LoadBalancerArn ?? '';
			return {
				id: loadBalancerDimension(arn),
				name: lb.LoadBalancerName ?? '',
				arn,
				extra: {
					type: lb.Type ?? '',
					scheme: lb.Scheme ?? '',
					dnsName: lb.DNSName ?? ''
				}
			};
		}),
		truncated
	);
}

/**
 * targetGroups 는 리전의 타겟 그룹을, 각각이 붙어 있는 로드밸런서와 함께
 * 나열한다.
 *
 * 두 순회는 동시에 돈다. 서로 독립적이고 — 로드밸런서 목록은 마지막에 각 그룹의
 * ARN 에 이름을 붙이는 데만 쓰인다 — 애플리케이션마다 타겟 그룹이 하나씩이면
 * 목록이 순서대로 도는 것이 체감될 만큼 길다. 설정 버튼이 둘 중 긴 쪽이 아니라
 * 둘의 합만큼을 쓰게 된다. 한쪽이 실패하면 다른 쪽을 취소한다 — 이미 버려질
 * 운명인 순회가 페이징을 멈추게 하려는 것이다.
 */
export async function targetGroups(
	api: ElasticLoadBalancingV2Client,
	signal?: AbortSignal
): Promise<Listing> {
	const controller = new AbortController();
	const bounded =
		signal === undefined ? controller.signal : AbortSignal.any([signal, controller.signal]);

	const [lbResult, tgResult] = await Promise.allSettled([
		describeLoadBalancers(api, bounded).catch((err: unknown) => {
			controller.abort();
			throw err;
		}),
		describeTargetGroups(api, bounded).catch((err: unknown) => {
			controller.abort();
			throw err;
		})
	]);

	if (lbResult.status === 'rejected') throw lbResult.reason;
	if (tgResult.status === 'rejected') throw tgResult.reason;

	const lbNames = new Map<string, string>();
	for (const lb of lbResult.value.items) {
		lbNames.set(lb.LoadBalancerArn ?? '', lb.LoadBalancerName ?? '');
	}

	const out = tgResult.value.items.map((tg) => {
		const arn = tg.TargetGroupArn ?? '';
		const name = tg.TargetGroupName ?? '';
		const extra: Record<string, string> = { friendlyName: friendlyTargetGroupName(name) };
		const lbArn = (tg.LoadBalancerArns ?? [])[0];
		if (lbArn !== undefined) {
			extra['loadBalancer'] = loadBalancerDimension(lbArn);
			extra['loadBalancerName'] = lbNames.get(lbArn) ?? '';
		}
		return { id: targetGroupDimension(arn), name, arn, extra };
	});

	return sorted(out, lbResult.value.truncated || tgResult.value.truncated);
}

/** logGroups 는 이름이 prefix 로 시작하는 로그 그룹을 나열한다. */
export async function logGroups(
	api: CloudWatchLogsClient,
	prefix: string,
	signal?: AbortSignal
): Promise<Listing> {
	const out: Resource[] = [];
	let nextToken: string | undefined;

	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new DescribeLogGroupsCommand({
					limit: 50,
					...(prefix !== '' ? { logGroupNamePrefix: prefix } : {}),
					...(nextToken !== undefined ? { nextToken } : {})
				}),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`DescribeLogGroups: ${message(err)}`);
		}
		for (const lg of resp.logGroups ?? []) {
			const name = lg.logGroupName ?? '';
			out.push({ id: name, name, arn: lg.arn ?? '' });
		}
		if (resp.nextToken === undefined || resp.nextToken === '') return sorted(out, false);
		nextToken = resp.nextToken;
	}
	return sorted(out, true);
}

/** rdsProxies 는 리전의 RDS 프록시를 나열한다. */
export async function rdsProxies(api: RDSClient, signal?: AbortSignal): Promise<Listing> {
	const out: Resource[] = [];
	let marker: string | undefined;

	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new DescribeDBProxiesCommand(marker === undefined ? {} : { Marker: marker }),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`DescribeDBProxies: ${message(err)}`);
		}
		for (const p of resp.DBProxies ?? []) {
			const name = p.DBProxyName ?? '';
			out.push({
				id: name,
				name,
				arn: p.DBProxyArn ?? '',
				extra: { engine: p.EngineFamily ?? '', status: p.Status ?? '' }
			});
		}
		if (resp.Marker === undefined || resp.Marker === '') return sorted(out, false);
		marker = resp.Marker;
	}
	return sorted(out, true);
}

/**
 * webACLs 는 한 범위의 web ACL 을 나열한다. REGIONAL ACL 은 작업 리전에 살고,
 * CLOUDFRONT ACL 은 us-east-1 에만 있다.
 */
export async function webACLs(
	api: WAFV2Client,
	scope: Scope,
	signal?: AbortSignal
): Promise<Listing> {
	const out: Resource[] = [];
	let nextMarker: string | undefined;

	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new ListWebACLsCommand({
					Scope: scope,
					Limit: 100,
					...(nextMarker !== undefined ? { NextMarker: nextMarker } : {})
				}),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`ListWebACLs(${scope}): ${message(err)}`);
		}
		for (const acl of resp.WebACLs ?? []) {
			const name = acl.Name ?? '';
			out.push({
				// WebACL 의 CloudWatch 차원은 이름이다.
				id: name,
				name,
				arn: acl.ARN ?? '',
				extra: { scope, id: acl.Id ?? '' }
			});
		}
		if (resp.NextMarker === undefined || resp.NextMarker === '') return sorted(out, false);
		nextMarker = resp.NextMarker;
	}
	return sorted(out, true);
}

/** clusters 는 EKS 클러스터를 나열한다. */
export async function clusters(api: EKSClient, signal?: AbortSignal): Promise<Listing> {
	const out: Resource[] = [];
	let nextToken: string | undefined;

	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new ListClustersCommand(nextToken === undefined ? {} : { nextToken }),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`ListClusters: ${message(err)}`);
		}
		for (const name of resp.clusters ?? []) {
			out.push({
				id: name,
				name,
				extra: { logGroup: `/aws/containerinsights/${name}/application` }
			});
		}
		if (resp.nextToken === undefined || resp.nextToken === '') return sorted(out, false);
		nextToken = resp.nextToken;
	}
	return sorted(out, true);
}

/**
 * NodeScaling 은 클러스터의 노드 수 한계를 노드 그룹 전체에 걸쳐 더한 것이다.
 *
 * CloudWatch 는 현재 노드 수만 publish 하므로, 팟·노드 개수 패널이 보여 주는
 * 최소와 최대의 출처는 여기뿐이다. 한 번 붙잡아 두지 않고 요청마다 읽는다 —
 * 이전 구현은 로그인 시점에 이것을 캐시했고, 노드 그룹을 다시 스케일한 순간부터
 * 표시된 한계가 영구히 틀렸다.
 */
export interface NodeScaling {
	min: number;
	max: number;
	desired: number;
	groups: string[];
}

/** clusterNodeScaling 은 모든 노드 그룹의 스케일링 설정을 더한다. */
export async function clusterNodeScaling(
	api: EKSClient | null,
	cluster: string,
	signal?: AbortSignal
): Promise<NodeScaling> {
	if (api === null) throw new Error('EKS client is not configured');
	if (cluster === '') throw new Error('no cluster configured');

	const names: string[] = [];
	let nextToken: string | undefined;
	for (let page = 0; page < maxDiscoveryPages; page++) {
		let resp;
		try {
			resp = await api.send(
				new ListNodegroupsCommand({
					clusterName: cluster,
					...(nextToken !== undefined ? { nextToken } : {})
				}),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`ListNodegroups: ${message(err)}`);
		}
		names.push(...(resp.nodegroups ?? []));
		if (resp.nextToken === undefined || resp.nextToken === '') break;
		nextToken = resp.nextToken;
	}

	const out: NodeScaling = { min: 0, max: 0, desired: 0, groups: names };
	for (const name of names) {
		let resp;
		try {
			resp = await api.send(
				new DescribeNodegroupCommand({ clusterName: cluster, nodegroupName: name }),
				sendOptions(signal)
			);
		} catch (err) {
			throw new Error(`DescribeNodegroup ${name}: ${message(err)}`);
		}
		const sc = resp.nodegroup?.scalingConfig;
		if (sc === undefined) continue;
		out.min += sc.minSize ?? 0;
		out.max += sc.maxSize ?? 0;
		out.desired += sc.desiredSize ?? 0;
	}
	out.groups.sort();
	return out;
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}
