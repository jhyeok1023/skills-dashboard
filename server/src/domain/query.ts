// Logs Insights 쿼리를 만든다. internal/domain/query.go 의 이식이다.
//
// TODO: 팟 로그와 WAF 쿼리 빌더 전체는 다음 단계에서 옮긴다. 지금 여기 있는
// 것은 설정 저장 경로가 먼저 필요로 하는 부분뿐이다 — wafHeaders 에 들어온
// 이름이 쿼리에 넣을 수 있는 것인지 판단하는 자리.

/**
 * headerNameRe 는 운영자가 준 헤더 이름을 RFC 9110 이 필드 이름에 허용하는
 * 문자로 묶는다. 그래야 아래 parse 패턴에 그대로 끼워 넣어도 이스케이프
 * 사고가 나지 않는다.
 */
const headerNameRe = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/;

/** Go 의 regexp.QuoteMeta. 정규식 메타문자를 이스케이프한다. */
export function quoteMeta(s: string): string {
	return s.replace(/[\\.+*?()|[\]{}^$]/g, (ch) => '\\' + ch);
}

/**
 * headerParse 는 WAF 레코드에서 요청 헤더 하나를 꺼내 alias 에 묶는 명령을
 * 만든다.
 *
 * WAF 는 헤더를 {name, value} 객체의 배열로 저장한다. Insights 는 배열 원소로
 * 그룹을 지을 수 없고, 위치로 인덱싱하는 것(headers.0.value)은 요청마다 순서가
 * 달라 의미가 없다. 그래서 원본 레코드에서 parse 로 값을 끌어낸다. 이전 구현은
 * 같은 문제를 저장된 모든 행에 대한 SQLite json_each 크로스 조인으로 풀었고,
 * 그것이 그 구현에서 가장 비싼 쿼리였다.
 *
 * 헤더별 분해와 최근 요청 목록이 둘 다 이것을 쓰고, 둘이 똑같이 동작해야 한다 —
 * 헤더가 안 잡히기 시작했을 때 들여다볼 정규식이 하나여야 한다.
 */
export function headerParse(name: string, alias: string): string {
	if (!headerNameRe.test(name)) {
		throw new Error(`invalid header name "${name}"`);
	}
	return `parse @message /"name":"(?i)${quoteMeta(name)}","value":"(?<${alias}>[^"]*)"/`;
}

/** isQueryableHeader 는 이 헤더 이름을 쿼리에 넣을 수 있는지만 본다. */
export function isQueryableHeader(name: string): boolean {
	return headerNameRe.test(name);
}
