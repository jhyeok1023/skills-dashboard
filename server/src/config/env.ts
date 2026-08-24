// 자격증명과 그것을 읽는 .env 를 다룬다. internal/config/env.go 의 이식이다.

import { readFileSync, statSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { configDir } from './store.ts';

/**
 * Credentials 는 .env 로 들어온 AWS 액세스 키다.
 *
 * 키는 디스크에서 메모리로만 읽고 되쓰지 않는다. 이 대시보드에는 무언가를
 * 저장하는 로그인 화면이 없다 — 키를 바꾼다는 것은 .env 를 고친다는 뜻이고,
 * 그 파일은 이미 .gitignore 에 있다.
 */
export interface Credentials {
	accessKeyId: string;
	secretAccessKey: string;
	sessionToken: string;
	region: string;
}

/**
 * redacted 는 화면과 로그에 쓸 모양으로 만든다. 비밀은 절대 되울리지 않고 키
 * ID 도 꼬리만 보인다 — 두 키를 구분하기에는 충분하고, 스크린샷에서 베껴 갈
 * 만큼은 아니다.
 */
export function redacted(c: Credentials): string {
	const id = c.accessKeyId.length > 4 ? '…' + c.accessKeyId.slice(-4) : c.accessKeyId;
	return `AccessKeyId=${id} Region=${c.region} SessionToken=${c.sessionToken !== ''}`;
}

/**
 * validateCredentials 는 호출을 시도할 만큼 갖춰졌는지만 본다. 키가 AWS 에서
 * 유효한지는 일부러 보지 않는다 — 그것은 identity 엔드포인트가 할 일이다.
 */
export function validateCredentials(c: Credentials): void {
	const missing: string[] = [];
	if (c.accessKeyId === '') missing.push('AWS_ACCESS_KEY_ID');
	if (c.secretAccessKey === '') missing.push('AWS_SECRET_ACCESS_KEY');
	if (c.region === '') missing.push('AWS_REGION');
	if (missing.length > 0) {
		throw new Error(`missing ${missing.join(', ')}; add them to your .env file`);
	}
}

/** 자격증명을 읽는 파일 이름. */
export const defaultEnvFile = '.env';

/**
 * 실행 파일이 있는 디렉터리.
 *
 * Go 는 os.Executable() 을 물었다. node 에서 그 자리에 해당하는 것은 지금 돌고
 * 있는 번들의 위치다 — process.execPath 는 node 자신을 가리키므로 쓸 수 없다.
 */
function bundleDir(): string {
	return dirname(fileURLToPath(import.meta.url));
}

/**
 * envFileCandidates 는 .env 를 찾는 자리를 순서대로 준다.
 *
 * 현재 작업 디렉터리는 일부러 빠져 있다. 배포된 실행 파일은 더블클릭으로 뜨거나
 * 마침 열려 있던 셸에서 뜨므로, cwd 는 운영자가 키를 어디에 두었는지에 대해
 * 아무 말도 하지 않는다 — 실행 파일 옆의 .env 가 한 디렉터리 위에서는 보이지
 * 않았고, 그것이 "대시보드가 .env 를 못 읽는다" 의 정체였다. 파일이 어디 사는지는
 * 설치의 성질이지 실행 방법의 성질이 아니다.
 */
export function envFileCandidates(exeDir: string, homeDir: string): string[] {
	const out: string[] = [];
	for (const dir of [exeDir, homeDir]) {
		if (dir === '') continue;
		const p = resolve(join(dir, defaultEnvFile));
		if (!out.includes(p)) out.push(p);
	}
	return out;
}

export interface ResolvedEnv {
	/** 읽기로 정한 파일. 아무 데도 없으면 빈 문자열이다. */
	path: string;
	/** 확인한 자리. 없을 때 무엇을 해야 하는지 말하려면 이것이 필요하다. */
	tried: string[];
}

/**
 * resolveEnvFile 은 읽을 .env 를 고른다.
 *
 * 명령줄로 준 경로는 준 그대로 쓰고, 반드시 있어야 한다. 거기서의 오타는 조용히
 * 빈 환경으로 흘러가는 대신 그렇다고 말해야 한다 — 예전의 말없이 지나가던
 * 동작이 밖에서 보기에 딱 그랬다. 그 외에는 먼저 있는 후보가 이긴다. 아무것도
 * 없으면 호출자가 목록을 돌려받는다. "자격증명이 없다" 는 어디를 봤는지와 함께여야
 * 비로소 행동할 수 있는 말이 되기 때문이다.
 *
 * 이름을 주지 않은 경우에 파일이 없는 것은 오류가 아니다 — 프로세스 환경이 이미
 * 값을 들고 있을 수 있다.
 */
export function resolveEnvFile(named: string): ResolvedEnv {
	if (named !== '') {
		const p = isAbsolute(named) ? named : resolve(named);
		statSync(p); // 없으면 던진다. 오타를 삼키지 않겠다는 뜻이다.
		return { path: named, tried: [named] };
	}

	const tried = envFileCandidates(bundleDir(), configDir());
	for (const p of tried) {
		try {
			statSync(p);
			return { path: p, tried };
		} catch {
			// 다음 후보로.
		}
	}
	return { path: '', tried };
}

/**
 * loadEnvFile 은 .env 를 맵으로 읽는다. 파일이 없는 것은 오류가 아니다 —
 * 프로세스 환경이 이미 값을 들고 있을 수 있다.
 */
export function loadEnvFile(path: string): Record<string, string> {
	// 빈 경로는 후보가 하나도 없었다고 resolveEnvFile 이 말한 것이다. 파일이
	// 없다는 뜻이고, 없는 파일과 같다.
	if (path === '') return {};

	let text: string;
	try {
		text = readFileSync(path, 'utf8');
	} catch (err) {
		if ((err as NodeJS.ErrnoException).code === 'ENOENT') return {};
		throw err;
	}

	const out: Record<string, string> = {};
	const lines = text.split(/\r?\n/);
	for (let i = 0; i < lines.length; i++) {
		const parsed = parseEnvLine(lines[i] as string);
		if (parsed === null) continue;
		if (parsed.key === '') {
			throw new Error(`${path}:${i + 1}: assignment with no name`);
		}
		out[parsed.key] = parsed.value;
	}
	return out;
}

/**
 * parseEnvLine 은 KEY=VALUE 한 줄을 읽는다. 이런 파일에 쌓이게 마련인 셸투의
 * 표기를 참아 준다 — 앞의 `export`, 값을 감싼 따옴표, 따옴표 없는 값 뒤의 주석.
 */
export function parseEnvLine(raw: string): { key: string; value: string } | null {
	let s = raw.trim();
	if (s === '' || s.startsWith('#')) return null;
	if (s.startsWith('export ')) s = s.slice('export '.length);

	const eq = s.indexOf('=');
	if (eq < 0) return null;

	const key = s.slice(0, eq).trim();
	let value = s.slice(eq + 1).trim();

	if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
		value = unescapeDoubleQuoted(value.slice(1, -1));
	} else if (value.length >= 2 && value.startsWith("'") && value.endsWith("'")) {
		value = value.slice(1, -1);
	} else {
		// 따옴표 없는 값은 이스케이프되지 않은 주석 표시에서 끝난다.
		const i = value.indexOf(' #');
		if (i >= 0) value = value.slice(0, i).trim();
	}
	return { key, value };
}

/**
 * unescapeDoubleQuoted 는 큰따옴표로 감싼 값 안의 이스케이프를 푼다.
 *
 * Go 는 strings.NewReplacer 를 썼다. 한 번만 훑고 바꾼 자리를 다시 보지 않는
 * 성질이 중요하다 — 순차로 치환하면 `\\n` 이 백슬래시가 된 뒤 다시 개행으로
 * 바뀐다. 정규식 한 번으로 훑는 것이 그 성질을 그대로 준다.
 */
function unescapeDoubleQuoted(value: string): string {
	return value.replace(/\\[n"\\]/g, (m) => {
		switch (m) {
			case '\\n':
				return '\n';
			case '\\"':
				return '"';
			default:
				return '\\';
		}
	});
}

/**
 * loadCredentials 는 .env 에서 자격증명을 읽고, 파일이 말하지 않는 값은 프로세스
 * 환경으로 메운다. 환경에 이미 있는 값은 파일이 침묵할 때만 이기므로, .env 가
 * 여전히 먼저 볼 곳으로 남는다.
 */
export function loadCredentials(envPath: string): Credentials {
	let vals: Record<string, string>;
	try {
		vals = loadEnvFile(envPath);
	} catch (err) {
		throw new Error(`read ${envPath}: ${err instanceof Error ? err.message : String(err)}`);
	}

	const get = (key: string): string => {
		const v = vals[key];
		if (v !== undefined && v !== '') return v;
		return process.env[key] ?? '';
	};

	const c: Credentials = {
		accessKeyId: get('AWS_ACCESS_KEY_ID'),
		secretAccessKey: get('AWS_SECRET_ACCESS_KEY'),
		sessionToken: get('AWS_SESSION_TOKEN'),
		region: get('AWS_REGION')
	};
	if (c.region === '') c.region = get('AWS_DEFAULT_REGION');
	return c;
}
