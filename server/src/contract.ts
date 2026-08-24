// 프론트엔드가 선언한 와이어 계약을 그대로 쓴다.
//
// web/src/lib/types.ts 는 런타임 export 가 하나도 없는 순수 타입 파일이고,
// internal/domain/series.go 를 미러링한다. 서버가 그 타입으로 구현하면 계약
// 위반이 컴파일 단계에서 잡힌다 — 두 트랙이 같은 프론트를 먹여야 하므로,
// 이식에서 가장 값싼 안전장치다.
export type * from '../../web/src/lib/types.ts';
