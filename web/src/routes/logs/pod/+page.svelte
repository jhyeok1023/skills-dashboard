<script lang="ts">
	import PageView from '$lib/components/PageView.svelte';
</script>

<PageView
	pageId="pod-logs"
	namespaceFilter
	title="팟 로그"
	description="CloudWatch Logs Insights에서 집계합니다. 응답 시간 백분위, 200·201이 아닌 응답, ERROR·WARN 라인을 봅니다. 목록은 상한이 있지만 건수는 별도 집계이므로 잘리지 않습니다."
/>
