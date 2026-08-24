// CloudWatch 가 쓰는 철자로 된 리소스 식별자.
//
// AWS 호출 옆이 아니라 domain 에 있는 이유는, 저장된 설정이 담아야 하는 값이기도
// 하기 때문이다. 설정 계층은 운영자가 붙여넣은 것을 discovery 가 내놓는 것과
// 같은 형태로 정규화하고, awsx 는 이미 config 를 임포트하므로 변환이 그쪽에
// 살 수 없다.

/**
 * TargetGroupDimension 은 타겟 그룹 ARN 을 CloudWatch 의 TargetGroup 차원 값,
 * 즉 ARN 의 꼬리 경로로 바꾼다.
 *
 *   arn:aws:elasticloadbalancing:ap-northeast-2:1:targetgroup/k8s-default-app/abc
 *                                                 ^--------- 이 부분 ---------^
 */
export function targetGroupDimension(arn: string): string {
	const i = arn.indexOf(':targetgroup/');
	return i >= 0 ? arn.slice(i + 1) : arn;
}

/**
 * LoadBalancerDimension 은 로드밸런서 ARN 을 CloudWatch 의 LoadBalancer 차원
 * 값으로 바꾼다.
 *
 *   arn:...:loadbalancer/app/my-alb/abc  ->  app/my-alb/abc
 */
export function loadBalancerDimension(arn: string): string {
	const marker = ':loadbalancer/';
	const i = arn.indexOf(marker);
	return i >= 0 ? arn.slice(i + marker.length) : arn;
}

/**
 * FriendlyTargetGroupName 은 쿠버네티스가 만든 타겟 그룹 이름에서 생성 접미사를
 * 떼어 낸다. UI 가 그 앞의 서비스 이름으로 라벨을 달 수 있게 하려는 것이다.
 *
 *   k8s-default-product-d6d507c878  ->  product
 */
export function friendlyTargetGroupName(name: string): string {
	const parts = name.split('-');
	if (parts.length < 4 || parts[0] !== 'k8s') return name;
	return parts.slice(2, parts.length - 1).join('-');
}
