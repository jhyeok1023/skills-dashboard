// 프로세스가 살아 있는 동안 유지되는 상태. internal/api/server.go 의 Service
// 구조체가 하던 일이다.
//
// 자격증명 실패를 치명적으로 다루지 않는 것이 핵심이다. 키가 없어도 UI 는
// 뜨고, 설정 화면이 무엇을 채워야 하는지 설명한다. 시작하자마자 죽으면
// 운영자는 이유를 볼 기회조차 얻지 못한다.

export interface Service {
	/** AWS 에 닿지 못하는 이유. null 이면 정상이다. */
	credentialError: Error | null;

	/**
	 * 자격증명을 읽은 .env 경로. 찾지 못했으면 빈 문자열이다.
	 *
	 * 힌트에 실려 나간다. "`.env` 를 고치세요" 가 실제 경로를 짚어야 두 곳 중
	 * 어느 쪽이 쓰였는지 운영자가 짐작하지 않는다.
	 */
	envFile: string;

	/** 저장된 설정을 읽으면서 고쳐야 했던 것. 빈 패널의 이유가 되기 때문에 남긴다. */
	configNotices: string[];

	/** 창을 고정할 수 있게 열어 둔다. 테스트가 쓴다. */
	now: () => Date;
}

export function newService(): Service {
	return {
		credentialError: null,
		envFile: '',
		configNotices: [],
		now: () => new Date()
	};
}

/**
 * credentialHint 는 무엇을 어디서 고쳐야 하는지 말한다.
 *
 * 설정 화면은 detail 보다 hint 를 먼저 보여 준다. 그래서 경로는 hint 를 타고
 * 가야 하고, detail 에만 넣으면 화면에 영영 뜨지 않는다.
 */
export function credentialHint(service: Service): string {
	if (service.envFile === '') {
		return (
			'AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION 을 담은 .env 를 ' +
			'실행 파일과 같은 폴더나 ~/.skills-dashboard 에 두고 다시 실행하세요.'
		);
	}
	return `${service.envFile} 에 AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION 을 설정한 뒤 다시 실행하세요.`;
}
