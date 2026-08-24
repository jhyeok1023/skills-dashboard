// 대시보드가 무엇을 지켜보는지, 어떻게 읽는지를 기록한다.
// internal/config/config.go 의 이식이다.
//
// 디스크에 쓰이므로 선택이 재시작을 넘겨 살아남는다. 자격증명은 여기에 절대
// 들어가지 않는다.

import type { Config, HealthCheck, LogFormat, Meta } from '../contract.ts';
import { loadBalancerDimension, targetGroupDimension } from '../domain/dimensions.ts';
import { defaultLogFormat, validateLogFormat } from '../domain/logfmt.ts';
import { isQueryableHeader } from '../domain/query.ts';

export type Limits = Meta['limits'];

/** 한 명의 운영자가 클러스터 하나를 볼 때에 맞춘 값. */
export function defaultLimits(): Limits {
	return {
		// 상세 목록의 상한. 총계는 언제나 따로 세므로 목록을 자른다고 옆의
		// 숫자가 일그러지지 않는다.
		logRows: 300,
		// 분해 표의 상한.
		topN: 20,
		// WAF 페이지는 Insights 쿼리를 일곱 개 던진다. 패널을 동시에 만들기
		// 때문에(handlePage) 세 묶음이 아니라 한 파도로 도착한다. 여섯이면
		// 쿼리 하나가 다른 쿼리의 지연을 통째로 기다린 뒤에야 시작했다. WAF
		// 리전이 작업 리전과 다르면 러너가 둘이고 각자 세마포어를 가지므로
		// 최악의 경우 열여섯이다 — CloudWatch 의 서른에 견주면 여유가 있다.
		insightsConcurrency: 8,
		queryTimeoutSeconds: 45,
		// 한 주기. 로그 쿼리 캐시 키가 창의 경계를 고정하고(runLogQueries)
		// newWindow 가 끝을 주기로 내림하므로, 키 하나는 정확히 한 주기 동안
		// 닿을 수 있고 그 뒤로는 영영 닿지 않는다. 기본 1분 주기에서 30초로
		// 두면 매 분의 뒷절반이 이미 메모리에 답이 있는 키에 대한 확정된
		// 미스였다.
		//
		// 더 늘리지 않은 것도 의도다. 창은 고정이지만 그 내용은 아니다 —
		// CloudWatch 는 레코드를 늦게 배달하므로 같은 창에 대한 같은 쿼리가
		// 1분 뒤에 더 많은 행을 낼 수 있다. 키가 사는 만큼만 사는 것이 이
		// 캐시가 주장할 수 있는 최대다.
		cacheTtlSeconds: 60
	};
}

/** 리소스 이름 말고는 전부 채워진 설정. 이름은 발견하거나 입력해야 한다. */
export function defaultConfig(): Config {
	return {
		region: 'ap-northeast-2',
		wafRegion: 'us-east-1',
		clusterName: '',
		namespace: 'default',
		podLogGroup: '',
		wafLogGroup: '',
		loadBalancer: '',
		targetGroups: [],
		rdsProxies: [],
		webAcls: [],
		wafHeaders: defaultWAFHeaders(),
		logFormat: defaultLogFormat(),
		limits: defaultLimits(),
		check: { url: '', expectStatus: 0 }
	};
}

/** WAF 트래픽을 분해할 기본 헤더. 하나마다 창을 한 번 더 스캔하므로 짧게 둔다. */
export function defaultWAFHeaders(): string[] {
	return ['Host', 'User-Agent'];
}

/**
 * podLogGroupOrDefault 는 명시하지 않았을 때 클러스터 이름에서 Container
 * Insights 애플리케이션 로그 그룹을 유도한다.
 */
export function podLogGroupOrDefault(c: Config): string {
	if (c.podLogGroup !== '') return c.podLogGroup;
	if (c.clusterName === '') return '';
	return `/aws/containerinsights/${c.clusterName}/application`;
}

/**
 * canonical 은 설정을 Go 의 구조체 필드 순서대로 다시 만든다. 배열은 전부
 * 새것으로 복사하고, 비어 있으면 빈 배열로 둔다.
 *
 * 순서를 맞추는 이유는 두 엔진이 같은 config.json 을 써야 하기 때문이다. Go 는
 * 필드 선언 순서로 마샬하지만 JS 는 객체에 키가 꽂힌 순서로 낸다 — 저장된
 * 파일에서 읽은 그대로 쓰면 두 엔진이 같은 내용을 다른 줄 순서로 저장하고,
 * 엔진을 바꿀 때마다 파일 전체가 바뀐 것처럼 보인다.
 *
 * 빈 배열로 시작하는 것은 `null` 을 와이어에서 막기 위해서다. Go 의 nil 슬라이스는
 * `null` 로 마샬되는데, 브라우저의 Config 는 이것을 `string[]` 로 선언하고
 * 설정 화면의 첫 `.filter` 가 거기서 터진다 — 그러면 모니터링 대상 구획 전체가
 * 사라져, 리소스 칸이 아예 없는 화면처럼 보인다. 아무것도 고르지 않은 상태는
 * 갓 설치한 시스템의 상태이므로, 그게 모든 첫 실행이었다.
 */
export function canonical(c: Config): Config {
	return {
		region: c.region,
		wafRegion: c.wafRegion,
		clusterName: c.clusterName,
		namespace: c.namespace,
		podLogGroup: c.podLogGroup,
		wafLogGroup: c.wafLogGroup,
		loadBalancer: c.loadBalancer,
		targetGroups: [...(c.targetGroups ?? [])],
		rdsProxies: [...(c.rdsProxies ?? [])],
		webAcls: [...(c.webAcls ?? [])],
		wafHeaders: [...(c.wafHeaders ?? [])],
		logFormat: canonicalLogFormat(c.logFormat),
		limits: { ...c.limits },
		check: { ...c.check }
	};
}

function canonicalLogFormat(f: LogFormat): LogFormat {
	return {
		preset: f.preset,
		timeField: f.timeField,
		messageField: f.messageField,
		processedField: f.processedField,
		streamField: f.streamField,
		appField: f.appField,
		latencyField: f.latencyField,
		latencyUnit: f.latencyUnit,
		statusField: f.statusField,
		methodField: f.methodField,
		pathField: f.pathField,
		levelField: f.levelField,
		clientIpField: f.clientIpField,
		userAgentField: f.userAgentField,
		textPattern: f.textPattern,
		levelPattern: f.levelPattern,
		namespace: f.namespace,
		okStatuses: [...(f.okStatuses ?? [])],
		excludePaths: [...(f.excludePaths ?? [])]
	};
}

/** OK 는 이 상태 코드를 정상으로 보는지 답한다. 0 이면 2xx 전부다. */
export function checkOK(h: HealthCheck, status: number): boolean {
	if (h.expectStatus > 0) return status === h.expectStatus;
	return status >= 200 && status < 300;
}

/**
 * problem 은 이 설정으로 할 수 없는 것 하나와, 그것 없이 쓸 수 있게 만드는
 * 방법이다.
 *
 * 저장 경로와 적재 경로가 같은 목록을 읽는다. 설정 화면이 강제하는 규칙과
 * 로더가 적용하는 규칙이 어긋날 수 없게 하려는 것이다.
 */
interface Problem {
	/** 무엇이 잘못됐는지. 저장 경로에서는 이것이 오류 문구가 된다. */
	msg: string;
	/** 그래서 어떻게 했는지. 적재 경로에서 뒤에 붙는다. */
	dropped: string;
	drop: (c: Config) => void;
}

/**
 * albDimensionRe 는 CloudWatch 가 Application Load Balancer 에 주는 모양이다:
 * app/<이름>/<ID>. 이 대시보드는 AWS/ApplicationELB 만 읽으므로 NLB 나 게이트웨이
 * 차원은 이름이나 ARN 만큼이나 쓸모가 없다.
 */
const albDimensionRe = /^app\/[^/]+\/[0-9a-fA-F]+$/;

/**
 * checkURLProblem 은 점검 주소를 요청할 수 없는 이유를 말한다. 요청할 수 있으면
 * 빈 문자열이다. 저장 경로와 적재 경로가 모두 여기를 지나므로, 설정 화면은
 * 로더가 버렸을 것을 정확히 그대로 거절한다.
 */
function checkURLProblem(raw: string): string {
	let u: URL;
	try {
		u = new URL(raw);
	} catch {
		// Go 의 url.Parse 는 스킴 없는 주소도 받아들이고 Scheme 을 비운다.
		// JS 의 URL 은 던진다. 같은 답을 내려면 여기서 갈라 준다.
		return /^https?:/i.test(raw) ? '호스트가 없습니다' : 'http 또는 https 로 시작해야 합니다';
	}
	if (u.protocol !== 'http:' && u.protocol !== 'https:') {
		return 'http 또는 https 로 시작해야 합니다';
	}
	if (u.host === '') return '호스트가 없습니다';
	return '';
}

/**
 * normaliseDimensions 는 로드밸런서와 타겟 그룹 항목을 CloudWatch 가 기대하는
 * 형태로 바꾼다.
 *
 * 두 필드는 ARN 이 아니라 CloudWatch 차원 값을 담는다. 그런데 SEARCH 값
 * 정규식(searchValueRe)이 ':' 와 '/' 를 받아 주므로, 여기 붙여넣은 ARN 은 모든
 * 검사를 통과한 뒤 어떤 메트릭에도 맞지 않는다. 패널은 경고 없이 비고, 그것은
 * "이 로드밸런서에 트래픽이 없었다" 로 읽힌다. 바꿀 수 있는 것을 바꾸는 것이
 * 그 침묵을 피하는 방법이고, 변환으로 구제되지 않는 것은 inspect 가 말한다.
 */
function normaliseDimensions(c: Config): void {
	c.loadBalancer = loadBalancerDimension(c.loadBalancer.trim());
	c.targetGroups = c.targetGroups.map((tg) => targetGroupDimension(tg.trim()));
}

/**
 * fillDefaults 는 비어 있는 것을 채우고 바꿀 수 있는 것을 바꾼다. 여기서는
 * 아무것도 실패할 수 없다 — 바꿔도 쓸 수 없는 값은 inspect 가 대신 보고한다.
 */
function fillDefaults(c: Config): void {
	c.targetGroups ??= [];
	c.rdsProxies ??= [];
	c.webAcls ??= [];
	c.wafHeaders ??= [];
	if (c.wafRegion === '') c.wafRegion = 'us-east-1';

	const d = defaultLimits();
	if (!(c.limits.logRows > 0)) c.limits.logRows = d.logRows;
	if (!(c.limits.topN > 0)) c.limits.topN = d.topN;
	if (!(c.limits.insightsConcurrency > 0)) c.limits.insightsConcurrency = d.insightsConcurrency;
	if (!(c.limits.queryTimeoutSeconds > 0)) c.limits.queryTimeoutSeconds = d.queryTimeoutSeconds;
	if (!(c.limits.cacheTtlSeconds >= 0)) c.limits.cacheTtlSeconds = d.cacheTtlSeconds;

	if ((c.logFormat.okStatuses ?? []).length === 0) {
		c.logFormat.okStatuses = defaultLogFormat().okStatuses;
	}
	// excludePaths 는 비어 있어도 그대로 둔다. 목록을 지운 운영자는 프로브
	// 트래픽을 다시 보고 싶은 것이고, 조용히 기본값을 되돌리면 그럴 방법이
	// 없어진다.
	c.logFormat.excludePaths = (c.logFormat.excludePaths ?? []).map((p) => p.trim());
	normaliseDimensions(c);
}

/** inspect 는 기본값을 채운 뒤 그래도 쓸 수 없는 것을 돌려준다. */
function inspect(c: Config): Problem[] {
	fillDefaults(c);

	const out: Problem[] = [];
	if (c.region === '') {
		out.push({
			msg: 'region이 설정되지 않았습니다',
			dropped: `기본 리전 ${defaultConfig().region} 을 사용합니다.`,
			drop: (x) => {
				x.region = defaultConfig().region;
			}
		});
	}
	if (c.loadBalancer !== '' && !albDimensionRe.test(c.loadBalancer)) {
		out.push({
			msg:
				`loadBalancer "${c.loadBalancer}"는 CloudWatch 차원 값이 아닙니다. ` +
				'app/<이름>/<ID> 형식이어야 합니다 (예: app/my-alb/50dc6c495c0c9188). ' +
				'전체 ARN을 붙여넣어도 됩니다',
			dropped: '이 값을 비웠습니다. 설정에서 다시 선택하세요.',
			drop: (x) => {
				x.loadBalancer = '';
			}
		});
	}
	// 이 파일에서 대시보드가 스스로 요청하는 유일한 주소다. 그래서 글이 아니라
	// 주소로서 검사해야 하는 유일한 값이기도 하다. http·https 가 아닌 스킴은
	// 저장된 설정이 프로세스를 file:// 이나 사용자 정의 핸들러로 향하게 만든다.
	// 경계에서 거절하는 편이 스킴마다 무슨 일이 벌어질지 따지는 것보다 싸다.
	if (c.check.url !== '') {
		const reason = checkURLProblem(c.check.url);
		if (reason !== '') {
			out.push({
				msg: `check.url "${c.check.url}"는 사용할 수 없습니다: ${reason}`,
				dropped: '이 값을 비웠습니다. 설정에서 다시 입력하세요.',
				drop: (x) => {
					x.check = { url: '', expectStatus: 0 };
				}
			});
		}
	}
	if (c.check.expectStatus !== 0 && (c.check.expectStatus < 100 || c.check.expectStatus > 599)) {
		out.push({
			msg: `check.expectStatus ${c.check.expectStatus}는 HTTP 상태 코드가 아닙니다`,
			dropped: '2xx 를 정상으로 보도록 되돌렸습니다.',
			drop: (x) => {
				x.check.expectStatus = 0;
			}
		});
	}
	try {
		validateLogFormat(c.logFormat);
	} catch (err) {
		out.push({
			msg: `logFormat: ${err instanceof Error ? err.message : String(err)}`,
			dropped: '로그 형식을 기본값으로 되돌렸습니다.',
			drop: (x) => {
				x.logFormat = defaultLogFormat();
			}
		});
	}
	// 넣을 수 없는 헤더만 버린다. 나머지 목록은 쓸 만한 선택이므로 살아남는다.
	const badHeaders = c.wafHeaders.filter((h) => !isQueryableHeader(h));
	if (badHeaders.length > 0) {
		out.push({
			msg: `wafHeaders: ${badHeaders.join(', ')} 는 쿼리에 넣을 수 없는 헤더 이름입니다`,
			dropped: '해당 헤더를 목록에서 제외했습니다.',
			drop: (x) => {
				x.wafHeaders = x.wafHeaders.filter((h) => isQueryableHeader(h));
			}
		});
	}
	return out;
}

/**
 * validateConfig 는 비어 있는 것을 채우고 쓸 수 없는 것을 보고한다. 저장
 * 경로다 — 사람이 설정 화면을 보고 있으므로, 쓰기를 거절하고 이유를 말하는
 * 것이 그들이 온 이유다.
 */
export function validateConfig(c: Config): void {
	const ps = inspect(c);
	if (ps.length === 0) return;
	throw new Error(ps.map((p) => p.msg).join('; '));
}

/**
 * repairConfig 는 기본값을 채우고 남은 못 쓸 것을 버린 뒤, 버린 것마다 한 줄씩
 * 돌려준다.
 *
 * 적재 경로이고, 의도적으로 실패하지 않는다. validateConfig 가 거절할 값 —
 * 옛 빌드가 받아 준 값이거나 손으로 고친 파일 — 이 대시보드의 시작을 막아서는
 * 안 된다. 그것을 고칠 수 있는 곳은 설정 화면뿐인데, 종료한 프로세스에서는
 * 거기에 닿을 수 없다. 자격증명이 이미 맺고 있는 것과 같은 거래다.
 */
export function repairConfig(c: Config): string[] {
	return inspect(c).map((p) => {
		p.drop(c);
		return `${p.msg} → ${p.dropped}`;
	});
}
