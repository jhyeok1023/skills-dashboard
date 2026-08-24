// Go 정규식을 JS 정규식으로 옮긴다.
//
// 이 대시보드는 두 종류의 패턴을 다룬다. 하나는 코드에 박힌 것이고, 다른 하나는
// 운영자가 설정 화면에서 입력하는 textPattern·levelPattern 이다. 둘 다 원래는
// Go 의 RE2 로 컴파일됐고, 옮기면서 세 가지가 달라진다.
//
//   1. 문법. RE2 는 `(?i)` 같은 선행 플래그와 `(?P<name>)` 형태의 이름 있는
//      그룹을 쓴다. JS 는 플래그를 리터럴 밖에 두고 `(?<name>)` 을 쓴다.
//   2. 엔진. RE2 는 백트래킹을 하지 않아 입력 길이에 선형이다. JS 는 아니다.
//      그래서 여기서 패턴 길이와 대상 문자열 길이에 상한을 둔다 — 로컬 도구라
//      악의적 입력을 가정하지는 않지만, 붙여넣은 패턴 하나가 대시보드를 멈추게
//      할 수는 있다.
//   3. 지원 범위. RE2 에는 있고 JS 에 없는 것(예: `(?U)`)은 옮기지 않고 거절한다.
//      조용히 다르게 동작하는 것보다 낫다.

/** 패턴 길이 상한. 이보다 긴 것은 설정 실수이거나 붙여넣기 사고다. */
const maxPatternLength = 1024;

/** 매치 대상 길이 상한. 로그 한 줄이 이보다 길면 뒤는 보지 않는다. */
export const maxSubjectLength = 64 * 1024;

// 선행 인라인 플래그. `(?i)`, `(?is)` 처럼 패턴 맨 앞에 오는 것만 다룬다.
// 중간에 나오는 `(?i:...)` 는 최신 엔진만 받으므로 손대지 않는다 — 안 되면
// JS 쪽 컴파일이 실패하고, 그 실패가 그대로 설정 화면에 뜬다.
const leadingFlags = /^\(\?([a-zA-Z]+)\)/;

const supportedFlags: Record<string, string> = { i: 'i', s: 's', m: 'm' };

/**
 * compileGoRegexp 는 Go 문법의 패턴을 JS RegExp 로 만든다.
 *
 * label 은 실패 메시지 앞에 붙는다. Go 가 `levelPattern: …` 로 보고했으므로
 * 같은 접두사를 쓴다 — 설정 화면이 어느 칸을 고쳐야 하는지 그 한 마디로 안다.
 */
export function compileGoRegexp(label: string, pattern: string): RegExp {
	if (pattern.length > maxPatternLength) {
		throw new Error(`${label}: 패턴이 너무 깁니다 (${pattern.length}자, 상한 ${maxPatternLength}자)`);
	}

	let body = pattern;
	let flags = '';

	for (;;) {
		const found = leadingFlags.exec(body);
		if (!found) break;
		for (const flag of found[1] as string) {
			const mapped = supportedFlags[flag];
			if (!mapped) {
				throw new Error(`${label}: (?${flag}) 플래그는 지원하지 않습니다`);
			}
			if (!flags.includes(mapped)) flags += mapped;
		}
		body = body.slice(found[0].length);
	}

	// (?P<name>…) → (?<name>…). 문자 클래스 안에 이 세 글자가 그대로 있는
	// 경우까지 가려내지는 않는다. `[(?P<]` 같은 클래스는 실전에 없다.
	body = body.replace(/\(\?P</g, '(?<');

	try {
		return new RegExp(body, flags);
	} catch (err) {
		throw new Error(`${label}: ${err instanceof Error ? err.message : String(err)}`);
	}
}

/**
 * execNamed 는 상한을 지킨 채 매치하고 이름 있는 그룹을 돌려준다.
 *
 * 잘라 내는 것이 결과를 바꿀 수 있다는 점은 인정하고 간다. 64KB 를 넘는 로그
 * 한 줄에서 뒷부분의 상태 코드를 놓치는 것과, 그 한 줄 때문에 패널 전체가
 * 응답하지 않는 것 중 앞쪽을 고른 것이다.
 */
export function execNamed(re: RegExp, subject: string): RegExpExecArray | null {
	const bounded = subject.length > maxSubjectLength ? subject.slice(0, maxSubjectLength) : subject;
	// lastIndex 는 g·y 플래그가 붙었을 때만 움직이지만, 재사용되는 RegExp 가
	// 이전 매치 위치를 들고 있으면 같은 입력에 다른 답을 낸다.
	re.lastIndex = 0;
	return re.exec(bounded);
}
