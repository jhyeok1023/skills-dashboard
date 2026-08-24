// 자격증명 하나를 AWS 연결로 만든다. internal/api/conn.go 이식이다.

import { globalWAFRegion, newClients, whoAmI, type Clients } from './aws/client.ts';
import { InsightsRunner } from './aws/insights.ts';
import { MetricFetcher } from './aws/metrics.ts';
import { credentialsPath, CredentialStore } from './config/credstore.ts';
import { loadCredentials, redacted, validateCredentials, type Credentials } from './config/env.ts';
import type { Identity } from './contract.ts';
import { logger } from './log.ts';
import type { Service } from './service.ts';

/**
 * CredentialSource 는 지금 쓰는 키가 어디서 왔는지다. 둘 중 어느 쪽이 이기는지는
 * 사고가 아니라 결정이고, 이긴 쪽을 말하지 않는 화면은 무시당하고 있는 파일을
 * 고치게 만든다.
 */
export type CredentialSource = 'saved' | 'env' | 'none';

/**
 * AWSConn 은 AWS 클라이언트 한 벌과 그 위에 세운 모든 것이다.
 *
 * 필드 하나씩이 아니라 통째로 바꾼다. 예전에는 키를 저장할 수 없었으므로
 * 클라이언트를 시작할 때 한 번 만들고 그대로 읽었다. 이제 저장이 연결을
 * 갈아끼우므로, 필드를 하나씩 바꾸면 await 사이에 낀 요청이 반씩 섞어 읽는다 —
 * Insights 는 새 계정, 메트릭은 옛 계정 — 그렇게 만든 패널은 둘 중 어느 것도
 * 설명하지 않는다.
 */
export interface AWSConn {
	clients: Clients | null;
	metrics: MetricFetcher | null;
	insights: InsightsRunner | null;
	insightsGlobal: InsightsRunner | null;
	identity: Identity | null;
	source: CredentialSource;
	/** 쓸 수 있는 연결이 없는 이유. null 이면 정상이다. */
	error: Error | null;
}

/** 아직 아무것도 연결하지 않은 상태. */
export function noConnection(source: CredentialSource, error: Error | null): AWSConn {
	return {
		clients: null,
		metrics: null,
		insights: null,
		insightsGlobal: null,
		identity: null,
		source,
		error
	};
}

/** 이 연결로 호출할 수 있는지. */
export function connectionOk(conn: AWSConn): boolean {
	return conn.error === null && conn.clients !== null;
}

/**
 * setAWS 는 연결을 갈아끼우고 이전 연결이 캐시해 둔 것을 버린다.
 *
 * 캐시 키는 리전과 리소스로 만들어지고 계정은 들어가지 않는다. 옛 키로 받아 둔
 * 답이 새 키의 답으로 나갈 수 있다는 뜻이다 — 같은 숫자가 다른 계정 이름을 달고
 * 나온다. 비우는 것이 교체를 정직하게 만든다.
 */
export function setAWS(service: Service, conn: AWSConn): void {
	service.aws = conn;
	service.invalidateCache();
}

/**
 * connect 는 자격증명 한 벌로 연결을 만든다.
 *
 * AWS 클라이언트로 가는 유일한 길이다. 설정 화면에서 저장한 키와 시작할 때 읽은
 * 키가 같은 검사를 같은 순서로 지나 같은 상태에 닿는다. 실패는 두 번째 반환값이
 * 아니라 AWSConn 안에 담아 돌려준다 — "연결이 없고 이유는 이것이다" 는 어차피
 * 핸들러가 들고 다녀야 하는 것이다.
 */
export async function connect(
	service: Service,
	creds: Credentials,
	source: CredentialSource
): Promise<AWSConn> {
	try {
		validateCredentials(creds);
	} catch (err) {
		return noConnection(source, asError(err));
	}

	let cfg = service.store.get();
	let clients: Clients;
	try {
		clients = newClients(creds, cfg.wafRegion === '' ? globalWAFRegion : cfg.wafRegion);
	} catch (err) {
		return noConnection(source, asError(err));
	}

	const runner = (api: Clients['logs']): InsightsRunner =>
		new InsightsRunner(api, {
			concurrency: cfg.limits.insightsConcurrency,
			timeoutMs: cfg.limits.queryTimeoutSeconds * 1000
		});

	const conn: AWSConn = {
		clients,
		metrics: new MetricFetcher(clients.cw),
		insights: runner(clients.logs),
		insightsGlobal: null,
		identity: null,
		source,
		error: null
	};
	// WAF 로그는 제 러너가 필요하다. CLOUDFRONT 범위 web ACL 은 us-east-1 에만
	// 로그를 내보내므로, 작업 리전 클라이언트로 그 로그 그룹을 조회하면 없는
	// 그룹이라며 실패한다. 두 리전이 같으면 러너를 공유해 동시 실행 상한이 같은
	// 크기의 풀 둘이 아니라 하나로 남는다.
	conn.insightsGlobal =
		clients.wafRegion === clients.region ? conn.insights : runner(clients.logsGlobal);

	// 리전을 정하는 것은 자격증명이고 config.json 은 그것을 기록할 뿐이다. 파일에
	// 낡은 리전을 남겨 두는 것이, 대시보드가 한 리전을 따지면서 다른 리전을
	// 부르게 만든 원인이었다.
	if (cfg.region !== clients.region) {
		logger.info("recording the credentials' region in the config", {
			was: cfg.region,
			now: clients.region
		});
		cfg = { ...cfg, region: clients.region };
		try {
			service.store.set(cfg);
		} catch (err) {
			logger.warn('could not save the region to the config', { error: message(err) });
		}
	}

	try {
		const identity = await whoAmI(
			clients.sts,
			clients.region,
			AbortSignal.timeout(identityTimeoutMs)
		);
		identity.wafRegion = clients.wafRegion;
		conn.identity = identity;
		logger.info('credentials accepted', {
			source,
			account: identity.account,
			region: identity.region,
			wafRegion: identity.wafRegion,
			key: redacted(creds)
		});
	} catch (err) {
		conn.error = new Error(`자격증명 확인 실패: ${message(err)}`);
	}
	return conn;
}

/** 키 하나가 통하는지를 판정하는 그 한 번의 호출에 두는 시한. */
export const identityTimeoutMs = 15_000;

/**
 * resolveConnection 은 어느 자격증명으로 돌지 고르고 그것으로 붙는다.
 *
 * 저장된 키가 .env 를 이긴다. 설정 화면이 어느 계정을 볼지에 대한 더 최근이고
 * 더 명시적인 표현이기 때문이다 — 거기 입력한 키가, 운영자가 존재조차 모를 수
 * 있는 파일에 밀려 무시되는 쪽이 둘 중 더 나쁜 놀라움이다. 진 쪽에 대해 침묵하지도
 * 않는다. 설정 화면이 지금 어느 출처가 힘을 쓰는지 이름으로 말한다.
 */
export async function resolveConnection(service: Service): Promise<AWSConn> {
	const saved = service.credentials.get();
	if (saved !== null) return connect(service, saved, 'saved');

	let creds: Credentials;
	try {
		creds = loadCredentials(service.envFile);
	} catch (err) {
		return noConnection('env', asError(err));
	}
	try {
		validateCredentials(creds);
	} catch (err) {
		return noConnection('none', asError(err));
	}
	return connect(service, creds, 'env');
}

/** 저장된 키의 저장소를 연다. */
export function openCredentialStore(): CredentialStore {
	return new CredentialStore(credentialsPath());
}

function asError(err: unknown): Error {
	return err instanceof Error ? err : new Error(String(err));
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}
