# web

대시보드의 SvelteKit 프론트엔드입니다. 프로젝트 전체 설명은 저장소 루트의 [README](../README.md)를 보세요.

## 이 디렉터리에서 알아 둘 것

- **SSR 없음.** `src/routes/+layout.ts`가 `ssr = false`, `prerender = false`를 설정합니다. 프로덕션에는 Node 런타임이 없고 모든 데이터가 런타임에 `/api`에서 오므로 서버에서 그릴 것이 없습니다.
- **빌드 산출물은 `../internal/web/dist`로 나갑니다.** `vite.config.ts`의 `adapter-static` 설정을 보세요. Go 바이너리가 그 디렉터리를 그대로 임베드하므로 복사 단계가 없습니다.
- **프론트는 계산하지 않습니다.** 화면에 뜨는 숫자는 전부 백엔드 `stats`에서 오고, 여기서는 포맷팅만 합니다. 단위 변환도 백엔드에서 끝납니다. 이유는 루트 README의 "설계상의 세 가지 결정"에 있습니다.
- **값은 잘리지 않습니다.** `src/lib/styles/app.css`의 규칙과 `e2e/layout.e2e.ts`의 측정이 함께 강제합니다. 새 값을 렌더할 때는 `data-value`를 붙이세요 — 그 표시가 곧 검사 대상입니다.

## 명령

루트에서 `mise`로 실행하는 편이 낫습니다.

```bash
mise run web:dev        # :5173, /api 를 :8080 으로 프록시
mise run web:build      # ../internal/web/dist 로 빌드
mise run web:check      # svelte-check
mise run test           # vitest (루트의 go test 와 함께)
mise run test:e2e       # playwright 레이아웃 검증
```

## 구조

```
src/lib/api.ts              타입 있는 fetch 래퍼
src/lib/types.ts            internal/domain/series.go 의 미러
src/lib/format.ts           포맷팅 전용 (계산 없음)
src/lib/timerange.svelte.ts range/period 공유 상태, 4시간 상한
src/lib/styles/             Apple HIG oklch 토큰, 잘림 금지 규칙
src/lib/components/         UPlotChart(시계열) · BarBreakdown(집계) · CopyValue · DataTable …
src/routes/                 개요 · 팟 로그 · WAF · 타겟 그룹 · 팟·노드 · RDS Proxy · 설정
e2e/                        Playwright 레이아웃 검증과 픽스처
```
