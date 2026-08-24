// 프로세스가 살아 있는 동안 유지되는 상태. internal/api/server.go 의 Service
// 구조체가 하던 일이다.
//
// 자격증명 실패를 치명적으로 다루지 않는 것이 핵심이다. 키가 없어도 UI 는
// 뜨고, 설정 화면이 무엇을 채워야 하는지 설명한다. 시작하자마자 죽으면
// 운영자는 이유를 볼 기회조차 얻지 못한다.

import type { Cache } from './aws/cache.ts';
import type { Clients } from './aws/client.ts';
import type { InsightsRunner } from './aws/insights.ts';
import type { MetricFetcher } from './aws/metrics.ts';
import type { Credentials } from './config/env.ts';
import type { ConfigStore } from './config/store.ts';
import type { Identity } from './contract.ts';

export interface Service {
	store: ConfigStore;

	/** AWS 클라이언트 묶음. 자격증명이 통하지 않았으면 null 이다. */
	clients: Clients | null;

	/** 짧은 창의 메모이제이션과 single-flight. */
	cache: Cache;

	/** 메트릭을 읽는 쪽. 클라이언트가 없으면 null 이다. */
	metrics: MetricFetcher | null;

	/** Insights 러너. 작업 리전용이다. */
	insights: InsightsRunner | null;

	/**
	 * WAF 리전을 조회하는 러너.
	 *
	 * CLOUDFRONT 범위 web ACL 은 로그를 us-east-1 에만 내보내므로, 작업 리전에
	 * 묶인 러너는 그 로그 그룹을 아예 볼 수 없다 — StartQuery 가 없는 그룹이라며
	 * 실패한다. 두 리전이 같으면 위와 같은 러너이고, 그래서 동시 실행 예산이
	 * 두 개가 아니라 하나의 풀로 남는다.
	 */
	insightsGlobal: InsightsRunner | null;

	/** AWS 에 닿지 못하는 이유. null 이면 정상이다. */
	credentialError: Error | null;

	/** 자격증명이 통했을 때의 신원. 아직 확인 전이면 null 이다. */
	identity: Identity | null;

	/**
	 * 자격증명을 읽은 .env 경로. 찾지 못했으면 빈 문자열이다.
	 *
	 * 힌트에 실려 나간다. "`.env` 를 고치세요" 가 실제 경로를 짚어야 두 곳 중
	 * 어느 쪽이 쓰였는지 운영자가 짐작하지 않는다.
	 */
	envFile: string;

	/** 저장된 설정을 읽으면서 고쳐야 했던 것. 빈 패널의 이유가 되므로 남긴다. */
	configNotices: string[];

	/**
	 * 검증 전의 자격증명. 시작 절차가 .env 를 읽는 단계와 AWS 에 붙는 단계로
	 * 나뉘어 있어 그 사이를 건너는 값이다.
	 */
	pendingCredentials?: Credentials | undefined;

	/** 창을 고정할 수 있게 열어 둔다. 테스트가 쓴다. */
	now: () => Date;

	/**
	 * 설정이 바뀌었을 때 캐시를 버린다. 리소스 선택이 바뀌면 옛 선택을 기준으로
	 * 캐시된 답은 다른 것을 설명하고 있기 때문이다.
	 */
	invalidateCache: () => void;
}

export function newService(store: ConfigStore, cache: Cache): Service {
	return {
		store,
		clients: null,
		cache,
		metrics: null,
		insights: null,
		insightsGlobal: null,
		credentialError: null,
		identity: null,
		envFile: '',
		configNotices: store.notices(),
		now: () => new Date(),
		invalidateCache: () => cache.invalidate()
	};
}

/**
 * credentialHint 는 무엇을 어디서 고쳐야 하는지 말한다.
 *
 * 설정 화면은 detail 보다 hint 를 먼저 보여 준다. 그래서 경로는 hint 를 타고
 * 가야 하고, detail 에만 넣으면 화면에 영영 뜨지 않는다.
 */
export function credentialHint(service: Service): string {
	if (service.envFile === '') {
		return (
			'AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION 을 담은 .env 를 ' +
			'실행 파일과 같은 폴더나 ~/.skills-dashboard 에 두고 다시 실행하세요.'
		);
	}
	return `${service.envFile} 에 AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION 을 설정한 뒤 다시 실행하세요.`;
}

/**
 * region 은 주 클라이언트가 향한 곳, wafRegion 은 CLOUDFRONT 범위 클라이언트가
 * 향한 곳이다.
 *
 * 둘 다 저장된 설정이 아니라 클라이언트를 읽는다. 클라이언트는 자격증명의
 * 리전으로 만들어졌고(newClients), config.json 은 아무것도 강제하지 않는 제
 * 나름의 리전을 들고 있다 — 호출이 어느 리전에 떨어지는지를 설정에 묻는 것이
 * 둘이 어긋나게 된 경위다. 클라이언트가 없으면 설명할 대상 자체가 없으므로,
 * 남는 답은 설정뿐이다.
 */
export function serviceRegion(service: Service): string {
	return service.clients !== null ? service.clients.region : service.store.get().region;
}

export function serviceWAFRegion(service: Service): string {
	return service.clients !== null ? service.clients.wafRegion : service.store.get().wafRegion;
}

/**
 * requireAWS 는 쓸 수 있는 자격증명이 있는지 본다. 없으면 그 이유를 담은 응답을
 * 돌려주고, 있으면 null 을 돌려준다.
 */
export function requireAWS(service: Service): { error: Error; hint: string } | null {
	if (service.credentialError !== null) {
		return { error: service.credentialError, hint: credentialHint(service) };
	}
	if (service.clients === null) {
		return { error: new Error('AWS clients are not configured'), hint: credentialHint(service) };
	}
	return null;
}
