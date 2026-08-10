<script lang="ts">
	import PageView from '$lib/components/PageView.svelte';
</script>

<PageView
	pageId="kubernetes"
	title="팟 · 노드"
	description="Container Insights 지표입니다. 팟과 노드의 CPU·메모리 사용률, 개수(최소·현재·최대), 팟 상태를 봅니다. 노드 최소·최대는 EKS 노드그룹 설정에서, 팟 최소·최대는 구간 내 관측값에서 옵니다."
/>
