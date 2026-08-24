// 자격증명 엔드포인트. internal/api/credentials.go 이식이다.

import type { Context } from 'hono';

import type { AWSConn, CredentialSource } from '../connect.ts';
import {
	connect,
	identityTimeoutMs,
	noConnection,
	resolveConnection,
	setAWS
} from '../connect.ts';
import {
	loadCredentials,
	redacted,
	validateCredentials,
	type Credentials
} from '../config/env.ts';
import { logger } from '../log.ts';
import type { Service } from '../service.ts';
import { badRequest, fail, json } from './respond.ts';

/**
 * 설정 화면이 보고 고치는 모양.
 *
 * 키를 가리지 않고 통째로 돌려준다. 의도한 거래이고 대가가 있다 — 설정 화면을
 * 열 때마다 시크릿이 선을 건너고, 화면이 열려 있는 동안 브라우저 메모리에
 * 남는다. 얻는 것은 고칠 수 있는 폼이다. 가려진 칸은 고칠 수 없고 다시 치는
 * 수밖에 없는데, 기억으로 다시 친 키는 잘못 친 키다. 이 대시보드는 한 대에서
 * 자기 자신과 이야기하는 로컬 도구이므로, 늘어나는 노출 경로는 화면이다.
 */
interface CredentialsResponse extends Credentials {
	/**
	 * 대시보드가 지금 둘 중 어느 것으로 도는지. 진 파일을 고치고 있는 운영자를
	 * 막는 것이 이 필드가 있는 이유다.
	 */
	source: CredentialSource;
	/** .env 를 찾은 곳. 없었으면 생략된다. */
	envFile?: string;
	/** 지울 credentials.json 이 있는지. */
	saved: boolean;
}

function respond(service: Service, creds: Credentials, saved: boolean): CredentialsResponse {
	const out: CredentialsResponse = { ...creds, source: service.aws.source, saved };
	if (service.envFile !== '') out.envFile = service.envFile;
	return out;
}

/** 저장된 키가 없으면 .env 값으로 폼을 채워 준다. 지금 힘을 쓰는 값이다. */
function currentCredentials(service: Service): Credentials {
	const saved = service.credentials.get();
	if (saved !== null) return saved;
	try {
		return loadCredentials(service.envFile);
	} catch {
		return { accessKeyId: '', secretAccessKey: '', sessionToken: '', region: '' };
	}
}

export function handleGetCredentials(service: Service): Response {
	const saved = service.credentials.get();
	return json(200, respond(service, currentCredentials(service), saved !== null));
}

export async function handlePutCredentials(service: Service, c: Context): Promise<Response> {
	let raw: unknown;
	try {
		raw = await c.req.json();
	} catch (err) {
		return badRequest(new Error(`자격증명을 읽을 수 없습니다: ${message(err)}`));
	}
	let creds: Credentials;
	try {
		creds = readCredentials(raw);
		validateCredentials(creds);
	} catch (err) {
		return badRequest(err);
	}

	// 쓰기 전에 AWS 에 물어본다. 쓴 다음이 아니다. 그래서 파일이 있다는 것은 한
	// 번은 인증에 성공한 키라는 뜻이고, 오타는 폼이 아직 눈앞에 있을 때 드러난다.
	// 다른 화면의 빈 패널로 알게 되는 것이 아니라.
	const connector =
		service.connector ?? ((k: Credentials, s: CredentialSource) => connect(service, k, s));
	const conn = await bounded(() => connector(creds, 'saved'), 'saved');
	if (conn.error !== null) {
		return fail(502, conn.error, 'AWS 가 이 키를 받아들이지 않았습니다. 저장하지 않았습니다.');
	}
	try {
		service.credentials.set(creds);
	} catch (err) {
		return fail(500, err, '자격증명 파일을 쓰지 못했습니다.');
	}
	setAWS(service, conn);
	logger.info('credentials saved from the settings page', { key: redacted(creds) });
	return json(200, respond(service, creds, true));
}

/**
 * handleDeleteCredentials 는 저장된 키를 잊고 .env 로 돌아간다.
 *
 * 지우기만 하지 않고 다시 붙는 것이 핵심이다. 그러지 않으면 누군가 재시작할
 * 때까지 방금 버리라고 한 그 키로 계속 돈다 — 버튼이 말하는 것의 반대다.
 */
export async function handleDeleteCredentials(service: Service): Promise<Response> {
	try {
		service.credentials.clear();
	} catch (err) {
		return fail(500, err, '자격증명 파일을 지우지 못했습니다.');
	}
	const conn = await bounded(() => resolveConnection(service), 'none');
	setAWS(service, conn);
	logger.info('saved credentials cleared', { source: conn.source, error: conn.error?.message });
	return json(200, respond(service, currentCredentials(service), false));
}

/**
 * readCredentials 는 본문을 읽으면서 모르는 키를 거절한다. Go 는
 * DisallowUnknownFields 로 같은 일을 한다 — 오타 난 키를 조용히 무시하면 화면은
 * 저장에 성공했다고 말하고 값은 반영되지 않는다.
 */
function readCredentials(raw: unknown): Credentials {
	if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
		throw new Error('json: expected an object');
	}
	const o = raw as Record<string, unknown>;
	const known = ['accessKeyId', 'secretAccessKey', 'sessionToken', 'region'];
	for (const key of Object.keys(o)) {
		if (!known.includes(key)) throw new Error(`json: unknown field "${key}"`);
	}
	const str = (key: string): string => (typeof o[key] === 'string' ? (o[key] as string) : '');
	return {
		accessKeyId: str('accessKeyId'),
		secretAccessKey: str('secretAccessKey'),
		sessionToken: str('sessionToken'),
		region: str('region')
	};
}

/** 붙는 데 걸리는 시간의 상한. 신원 확인 자체의 시한에 여유를 얹은 값이다. */
const connectBudgetMs = identityTimeoutMs + 5_000;

/**
 * bounded 는 연결 시도를 시간으로 묶고, 무슨 일이 있어도 AWSConn 하나로 만들어
 * 낸다.
 *
 * SDK 의 재시도는 제 시한을 넘겨 늘어질 수 있다. 저장 버튼이 소리 없이 몇 분을
 * 앉아 있는 것보다 포기하고 말하는 편이 낫고, 실패를 던지는 대신 conn.error 로
 * 담는 것은 호출부가 성공과 실패를 같은 모양으로 다루게 하려는 것이다.
 */
async function bounded(
	start: () => Promise<AWSConn>,
	source: CredentialSource
): Promise<AWSConn> {
	let timer: NodeJS.Timeout | undefined;
	const guard = new Promise<AWSConn>((resolve) => {
		timer = setTimeout(
			() =>
				resolve(
					noConnection(
						source,
						new Error(`AWS 응답이 ${Math.round(connectBudgetMs / 1000)}초 안에 오지 않았습니다`)
					)
				),
			connectBudgetMs
		);
	});
	try {
		return await Promise.race([start().catch((err) => noConnection(source, asError(err))), guard]);
	} finally {
		clearTimeout(timer);
	}
}

function asError(err: unknown): Error {
	return err instanceof Error ? err : new Error(String(err));
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}
