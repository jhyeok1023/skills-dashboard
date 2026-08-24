// 설정 화면에서 저장한 자격증명을 읽고 쓴다. internal/config/credstore.go 이식이다.

import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

import type { Credentials } from './env.ts';
import { validateCredentials } from './env.ts';
import { configDir } from './store.ts';

/**
 * 설정 화면에서 저장한 키가 떨어지는 파일. config.json 안이 아니라 옆이다 —
 * 설정 화면이 통째로 왕복시키는 파일에 시크릿을 둘 이유가 없고, 잘못된 설정을
 * 옆으로 치울 때 만드는 config.json.bak 에 사본이 남지 않게 하려는 것이기도
 * 하다.
 */
export function credentialsPath(): string {
	return join(configDir(), 'credentials.json');
}

/**
 * CredentialStore 는 설정 화면에서 저장한 키를 들고 있다.
 *
 * 이것이 생기기 전까지 대시보드는 .env 를 읽기만 하고 아무것도 쓰지 않았다.
 * 키를 바꾸려면 파일을 고치고 재시작해야 했다는 뜻이다. 이제 저장할 수 있고,
 * 대가는 실재한다 — 키가 사용자 홈 디렉터리에 평문으로, 0600 으로 남는다.
 * 로그에는 키 ID 끝 네 자리만 남는다 (redacted).
 *
 * 저장된 키가 .env 를 이긴다. 설정 화면이 어느 계정을 볼지에 대한 더 최근이고
 * 더 명시적인 표현이며, 둘 중 어느 쪽이 이겼는지는 화면이 그대로 말해 준다.
 */
export class CredentialStore {
	private creds: Credentials | null = null;
	private noticeText = '';

	constructor(private readonly path: string) {
		let body: string;
		try {
			body = readFileSync(path, 'utf8');
		} catch (err) {
			// 파일이 없는 것이 보통이다. 아직 아무것도 저장하지 않았다는 뜻이다.
			if ((err as NodeJS.ErrnoException).code !== 'ENOENT') {
				this.noticeText = `저장된 자격증명을 읽을 수 없어 무시했습니다 (${message(err)}).`;
			}
			return;
		}
		let parsed: unknown;
		try {
			parsed = JSON.parse(body);
		} catch (err) {
			this.noticeText = `저장된 자격증명이 깨져 있어 무시했습니다 (${message(err)}).`;
			return;
		}
		const creds = asCredentials(parsed);
		try {
			validateCredentials(creds);
		} catch {
			// 반쪽짜리 키는 없느니만 못하다. 멀쩡한 .env 를 이겨 놓고 모든 호출을
			// 수수께끼로 실패시킨다.
			this.noticeText = '저장된 자격증명이 불완전해 무시했습니다.';
			return;
		}
		this.creds = creds;
	}

	/** 파일을 읽으며 그냥 넘긴 것. 할 말이 없으면 빈 문자열이다. */
	notice(): string {
		return this.noticeText;
	}

	/** 저장된 키. 없으면 null 이다. */
	get(): Credentials | null {
		return this.creds;
	}

	/**
	 * set 은 검증하고 파일에 쓴다.
	 *
	 * validateCredentials 는 칸이 다 찼는지만 본다. 키가 실제로 통하는지는 이걸
	 * 부르기 전에 STS 로 판정한다 — AWS 에 닿지 못한 키는 절대 쓰이지 않으므로,
	 * 파일이 있다는 것은 한 번은 인증에 성공한 키라는 뜻이다.
	 */
	set(creds: Credentials): void {
		validateCredentials(creds);
		this.creds = creds;
		if (this.path === '') return;
		mkdirSync(dirname(this.path), { recursive: true, mode: 0o700 });
		const tmp = this.path + '.tmp';
		writeFileSync(tmp, JSON.stringify(creds, null, 2) + '\n', { mode: 0o600 });
		renameSync(tmp, this.path);
	}

	/**
	 * clear 는 저장된 키를 잊고 파일을 지운다. 대시보드가 .env 로 돌아가는 것이
	 * 이것이다. 없는 파일을 지우는 것은 성공이다.
	 */
	clear(): void {
		this.creds = null;
		if (this.path === '') return;
		rmSync(this.path, { force: true });
	}
}

function asCredentials(raw: unknown): Credentials {
	const o = (typeof raw === 'object' && raw !== null ? raw : {}) as Record<string, unknown>;
	const str = (key: string): string => (typeof o[key] === 'string' ? (o[key] as string) : '');
	return {
		accessKeyId: str('accessKeyId'),
		secretAccessKey: str('secretAccessKey'),
		sessionToken: str('sessionToken'),
		region: str('region')
	};
}

function message(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}
