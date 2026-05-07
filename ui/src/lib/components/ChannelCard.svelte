<script lang="ts">
	import type { ChannelDTO } from '$lib/stores/channels';

	let { channel }: { channel: ChannelDTO } = $props();

	const RF_FLOOR = -90;
	const RF_CEIL = -30;
	const AUDIO_FLOOR = -60;
	const AUDIO_CEIL = 0;

	function pct(v: number, lo: number, hi: number): number {
		if (!Number.isFinite(v)) return 0;
		if (v <= lo) return 0;
		if (v >= hi) return 100;
		return ((v - lo) / (hi - lo)) * 100;
	}

	function fmt(v: number, digits = 1, fallback = '—'): string {
		if (!Number.isFinite(v) || v <= -900) return fallback;
		return v.toFixed(digits);
	}

	function fmtFreq(mhz: number): string {
		if (!Number.isFinite(mhz) || mhz <= 0) return '—';
		return mhz.toFixed(3);
	}
</script>

<div
	class="rounded-lg border border-gray-200 bg-white p-4 shadow-sm transition-opacity"
	class:opacity-50={channel.snapshot.stale}
>
	<div class="flex items-start justify-between gap-2">
		<div>
			<h2 class="text-sm font-semibold text-gray-900">{channel.name}</h2>
			<p class="text-xs text-gray-500">
				{channel.vendor} · {channel.ref}
			</p>
		</div>
		<div class="text-right">
			<div class="font-mono text-sm text-gray-900">{fmtFreq(channel.snapshot.freq.mhz)} MHz</div>
			{#if channel.snapshot.stale}
				<span class="text-xs text-amber-600">stale</span>
			{:else if channel.snapshot.audio.muted}
				<span class="text-xs text-red-600">muted</span>
			{:else}
				<span class="text-xs text-green-600">live</span>
			{/if}
		</div>
	</div>

	<div class="mt-3 space-y-2">
		<div>
			<div class="flex justify-between text-xs text-gray-600">
				<span>RF A</span>
				<span class="font-mono">{fmt(channel.snapshot.rf.a_dbm)} dBm</span>
			</div>
			<div class="mt-0.5 h-1.5 overflow-hidden rounded bg-gray-200">
				<div
					class="h-full bg-blue-500 transition-[width] duration-75"
					style:width="{pct(channel.snapshot.rf.a_dbm, RF_FLOOR, RF_CEIL)}%"
				></div>
			</div>
		</div>

		<div>
			<div class="flex justify-between text-xs text-gray-600">
				<span>RF B</span>
				<span class="font-mono">{fmt(channel.snapshot.rf.b_dbm)} dBm</span>
			</div>
			<div class="mt-0.5 h-1.5 overflow-hidden rounded bg-gray-200">
				<div
					class="h-full bg-blue-500 transition-[width] duration-75"
					style:width="{pct(channel.snapshot.rf.b_dbm, RF_FLOOR, RF_CEIL)}%"
				></div>
			</div>
		</div>

		<div>
			<div class="flex justify-between text-xs text-gray-600">
				<span>Audio</span>
				<span class="font-mono">{fmt(channel.snapshot.audio.level_dbfs)} dBFS</span>
			</div>
			<div class="mt-0.5 h-1.5 overflow-hidden rounded bg-gray-200">
				<div
					class="h-full bg-emerald-500 transition-[width] duration-75"
					style:width="{pct(channel.snapshot.audio.level_dbfs, AUDIO_FLOOR, AUDIO_CEIL)}%"
				></div>
			</div>
		</div>
	</div>

	<div class="mt-3 flex items-center justify-between border-t border-gray-100 pt-2 text-xs text-gray-700">
		<span>
			{#if channel.snapshot.tx.present}
				🔋 {channel.snapshot.tx.battery_pct >= 0 ? channel.snapshot.tx.battery_pct + '%' : '—'}
			{:else}
				⚪ no TX
			{/if}
		</span>
		<span class="text-gray-500">
			{#if channel.snapshot.tx.runtime_min >= 0}
				{channel.snapshot.tx.runtime_min} min
			{/if}
		</span>
		{#if channel.snapshot.freq.encrypted}
			<span class="text-purple-600">🔒</span>
		{/if}
	</div>
</div>
