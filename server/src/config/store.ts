// 설정 파일을 읽고 쓴다. internal/config/config.go 의 Store 이식이다.

import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

import type { Config, HealthCheck, LogFormat, Meta } from '../contract.ts';
import { defaultLogFormat } from '../domain/logfmt.ts';
import { canonical, defaultConfig, repairConfig, validateConfig } from './config.ts';

/** 설정 파일이 사는 디렉터리. */
export function configDir(): string {
	return join(homedir(), '.skills-dashboard');
}

/** 설정 파일 자체. */
export function configPath(): string {
	return join(configDir(), 'config.json');
}

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * mergeStored 는 저장된 값을 기본값 위에 얹는다.
 *
 * Go 의 json.Unmarshal 이 하던 일이다 — 파일에 없는 필드는 건드리지 않으므로,
 * 예전 파일에 없던 설정이 생겨도 그 값만 기본값으로 남는다. JSON.parse 는 있는
 * 것만 주므로 중첩 객체까지 손으로 얹어야 같은 결과가 된다.
 */
function mergeStored(base: Config, raw: Record<string, unknown>): Config {
	const out: Config = canonical(base);

	for (const key of [
		'region',
		'wafRegion',
		'clusterName',
		'namespace',
		'podLogGroup',
		'wafLogGroup',
		'loadBalancer'
	] as const) {
		if (typeof raw[key] === 'string') out[key] = raw[key];
	}
	for (const key of ['targetGroups', 'rdsProxies', 'webAcls', 'wafHeaders'] as const) {
		const v = raw[key];
		if (Array.isArray(v)) out[key] = v.filter((x): x is string => typeof x === 'string');
	}
	if (isObject(raw['logFormat'])) {
		out.logFormat = { ...out.logFormat, ...(raw['logFormat'] as Partial<LogFormat>) };
	}
	if (isObject(raw['limits'])) {
		out.limits = { ...out.limits, ...(raw['limits'] as Partial<Meta['limits']>) };
	}
	if (isObject(raw['check'])) {
		out.check = { ...out.check, ...(raw['check'] as Partial<HealthCheck>) };
	}
	return out;
}

/**
 * ConfigStore 는 살아 있는 설정을 들고 접근을 정리한다.
 *
 * Go 는 여기에 RWMutex 를 걸었다. 설정 화면의 저장이 데이터 핸들러의 읽기와
 * 엇갈리지 못하게 하려는 것인데, node 는 한 줄로 돌기 때문에 그 엇갈림 자체가
 * 없다. 대신 Get 이 복사본을 주는 성질은 그대로 지킨다 — 핸들러가 들고 있던
 * 설정이 저장 한 번에 반쯤 바뀌어 있으면 그게 훨씬 찾기 어려운 버그다.
 */
export class ConfigStore {
	private cfg: Config;
	private readonly path: string;
	private readonly loadNotices: string[];

	private constructor(path: string, cfg: Config, notices: string[]) {
		this.path = path;
		this.cfg = cfg;
		this.loadNotices = notices;
	}

	/**
	 * load 는 path 의 설정을 읽고, 파일이 아직 없으면 기본값으로 시작한다.
	 *
	 * 파일 *내용* 은 무엇도 치명적이지 않다. 옛 빌드가 받아 준 값이나 누군가
	 * 잘못된 JSON 으로 고쳐 놓은 파일이 대시보드의 시작을 막아서는 안 된다 —
	 * 그런 것을 고치는 곳이 설정 화면이고, 종료한 프로세스는 설정 화면을
	 * 서비스하지 않는다. 파일을 아예 읽을 수 없는 것만 오류로 보고한다.
	 */
	static load(path: string): ConfigStore {
		const notices: string[] = [];
		let text: string;
		try {
			text = readFileSync(path, 'utf8');
		} catch (err) {
			if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
				return new ConfigStore(path, defaultConfig(), notices);
			}
			throw new Error(`read ${path}: ${err instanceof Error ? err.message : String(err)}`);
		}

		let raw: unknown;
		try {
			raw = JSON.parse(text);
			if (!isObject(raw)) throw new Error('설정 파일이 객체가 아닙니다');
		} catch (err) {
			notices.push(
				`설정 파일을 읽을 수 없어 기본값으로 시작했습니다 (${err instanceof Error ? err.message : String(err)}).`
			);
			// 기본값에서 시작한다는 것은 다음 저장이 파일을 덮어쓴다는 뜻이므로,
			// 원본을 잃게 두지 않고 옆으로 치운다.
			const kept = keepAside(path);
			if (kept !== null) notices.push(`원본은 ${kept} 에 두었습니다.`);
			return new ConfigStore(path, defaultConfig(), notices);
		}

		const storedLogFormat = raw['logFormat'];
		const hasLogPreset = isObject(storedLogFormat) && storedLogFormat['preset'] !== undefined;

		const cfg = mergeStored(defaultConfig(), raw);
		if (!hasLogPreset) {
			cfg.logFormat = defaultLogFormat();
			notices.push('기존 로그 파싱 규칙 → 지우고 Gin·JSON 자동 인식값을 적용했습니다.');
		}
		notices.push(...repairConfig(cfg));
		return new ConfigStore(path, cfg, notices);
	}

	/** 설정을 읽으면서 고쳐야 했던 것. 파일이 이미 쓸 만했으면 비어 있다. */
	notices(): string[] {
		return [...this.loadNotices];
	}

	/** 현재 설정의 복사본. */
	get(): Config {
		return canonical(this.cfg);
	}

	/** set 은 검사하고 저장한 뒤 파일에 쓴다. */
	set(cfg: Config): void {
		validateConfig(cfg);
		this.cfg = canonical(cfg);
		if (this.path === '') return;
		save(this.path, this.cfg);
	}
}

/** keepAside 는 이해할 수 없는 파일의 이름을 바꾸고 어디로 갔는지 돌려준다. */
function keepAside(path: string): string | null {
	const bak = path + '.bak';
	try {
		renameSync(path, bak);
		return bak;
	} catch {
		return null;
	}
}

function save(path: string, cfg: Config): void {
	mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
	const body = JSON.stringify(cfg, null, 2) + '\n';
	// 쓰고 나서 이름을 바꾼다. 저장이 중간에 끊겨도 다음 시작에서 파싱에
	// 실패하는 잘린 파일이 남지 않게 하려는 것이다.
	const tmp = path + '.tmp';
	writeFileSync(tmp, body, { mode: 0o600 });
	renameSync(tmp, path);
}

/** 파일이 이미 있는지. 시작 로그가 어디를 읽었는지 말할 때 쓴다. */
export function configExists(path: string): boolean {
	return existsSync(path);
}

const configKeys = [
	'region',
	'wafRegion',
	'clusterName',
	'namespace',
	'podLogGroup',
	'wafLogGroup',
	'loadBalancer',
	'targetGroups',
	'rdsProxies',
	'webAcls',
	'wafHeaders',
	'logFormat',
	'limits',
	'check'
] as const;

const logFormatKeys = new Set(Object.keys(defaultLogFormat()));

/**
 * mergeStrict 는 저장 요청 본문을 현재 설정 위에 얹되, 모르는 필드를 만나면
 * 거절한다.
 *
 * Go 는 DisallowUnknownFields 로 같은 일을 했다. 오타 난 키를 조용히 무시하면
 * 설정 화면은 저장에 성공했다고 말하고 값은 반영되지 않는다 — 그 조합이 가장
 * 오래 헤매게 만든다. 부분 본문이 언급하지 않은 필드를 지우지 않는 성질도
 * 그대로다.
 */
export function mergeStrict(base: Config, raw: Record<string, unknown>): Config {
	for (const key of Object.keys(raw)) {
		if (!(configKeys as readonly string[]).includes(key)) {
			throw new Error(`json: unknown field "${key}"`);
		}
	}
	if (isObject(raw['logFormat'])) {
		for (const key of Object.keys(raw['logFormat'])) {
			if (!logFormatKeys.has(key)) {
				throw new Error(`json: unknown field "${key}"`);
			}
		}
	}
	return mergeStored(base, raw);
}
