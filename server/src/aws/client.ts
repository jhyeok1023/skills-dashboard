// 서비스마다 클라이언트 하나. internal/awsx/client.go 의 이식이다.

import { Agent } from 'node:https';

import { CloudWatchClient } from '@aws-sdk/client-cloudwatch';
import { CloudWatchLogsClient } from '@aws-sdk/client-cloudwatch-logs';
import { EKSClient } from '@aws-sdk/client-eks';
import { ElasticLoadBalancingV2Client } from '@aws-sdk/client-elastic-load-balancing-v2';
import { RDSClient } from '@aws-sdk/client-rds';
import { GetCallerIdentityCommand, STSClient } from '@aws-sdk/client-sts';
import { WAFV2Client } from '@aws-sdk/client-wafv2';
import { NodeHttpHandler } from '@smithy/node-http-handler';

import type { Credentials } from '../config/env.ts';
import { validateCredentials } from '../config/env.ts';
import type { Identity } from '../contract.ts';

/**
 * CLOUDFRONT 범위 WAF 가 로그와 메트릭을 내보내는 곳. 배포가 어디서 트래픽을
 * 받든 여기다.
 */
export const globalWAFRegion = 'us-east-1';

/**
 * Clients 는 서비스마다 클라이언트를 하나씩 들고, 프로세스가 사는 동안 재사용한다.
 *
 * 한 번만 만드는 것이 중요하다. 이전 구현은 쿠버네티스 호출마다 — 새로고침마다
 * 여러 번, 10초에 한 번씩 — 새 http.Transport 를 만들고 하나도 닫지 않아서,
 * 놀고 있는 TLS 연결이 파일 디스크립터가 바닥날 때까지 쌓였다.
 */
export interface Clients {
	region: string;
	wafRegion: string;

	cw: CloudWatchClient;
	logs: CloudWatchLogsClient;
	sts: STSClient;
	elb: ElasticLoadBalancingV2Client;
	rds: RDSClient;
	waf: WAFV2Client;
	eks: EKSClient;

	// CLOUDFRONT 범위에 해당하는 것들. us-east-1 에 고정된다.
	cwGlobal: CloudWatchClient;
	logsGlobal: CloudWatchLogsClient;
	wafGlobal: WAFV2Client;
}

/**
 * 모든 서비스 클라이언트가 나눠 쓰는 HTTP 핸들러. 타임아웃이 AWS 호출 하나가
 * 요청을 얼마나 오래 붙들 수 있는지를 묶는다 — 이전 구현은 서버에도 AWS
 * 클라이언트에도 아무 시한을 두지 않아서, 느린 의존성이 막힌 핸들러를 그대로
 * 쌓아 올렸다.
 */
function httpHandler(): NodeHttpHandler {
	return new NodeHttpHandler({
		connectionTimeout: 10_000,
		requestTimeout: 60_000,
		httpsAgent: new Agent({
			keepAlive: true,
			maxSockets: 64,
			maxFreeSockets: 16,
			timeout: 90_000
		})
	});
}

/**
 * 재시도는 같은 요청을 고정 간격으로 다시 내는 대신 지터를 섞어 지수적으로
 * 물러선다. 스로틀링당한 계정을 전속력으로 재시도하면 계속 스로틀링당한다.
 *
 * SDK v3 의 standard 모드가 그 동작이다. 횟수만 Go 쪽과 맞춘다.
 */
const retryConfig = { maxAttempts: 5 };

/** newClients 는 주어진 자격증명으로 클라이언트 묶음을 만든다. */
export function newClients(creds: Credentials, wafRegion: string): Clients {
	validateCredentials(creds);
	const waf = wafRegion === '' ? globalWAFRegion : wafRegion;

	const shared = httpHandler();
	const credentials = {
		accessKeyId: creds.accessKeyId,
		secretAccessKey: creds.secretAccessKey,
		...(creds.sessionToken !== '' ? { sessionToken: creds.sessionToken } : {})
	};

	const base = (region: string) => ({
		region,
		credentials,
		requestHandler: shared,
		...retryConfig
	});

	const primary = base(creds.region);
	const clients: Clients = {
		region: creds.region,
		wafRegion: waf,
		cw: new CloudWatchClient(primary),
		logs: new CloudWatchLogsClient(primary),
		sts: new STSClient(primary),
		elb: new ElasticLoadBalancingV2Client(primary),
		rds: new RDSClient(primary),
		waf: new WAFV2Client(primary),
		eks: new EKSClient(primary),
		cwGlobal: undefined as unknown as CloudWatchClient,
		logsGlobal: undefined as unknown as CloudWatchLogsClient,
		wafGlobal: undefined as unknown as WAFV2Client
	};

	if (waf === creds.region) {
		clients.cwGlobal = clients.cw;
		clients.logsGlobal = clients.logs;
		clients.wafGlobal = clients.waf;
		return clients;
	}

	const global = base(waf);
	clients.cwGlobal = new CloudWatchClient(global);
	clients.logsGlobal = new CloudWatchLogsClient(global);
	clients.wafGlobal = new WAFV2Client(global);
	return clients;
}

/**
 * sendOptions 는 취소 신호를 SDK 가 받는 모양으로 감싼다.
 *
 * exactOptionalPropertyTypes 아래에서는 `{ abortSignal: undefined }` 조차 타입이
 * 맞지 않는다. 신호가 없으면 키 자체를 빼는 것이 옳고, 그 판단을 한 곳에 둔다.
 */
export function sendOptions(signal?: AbortSignal): { abortSignal?: AbortSignal } {
	return signal === undefined ? {} : { abortSignal: signal };
}

/**
 * whoAmI 는 자격증명을 AWS 에 대고 확인한다.
 *
 * 두 리전을 함께 보고하는 이유는 그것이 UI 밖에서 정해지기 때문이다 — 작업
 * 리전은 .env 가, WAF 리전은 설정 파일이 정한다. 빈 WAF 패널을 보는 운영자는
 * 대시보드가 실제로 어느 리전에 물었는지를 볼 수 있어야 한다.
 */
export async function whoAmI(
	sts: STSClient,
	region: string,
	signal?: AbortSignal
): Promise<Identity> {
	try {
		const out = await sts.send(new GetCallerIdentityCommand({}), sendOptions(signal));
		return {
			account: out.Account ?? '',
			arn: out.Arn ?? '',
			userId: out.UserId ?? '',
			region
		};
	} catch (err) {
		throw new Error(`GetCallerIdentity: ${err instanceof Error ? err.message : String(err)}`);
	}
}
