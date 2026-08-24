// 설정 화면이 쓰는 엔드포인트. internal/api/settings.go 의 이식이다.

import type { Context } from 'hono';

import type { Clients } from '../aws/client.ts';
import {
	clusters,
	loadBalancers,
	logGroups,
	rdsProxies,
	targetGroups,
	webACLs,
	type Listing
} from '../aws/discovery.ts';
import { mergeStrict } from '../config/store.ts';
import type {
	Config,
	DiscoveryResponse,
	LogFormat,
	LogFormatPreview,
	Meta,
	Resource
} from '../contract.ts';
import { logger } from '../log.ts';
import {
	compactDuration,
	defaultPeriod,
	maxRange,
	periodsFor,
	range1h,
	ranges
} from '../domain/window.ts';
import {
	isBadStatus,
	isExcludedPath,
	parseLogLine,
	toWire,
	validateLogFormat
} from '../domain/logfmt.ts';
import type { Service } from '../service.ts';
import { credentialHint, requireAWS, serviceRegion, serviceWAFRegion } from '../service.ts';
import { badRequest, fail, json, upstream } from './respond.ts';

/** 본문 상한. Go 의 http.MaxBytesReader(1<<20) 과 같다. */
const maxBodyBytes = 1 << 20;

async function readJSONBody(c: Context): Promise<Record<string, unknown>> {
	const text = await c.req.text();
	if (text.length > maxBodyBytes) throw new Error('본문이 너무 큽니다');
	const raw: unknown = JSON.parse(text);
	if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
		throw new Error('JSON 객체가 아닙니다');
	}
	return raw as Record<string, unknown>;
}

/**
 * meta 는 어떤 범위·주기 조합이 존재하는지 프론트에 알린다. UI 선택기가 서버가
 * 거절할 것을 제시할 수 없게 하려는 것이다.
 */
export function handleMeta(service: Service): Response {
	const resp: Meta = {
		maxRangeSeconds: Math.trunc(maxRange / 1000),
		ranges: ranges().map((r) => ({
			range: compactDuration(r),
			seconds: Math.trunc(r / 1000),
			periods: periodsFor(r).map(compactDuration),
			defaultPeriod: compactDuration(defaultPeriod(r))
		})),
		defaultRange: compactDuration(range1h),
		limits: service.store.get().limits
	};
	// notices 가 /api/config 가 아니라 여기 실리는 이유: 설정 화면은 받은 설정
	// 객체를 그대로 되돌려 저장하고, PUT 핸들러는 모르는 필드를 거절한다.
	if (service.configNotices.length > 0) resp.notices = service.configNotices;
	return json(200, resp);
}

export function handleGetConfig(service: Service): Response {
	return json(200, service.store.get());
}

export async function handlePutConfig(service: Service, c: Context): Promise<Response> {
	let merged: Config;
	try {
		// 저장된 설정에서 시작한다. 부분 본문이 호출자가 언급하지 않은 필드를
		// 조용히 비우지 못하게 하려는 것이다.
		merged = mergeStrict(service.store.get(), await readJSONBody(c));
	} catch (err) {
		return badRequest(new Error(`설정을 읽을 수 없습니다: ${message(err)}`));
	}

	try {
		service.store.set(merged);
	} catch (err) {
		return badRequest(err);
	}

	// 리소스 선택이 바뀌었으므로, 옛 선택을 기준으로 캐시된 것은 이제 다른
	// 것을 설명하고 있다.
	service.invalidateCache();
	return json(200, service.store.get());
}

/**
 * handleIdentity 는 자격증명이 누구인지 답한다. 없으면 503 과 함께 무엇을
 * 고쳐야 하는지 말한다.
 */
export function handleIdentity(service: Service): Response {
	if (service.credentialError !== null) {
		return fail(503, service.credentialError, '.env 파일에 AWS 액세스 키를 설정하세요.');
	}
	if (service.identity === null) {
		return fail(503, new Error('AWS clients are not configured'), credentialHint(service));
	}
	return json(200, service.identity);
}

/**
 * handleLogFormatPreview 는 붙여넣은 로그 한 줄을 주어진 형식으로 파싱한다.
 *
 * 애플리케이션 로그 모양이 아직 정해지는 중이라, 설정 화면에는 저장 전에 실제
 * 한 줄로 패턴을 확인할 방법이 필요하다. 그것이 없으면 필드 이름이 틀렸다는
 * 사실을 아는 유일한 방법은 패널이 조용히 비어 있는 것을 알아채는 것뿐이다.
 */
export async function handleLogFormatPreview(service: Service, c: Context): Promise<Response> {
	let body: Record<string, unknown>;
	try {
		body = await readJSONBody(c);
	} catch (err) {
		return badRequest(new Error(`요청을 읽을 수 없습니다: ${message(err)}`));
	}

	const sample = typeof body['sample'] === 'string' ? body['sample'] : '';
	if (sample === '') return badRequest(new Error('sample 로그 라인이 비어 있습니다'));

	const stored = service.store.get().logFormat;
	const format: LogFormat =
		body['format'] === null || body['format'] === undefined
			? stored
			: ({ ...stored, ...(body['format'] as Partial<LogFormat>) } as LogFormat);

	let compiled;
	try {
		compiled = validateLogFormat(format);
	} catch (err) {
		return badRequest(err);
	}

	const line = parseLogLine(compiled, sample, service.now());
	const matched = line.hasAccess || line.level !== '';
	const excluded = isExcludedPath(format, line.path);

	const resp: LogFormatPreview = {
		parsed: toWire(line),
		matched,
		badStatus: isBadStatus(format, line.status),
		excluded
	};
	if (!matched) {
		resp.suggestion =
			'요청 필드도 레벨도 인식되지 않았습니다. latencyField/statusField 이름이나 textPattern 정규식을 확인하세요.';
	} else if (excluded) {
		resp.suggestion = '이 경로는 제외 목록에 있어 팟 로그 패널에 집계되지 않습니다.';
	}
	return json(200, resp);
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}

// --- 발견 ---------------------------------------------------------------

/**
 * discoveryTimeout 은 조회 하나를 묶는다(ms).
 *
 * SDK 의 재시도 정책(다섯 번, 최대 15초 백오프)이 지속적인 스로틀링에서는 이것을
 * 넘길 수 있으므로, 느린 계정은 재시도 도중에 잘린다. 의도한 거래다 — 1분 넘게
 * 말없이 앉아 있는 설정 버튼이, 30초에 포기하고 이유를 말하는 버튼보다 나쁘다.
 */
const discoveryTimeoutMs = 30_000;

/** 조회 하나가 낸 것. 응답이 되기 전의 모습이고, 캐시가 저장하는 것도 이것이다. */
interface DiscoveryResult {
	resources: Resource[];
	truncated: boolean;
	partial: string[];
}

class UnknownKindError extends Error {}

/**
 * cachedDiscovery 는 조회 하나를 캐시 뒤에서 돌리고 그 값이 얼마였는지 기록을
 * 남긴다.
 *
 * 설정 화면에 리소스가 뜨지 않을 때 운영자가 가장 먼저 가리키는 것이 발견인데,
 * 지금까지 그것은 아무 흔적도 남기지 않았다. 실패한 호출, 성공했지만 아무것도
 * 찾지 못한 호출, 캐시가 답해서 아예 나가지 않은 호출이 프로세스 밖에서
 * 구분되지 않았다. 로그 줄이 로더 안에 있는 것은 일부러다 — 로더는 미스에서만
 * 도므로, 아무것도 찍지 않은 요청은 메모리에서 나갔다는 뜻이고, 그 구분을
 * 기록하는 데는 아무 비용도 들지 않는다.
 */
function cachedDiscovery(
	service: Service,
	key: string,
	kind: string,
	prefix: string,
	load: (signal: AbortSignal) => Promise<DiscoveryResult>,
	signal: AbortSignal
): Promise<DiscoveryResult> {
	return service.cache.do(key, signal, async () => {
		// 벽시계다. service.now 는 테스트에서 한 시점에 고정되는데, 여기서 재는
		// 것은 언제 일어났는가가 아니라 얼마나 걸렸는가다.
		const started = Date.now();
		try {
			const res = await load(signal);
			logger.info('discovery listed', {
				kind,
				prefix,
				region: serviceRegion(service),
				wafRegion: serviceWAFRegion(service),
				count: res.resources.length,
				truncated: res.truncated,
				partial: res.partial.length,
				elapsedMs: Date.now() - started
			});
			return res;
		} catch (err) {
			logger.warn('discovery failed', {
				kind,
				prefix,
				region: serviceRegion(service),
				wafRegion: serviceWAFRegion(service),
				elapsedMs: Date.now() - started,
				error: err instanceof Error ? err : String(err)
			});
			throw err;
		}
	});
}

function asResult(l: Listing): DiscoveryResult {
	return { resources: l.resources, truncated: l.truncated, partial: [] };
}

/**
 * discoverWebACLs 는 두 범위를 모두 나열한다. ALB 앞의 REGIONAL ACL 과
 * CLOUDFRONT ACL 은 어느 쪽이든 있을 법하고, CLOUDFRONT 목록은 us-east-1 에만
 * 존재한다.
 */
async function discoverWebACLs(clients: Clients, signal: AbortSignal): Promise<DiscoveryResult> {
	const out = asResult(await webACLs(clients.waf, 'REGIONAL', signal));

	try {
		const global = await webACLs(clients.wafGlobal, 'CLOUDFRONT', signal);
		out.resources.push(...global.resources);
		out.truncated = out.truncated || global.truncated;
	} catch (err) {
		// CLOUDFRONT 권한이 없다고 해서 운영자가 실제로 쓸 수 있는 REGIONAL ACL
		// 까지 가려서는 안 된다. 다만 삼키지 않고 보고한다. 이 갈래는 wafRegion
		// 이 작업 리전과 같을 때도 탄다(newClients 가 global 클라이언트를
		// regional 쪽에 별칭으로 걸고, us-east-1 밖에서 CLOUDFRONT 범위 호출은
		// 실패한다). 삼키면 잘못 설정된 wafRegion 이 완전히 보이지 않게 된다.
		out.partial.push(
			`CLOUDFRONT 스코프 조회 실패: ${err instanceof Error ? err.message : String(err)}`
		);
	}
	return out;
}

export async function handleDiscovery(service: Service, c: Context): Promise<Response> {
	const denied = requireAWS(service);
	if (denied !== null) return fail(503, denied.error, denied.hint);
	const clients = service.clients as Clients;

	const kind = c.req.param('kind') ?? '';
	const prefix = new URL(c.req.url).searchParams.get('prefix') ?? '';

	// 시작 줄이다. 끝내 돌아오지 않는 요청도 도착했다는 흔적은 남기게 하려는
	// 것이다. 이것이 없으면 "버튼이 아무 일도 안 했다" 와 "요청이 서버에 닿지
	// 않았다" 를 가를 수 없다.
	logger.debug('discovery requested', { kind, prefix });

	const deadline = AbortSignal.timeout(discoveryTimeoutMs);

	let load: ((signal: AbortSignal) => Promise<DiscoveryResult>) | null = null;
	switch (kind) {
		case 'targetgroups':
			load = async (s) => asResult(await targetGroups(clients.elb, s));
			break;
		case 'loadbalancers':
			load = async (s) => asResult(await loadBalancers(clients.elb, s));
			break;
		case 'loggroups':
			load = async (s) => asResult(await logGroups(clients.logs, prefix, s));
			break;
		case 'waf-loggroups':
			// WAF 로그 그룹은 WAF 리전에서 나열한다. CLOUDFRONT 범위 web ACL 은
			// us-east-1 에만 쓰므로, 작업 리전을 나열하면 아무것도 나오지 않고
			// 그것은 "이 계정에는 WAF 로깅이 없다" 로 읽힌다.
			load = async (s) => asResult(await logGroups(clients.logsGlobal, prefix, s));
			break;
		case 'rdsproxies':
			load = async (s) => asResult(await rdsProxies(clients.rds, s));
			break;
		case 'clusters':
			load = async (s) => asResult(await clusters(clients.eks, s));
			break;
		case 'webacls':
			load = (s) => discoverWebACLs(clients, s);
			break;
		default:
			return badRequest(new UnknownKindError(`unknown discovery kind "${kind}"`));
	}

	// 키는 설정이 기록한 리전이 아니라 클라이언트가 향한 리전을 적는다. 설정의
	// 리전은 자격증명에 대한 메모일 뿐 호출이 어디에 떨어지는지를 정하지 않는다.
	// us-east-1 의 조회와 작업 리전의 조회가 서로를 대신 답해서는 안 된다.
	const key = `discovery|${kind}|${prefix}|${serviceRegion(service)}|${serviceWAFRegion(service)}`;

	try {
		const res = await cachedDiscovery(service, key, kind, prefix, load, deadline);
		const resp: DiscoveryResponse = { kind, resources: res.resources };
		if (res.truncated) resp.truncated = true;
		if (res.partial.length > 0) resp.partial = res.partial;
		return json(200, resp);
	} catch (err) {
		// 재시도 뒤 취소에서는 SDK 가 "context deadline exceeded" 를 보고하는데,
		// 그것은 스로틀링당했거나 닿지 않는 계정이 아니라 고장난 대시보드처럼
		// 읽힌다. 어느 쪽이었는지 말한다.
		if (deadline.aborted) {
			return upstream(
				new Error(
					`AWS 응답이 ${Math.trunc(discoveryTimeoutMs / 1000)}초 안에 오지 않았습니다 (재시도 중 취소됨). ` +
						`계정이 스로틀링되고 있는지, 네트워크에서 AWS 엔드포인트에 닿는지 확인하세요: ${message(err)}`
				)
			);
		}
		return upstream(err);
	}
}
