<script lang="ts">
	import type { ChannelDTO } from '$lib/stores/channels';
	import { devices, showMode, setDeviceLock } from '$lib/stores/devices';
	import { rpc, RPCError } from '$lib/api';

	let { channel }: { channel: ChannelDTO } = $props();

	const RF_FLOOR = -90;
	const RF_CEIL = -30;
	const AUDIO_FLOOR = -60;
	const AUDIO_CEIL = 0;

	let device = $derived($devices.get(channel.adapter_id));
	let deviceUnlocked = $derived(!!device?.write_enabled);
	let writesAllowed = $derived(deviceUnlocked && !$showMode);

	let editingFreq = $state(false);
	let freqDraft = $state('');
	let freqError = $state('');
	let pendingMute = $state(false);

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

	function startEditFreq() {
		if (!writesAllowed) return;
		editingFreq = true;
		freqDraft = fmtFreq(channel.snapshot.freq.mhz);
		freqError = '';
	}

	async function commitFreq() {
		const mhz = parseFloat(freqDraft);
		if (!Number.isFinite(mhz)) {
			freqError = 'invalid';
			return;
		}
		try {
			await rpc('channel.set_frequency', { channel_id: channel.id, mhz });
			editingFreq = false;
			freqError = '';
		} catch (e) {
			const err = e as RPCError;
			freqError = err.code === 'invalid_argument' ? 'out of range' : err.code;
		}
	}

	function cancelEditFreq() {
		editingFreq = false;
		freqError = '';
	}

	async function toggleMute() {
		if (!writesAllowed || pendingMute) return;
		pendingMute = true;
		try {
			await rpc('channel.set_mute', {
				channel_id: channel.id,
				muted: !channel.snapshot.audio.muted
			});
		} catch (e) {
			console.error('toggleMute', e);
		} finally {
			pendingMute = false;
		}
	}

	async function toggleLock() {
		const currentlyUnlocked = deviceUnlocked;
		if (!currentlyUnlocked) {
			const ok = window.confirm(
				`Enable writes on ${channel.adapter_id}?\n\nThis lets Frequency Bridge change settings on the device.`
			);
			if (!ok) return;
		}
		try {
			await rpc('device.set_write_enabled', {
				device_id: channel.adapter_id,
				enabled: !currentlyUnlocked,
				confirm: !currentlyUnlocked
			});
			setDeviceLock(channel.adapter_id, !currentlyUnlocked);
		} catch (e) {
			console.error('toggleLock', e);
		}
	}

	function onFreqKey(event: KeyboardEvent) {
		if (event.key === 'Enter') commitFreq();
		else if (event.key === 'Escape') cancelEditFreq();
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
			{#if editingFreq}
				<input
					type="number"
					step="0.025"
					bind:value={freqDraft}
					onblur={commitFreq}
					onkeydown={onFreqKey}
					class="w-24 rounded border border-blue-400 bg-white px-1 py-0.5 text-right font-mono text-sm focus:outline-none focus:ring focus:ring-blue-200"
				/>
				{#if freqError}
					<div class="text-xs text-red-600">{freqError}</div>
				{/if}
			{:else}
				<button
					type="button"
					onclick={startEditFreq}
					disabled={!writesAllowed}
					title={writesAllowed ? 'Click to edit' : 'Unlock device or leave Show Mode to edit'}
					class="font-mono text-sm text-gray-900 enabled:hover:bg-gray-100 enabled:cursor-text rounded px-1 disabled:cursor-not-allowed"
				>
					{fmtFreq(channel.snapshot.freq.mhz)} MHz
				</button>
			{/if}
			{#if channel.snapshot.stale}
				<div class="text-xs text-amber-600">stale</div>
			{:else if channel.snapshot.audio.muted}
				<div class="text-xs text-red-600">muted</div>
			{:else}
				<div class="text-xs text-green-600">live</div>
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

	<div class="mt-3 flex items-center justify-between gap-2 border-t border-gray-100 pt-2 text-xs text-gray-700">
		<span class="flex items-center gap-2">
			{#if channel.snapshot.tx.present}
				<span>🔋 {channel.snapshot.tx.battery_pct >= 0 ? channel.snapshot.tx.battery_pct + '%' : '—'}</span>
			{:else}
				<span class="text-gray-400">⚪ no TX</span>
			{/if}
			{#if channel.snapshot.tx.runtime_min >= 0}
				<span class="text-gray-500">{channel.snapshot.tx.runtime_min} min</span>
			{/if}
		</span>
		<div class="flex items-center gap-1.5">
			<button
				type="button"
				onclick={toggleMute}
				disabled={!writesAllowed || pendingMute}
				class="rounded border px-2 py-0.5 text-xs font-medium transition disabled:cursor-not-allowed disabled:opacity-50 {channel.snapshot.audio.muted
					? 'border-red-300 bg-red-50 text-red-700 hover:bg-red-100'
					: 'border-gray-300 bg-white text-gray-700 hover:bg-gray-50'}"
				title={writesAllowed ? 'Toggle mute' : 'Unlock device or leave Show Mode'}
			>
				{channel.snapshot.audio.muted ? 'Unmute' : 'Mute'}
			</button>
			<button
				type="button"
				onclick={toggleLock}
				class="rounded border px-1.5 py-0.5 text-xs transition hover:bg-gray-50 {deviceUnlocked
					? 'border-amber-300 text-amber-700'
					: 'border-gray-300 text-gray-500'}"
				title={deviceUnlocked ? 'Lock device (block writes)' : 'Unlock device (allow writes)'}
			>
				{deviceUnlocked ? '🔓' : '🔒'}
			</button>
		</div>
	</div>
</div>
