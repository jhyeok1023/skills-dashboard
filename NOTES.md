# NOTES

## 현재 상태

- 구성: Go 백엔드(단일 바이너리, 웹 UI 임베드) + SvelteKit SPA. 데이터 소스는 CloudWatch 단일. `mise`가 툴체인과 빌드 커맨드를 관리.
- 미해결:
  - 팟 애플리케이션 로그의 최종 형식 미확정. 현재 필드 매핑은 참고 구현의 실제 샘플에서 역산한 기본값이며, 설정 화면의 "샘플 붙여넣기 → 파싱 미리보기"로 확인 후 조정 필요.
  - 팟 min/max는 AWS API만으로 얻을 수 없어 구간 내 관측값을 사용. 정확한 HPA 값이 필요해지면 K8s API 도입 여부를 다시 결정해야 함.
  - 실제 AWS 계정에 대한 엔드투엔드 확인 미실시(자격증명 없음). 모든 검증은 인터페이스 목킹과 픽스처 기반.

## 결정 로그
<!-- append -->

### 2026-08-10 로드 밸런서·타겟 그룹 값은 저장할 때 CloudWatch 차원으로 정규화하고, 안 되면 거부한다

- 맥락: `loadBalancer`에는 CloudWatch `LoadBalancer` 차원(`app/my-alb/50dc6c495c0c9188`)이 들어가야 하는데, 설정 화면은 안내 없는 자유 입력이었고 `Config.Validate()`에 검증이 없었다. 메트릭 SEARCH의 값 정규식(`domain.searchValueRe`)이 `:` 와 `/` 를 허용하므로 전체 ARN을 붙여넣어도 모든 검사를 통과한 뒤 아무 메트릭도 매칭하지 않는다. 결과는 에러 없는 빈 차트 — "트래픽이 없는 로드 밸런서"와 구분되지 않는다. 타겟 그룹을 자동 조회해 체크하는 경로에서만 우연히 올바른 값이 채워졌다.
- 채택: `Config.Validate()`에서 `domain.LoadBalancerDimension` / `TargetGroupDimension`으로 정규화한 뒤, 로드 밸런서가 `app/<이름>/<hex>` 꼴이 아니면 저장을 거부한다. UI 저장·손으로 고친 `config.json`·시작 시 로드가 모두 같은 경로를 탄다. 설정 화면에는 `loadbalancers` 자동 조회와 플레이스홀더를 붙였다.
- 기각: ⓐ API 계층(`handlePutConfig`)에서만 정규화 — 손으로 고친 설정 파일이 빠진다. ⓑ 경고만 하고 저장은 허용 — 조용한 빈 차트가 그대로 남는다.
- 대가: 변환 함수 세 개(`LoadBalancerDimension` / `TargetGroupDimension` / `FriendlyTargetGroupName`)를 `awsx`에서 `domain`으로 옮겼다. `awsx`가 `config`를 import하므로 `config`에서 `awsx`를 쓰면 순환이 된다. NLB/GWLB 차원은 거부된다 — 대시보드가 읽는 네임스페이스는 `AWS/ApplicationELB` 하나뿐이다.

### 2026-08-10 WAF 로그는 WAF 리전 클라이언트로 조회하고, 리전 판단은 Clients를 출처로 삼는다

- 맥락: CLOUDFRONT 스코프 웹 ACL은 us-east-1에만 로그를 남긴다. `Config.WAFRegion`(기본 `us-east-1`)과 그 리전에 핀된 `Clients.LogsGlobal`이 이미 있었는데 `LogsGlobal`은 프로덕션 코드에서 한 번도 쓰이지 않았다. Insights 러너는 `clients.Logs` 하나만 물고 있어 us-east-1 그룹 이름을 직접 넣어도 `StartQuery`가 없는 그룹으로 실패했고, 로그 그룹 자동 조회도 작업 리전만 봐서 `aws-waf-logs-*` 가 아예 목록에 없었다. 리전 판단도 두 갈래였다 — `creds.Validate()`가 빈 리전을 먼저 거르므로 `main.go`의 `cfg.Region` 폴백은 도달 불가였고(즉 `config.json`의 `region`은 클라이언트에 영향이 없었다), 그런데 WAF 메트릭 패널은 그 `cfg.Region`을 비교해 `CWGlobal` 사용 여부를 정했다.
- 채택: WAF 로그 전용 `InsightsGlobal` 러너를 두고, 러너·리전·로그 그룹을 `logSource` 한 덩어리로 묶어 패널 빌더에 넘긴다. 캐시 키에 리전을 포함해 두 리전의 동명 그룹이 서로의 결과를 받지 않게 했다. 조회 kind `waf-loggroups` 를 추가해 목록도 WAF 리전에서 읽는다. 리전 판단의 출처는 `Clients.Region` / `Clients.WAFRegion` 하나로 모으고, 시작 시 `cfg.Region`을 실제 자격증명 리전으로 맞춰 저장한다.
- 기각: ⓐ 패널마다 `if wafRegion != region` 분기 — 이미 있던 그 분기가 어긋난 원인이었다. ⓑ 리전을 설정 화면에서 편집 가능하게 — 클라이언트는 시작 시 1회 생성이라 재시작 없이는 적용되지 않는다. 읽기 전용 표시로 두고 어디서 바꾸는지 안내한다.
- 대가: 두 리전이 다르면 Insights 동시 실행 세마포어가 두 개가 된다(각 `insightsConcurrency`). 같은 리전이면 러너를 공유해 한 개로 유지한다.
- 남은 한계: `Config.WebACLs`는 ACL 이름만 저장하고 스코프를 잃는다. 그래서 WAF 메트릭 패널은 리전이 다를 때 선택된 ACL을 전부 WAF 리전에서 읽고, REGIONAL 스코프를 섞어 고르면 그쪽이 빈다. 고치려면 설정 스키마에 스코프를 넣어야 한다.

### 2026-08-10 헬스체크 경로를 팟 로그 집계에서 제외한다

- 맥락: 몇 초마다 도는 liveness/readiness 프로브가 요청 라인의 최대 공급원이라, 아무 일도 하지 않는 경로 쪽으로 응답 시간 백분위를 끌어내린다. 프로브가 실패하기 시작하면 동일한 행 수천 개가 비정상 응답 표를 채워 진짜 장애를 `LIMIT` 밖으로 밀어낸다.
- 채택: `LogFormat.ExcludePaths`(기본 `/health`, `/healthcheck`)를 두고 **Insights 쿼리 단계**에서 거른다. 스캔 바이트가 줄고, 차트 옆 숫자도 이미 제외된 값이 된다. 팟 로그 쿼리 5종(traffic / badStatus 집계·목록 / error 집계·목록)에 동일하게 적용해 목록과 그 옆 건수가 다른 모집단을 세는 일이 없게 했다. 각 stat의 `basis`에 제외된 경로를 명시한다.
- 기각: ⓐ 응답을 받은 뒤 Go에서 거르는 안 — 스캔 비용이 그대로다. ⓑ 접두어·부분 일치 — `/healthy-users`를 함께 삼키고, 설정 미리보기(Go)와 쿼리(Insights)에서 규칙을 두 번 구현해야 해 어긋날 여지가 생긴다. 정확 일치로 고정했다.
- 대가: `/health/live` 처럼 하위 경로를 쓰면 목록에 직접 추가해야 한다.
- 주의: 필터는 `not ispresent(path) or path not in [...]` 형태여야 한다. Insights에서 없는 필드에 대한 비교는 매칭되지 않으므로, 가드 없이 쓰면 path가 없는 평문 로그가 전부 사라져 ERROR·WARN 패널이 빈다.

### 2026-08-10 로그 집계를 로컬 SQLite에서 CloudWatch Logs Insights로 옮긴다

- 맥락: 이전 구현(`C:\Users\User\source\dashboard`)은 `FilterLogEvents`로 로그 전량을 로컬 SQLite에 적재하고 로컬 SQL로 집계했다. 백분위가 인덱스 없는 `latency_ms`에 `ORDER BY … OFFSET`을 걸었고(`store.go:141-173`), WAF 헤더 통계는 `json_each` 무인덱스 크로스 조인이었으며(`store.go:315`), `SetMaxOpenConns(1)`로 수집기와 조회가 직렬화됐다(`store.go:37`). 데이터가 쌓일수록 느려지는 것이 구조적으로 필연이었다.
- 채택: 집계를 Insights로 내린다. 로컬 누적 상태를 두지 않으므로 시간이 지나도 비용이 일정하다. 조회 상한 4시간이 스캔량을 묶는다.
- 기각: 인덱스 추가·`VACUUM`·커넥션 수 확대로 기존 구조를 고치는 안. 증상은 완화되지만 "누적될수록 느려진다"는 성질 자체는 남는다.
- 대가: Insights는 스캔 바이트당 과금이라 AWS 비용이 오른다. 자동 새로고침 기본 끄기, 최소 30초, 결과 30초 캐시로 억제하고 **각 패널이 스캔량을 표시**해 비용을 눈에 보이게 한다.

### 2026-08-10 표시값은 전부 백엔드가 계산하고 프론트는 포맷팅만 한다

- 맥락: 이전 구현은 오버뷰와 상세의 같은 숫자가 달랐다. 원인은 프론트 재집계였다. `totalReq`는 `status>0` 모집단, 백분위의 `n`은 `latency_ms>0` 모집단인데 둘 다 "요청 수"로 표시됐고(`+page.svelte:143`), `errTotal`은 `LIMIT 300`인 배열의 length였으며, `Bars.svelte`가 그리드 8행·확대 30행으로 잘라 같은 패널의 분모까지 달라졌다(`+page.svelte:195`).
- 채택: 응답 계약으로 막는다. ① 화면의 모든 숫자는 `stats`에서 온다 ② `table.total`은 `rows`와 독립 계산 ③ 각 `stat`이 `basis`로 모집단을 명시 ④ 한 페이지는 요청 하나, 모든 패널이 하나의 `window` 공유 ⑤ 단일 패널 엔드포인트와 페이지 엔드포인트가 같은 빌더를 쓰고, 두 결과가 바이트 단위로 같은지 테스트가 확인.
- 기각: 프론트에서 재집계하되 헬퍼를 공유하는 안. 공유 헬퍼는 호출부가 다른 배열을 넘기는 순간 다시 갈라진다.
- 대가: 백엔드 응답이 커지고, 새 표시값을 추가할 때마다 서버를 고쳐야 한다.

### 2026-08-10 메트릭은 ListMetrics 대신 SEARCH() 수식으로 조회한다

- 맥락: 이전 구현이 결국 데이터를 못 불러온 1순위 원인. `ListMetrics`는 삭제된 팟의 차원을 약 2주간 계속 반환하므로 생성되는 쿼리 수가 단조 증가했고, `GetMetricData`의 500쿼리 상한을 넘는 순간 `ValidationError`로 **영구 실패**했다. 상한은 주석에만 있었고 강제되지 않았다(`metrics.go:29`).
- 채택: `SEARCH('{Namespace,Dims} MetricName="x" …', 'Stat', period)` 하나로 대체한다. 매칭 시리즈가 몇 개든 쿼리는 하나다. 500 상한은 청크 분할로 코드가 강제하고 테스트가 확인한다.
- 기각: `ListMetrics`에 `RecentlyActive: PT3H`만 붙이는 안. 증가 속도는 늦추지만 상한을 넘을 가능성은 남는다. (불가피하게 `ListMetrics`를 쓰는 자리에는 이 옵션을 함께 적용.)
- 대가: `SEARCH`는 반환 시리즈 수에 상한이 있고, 라벨 형식이 CloudWatch에 달려 있다.

### 2026-08-10 자격증명은 .env에서만 읽고 어디에도 쓰지 않는다

- 맥락: 액세스 키 방식이 요구사항. 브라우저에 키 입력 필드를 두면 화면·로그·스크린샷으로 새어나갈 경로가 늘어난다.
- 채택: 시작 시 `.env`에서 읽어 메모리에만 보관. 설정 화면은 그 키가 무엇으로 확인됐는지(계정·ARN·리전)만 표시한다. 로그에는 키 끝 4자리만 남긴다.
- 기각: UI 입력 + OS 키체인 암호화 저장. 플랫폼별 분기가 늘고, 로컬 도구에는 과한 복잡도.
- 대가: 키 교체는 `.env` 편집 후 재시작.

### 2026-08-10 윈도의 끝을 간격 경계로 내림한다

- 맥락: 이전 구현은 `now`를 그대로 조회 경계로 쓰고 `(ts/width)*width`로 버킷을 나눴다(`k8s.go:262`, `store.go:94`). 첫 버킷은 임의 비율로 잘리고 마지막 버킷은 항상 미완성이었다. UI는 이를 헤드라인에서 "끝에서 두 번째 버킷"을 읽는 방식으로 우회했고, 옆 차트는 마지막 버킷을 포함해 둘이 어긋났다(`+page.svelte:136`).
- 채택: `end = now.Truncate(period)`, `start = end - range`. 구간 내 모든 버킷이 완전한 버킷이 된다.
- 기각: 미완성 버킷을 포함하되 `partial` 플래그로 표시하는 안. 표시가 하나 늘 뿐 "같은 값이 두 곳에서 다르다"는 문제는 남는다.
- 대가: 최신 데이터가 최대 한 period만큼 늦게 보인다.
