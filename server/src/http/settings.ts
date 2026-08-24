// 설정 화면이 쓰는 엔드포인트. internal/api/settings.go 의 이식이다.

import type { Context } from 'hono';

import type { Config, LogFormat, LogFormatPreview, Meta } from '../contract.ts';
import { mergeStrict } from '../config/store.ts';
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
import { credentialHint } from '../service.ts';
import { badRequest, fail, json } from './respond.ts';

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
