// 프로세스가 살아 있는 동안 유지되는 상태. internal/api/server.go 의 Service
// 구조체가 하던 일이다.
//
// 자격증명 실패를 치명적으로 다루지 않는 것이 핵심이다. 키가 없어도 UI 는
// 뜨고, 설정 화면이 무엇을 채워야 하는지 설명한다. 시작하자마자 죽으면
// 운영자는 이유를 볼 기회조차 얻지 못한다.

import type { Cache } from './aws/cache.ts';
import type { AWSConn, CredentialSource } from './connect.ts';
import { connectionOk, noConnection } from './connect.ts';
import { CredentialStore } from './config/credstore.ts';
import type { Credentials } from './config/env.ts';
import type { ConfigStore } from './config/store.ts';

export interface Service {
	store: ConfigStore;

	/** 짧은 창의 메모이제이션과 single-flight. */
	cache: Cache;

	/** 설정 화면에서 저장한 키. 저장된 것이 없으면 get() 이 null 을 낸다. */
	credentials: CredentialStore;

	/**
	 * 지금 힘을 쓰는 AWS 연결.
	 *
	 * 통째로 교체된다 — 설정 화면이 키를 저장하면 여기가 새 객체로 바뀐다.
	 * 그래서 핸들러는 요청마다 한 번 집어서 그 사본을 끝까지 쓴다. await 사이에
	 * 교체가 끼어들어 한 페이지의 두 패널이 서로 다른 계정을 설명하는 일이
	 * 없어야 하기 때문이다.
	 */
	aws: AWSConn;

	/**
	 * 자격증명을 읽은 .env 경로. 찾지 못했으면 빈 문자열이다.
	 *
	 * 힌트에 실려 나간다. "`.env` 를 고치세요" 가 실제 경로를 짚어야 두 곳 중
	 * 어느 쪽이 쓰였는지 운영자가 짐작하지 않는다.
	 */
	envFile: string;

	/** 저장된 설정을 읽으면서 고쳐야 했던 것. 빈 패널의 이유가 되므로 남긴다. */
	configNotices: string[];

	/** 창을 고정할 수 있게 열어 둔다. 테스트가 쓴다. */
	now: () => Date;

	/**
	 * 설정이 바뀌었을 때 캐시를 버린다. 리소스 선택이 바뀌면 옛 선택을 기준으로
	 * 캐시된 답은 다른 것을 설명하고 있기 때문이다.
	 */
	invalidateCache: () => void;

	/**
	 * 키를 연결로 바꾸는 방법. 테스트만 바꿔 끼우고, 실제 실행은 connect 를
	 * 그대로 쓴다.
	 */
	connector?: (creds: Credentials, source: CredentialSource) => Promise<AWSConn>;
}

export function newService(store: ConfigStore, cache: Cache, credentials: CredentialStore): Service {
	return {
		store,
		cache,
		credentials,
		aws: noConnection('none', new Error('AWS clients are not configured')),
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
 *
 * 이제 화면 자체가 키를 저장할 수 있으므로 그것부터 말한다. .env 경로도 함께
 * 실어 보낸다 — 거기에 키를 둔 운영자가 왜 안 먹혔는지 가장 먼저 찾을 사람이다.
 */
export function credentialHint(service: Service): string {
	const fix = '설정 화면에서 AWS 액세스 키를 입력해 저장하세요.';
	if (service.envFile === '') {
		return `${fix} .env 로 주려면 실행 파일과 같은 폴더나 ~/.skills-dashboard 에 두고 다시 실행하세요.`;
	}
	return `${fix} 또는 ${service.envFile} 에 AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION 을 설정하세요.`;
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
	const clients = service.aws.clients;
	return clients !== null ? clients.region : service.store.get().region;
}

export function serviceWAFRegion(service: Service): string {
	const clients = service.aws.clients;
	return clients !== null ? clients.wafRegion : service.store.get().wafRegion;
}

/**
 * requireAWS 는 핸들러가 쓸 연결을 돌려주거나, 왜 없는지를 돌려준다. 스냅샷은
 * 요청마다 한 번만 잡는다 — 중간에 떨어진 저장이, 이미 조립 중인 페이지의 발밑을
 * 옮기면 안 된다.
 */
export function requireAWS(
	service: Service
): { conn: AWSConn; denied: null } | { conn: null; denied: { error: Error; hint: string } } {
	const conn = service.aws;
	if (!connectionOk(conn)) {
		return {
			conn: null,
			denied: {
				error: conn.error ?? new Error('AWS clients are not configured'),
				hint: credentialHint(service)
			}
		};
	}
	return { conn, denied: null };
}
