<script lang="ts">
	import { onMount } from 'svelte';
	import { connect, connectionStatus } from '$lib/stores/ws';
	import { channels } from '$lib/stores/channels';
	import ChannelCard from '$lib/components/ChannelCard.svelte';

	onMount(() => {
		connect();
	});

	let sortedChannels = $derived([...$channels.values()].sort((a, b) => a.id - b.id));

	const statusColor: Record<string, string> = {
		open: 'bg-green-500',
		connecting: 'bg-amber-500',
		closed: 'bg-red-500'
	};
</script>

<div class="mx-auto max-w-5xl p-6">
	<header class="mb-6 flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-semibold text-gray-900">Frequency Bridge</h1>
			<p class="text-sm text-gray-500">Phase 1 — mock adapter end-to-end</p>
		</div>
		<div class="flex items-center gap-2 text-sm text-gray-600">
			<span class="inline-block h-2.5 w-2.5 rounded-full {statusColor[$connectionStatus]}"></span>
			{$connectionStatus}
		</div>
	</header>

	{#if sortedChannels.length === 0}
		<div class="rounded-lg border border-dashed border-gray-300 p-10 text-center text-sm text-gray-500">
			Waiting for channels…
		</div>
	{:else}
		<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
			{#each sortedChannels as channel (channel.id)}
				<ChannelCard {channel} />
			{/each}
		</div>
	{/if}
</div>
