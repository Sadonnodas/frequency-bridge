<script lang="ts">
	import { rpc } from '$lib/api';
	import { showMode, setShowMode } from '$lib/stores/devices';

	let pending = $state(false);

	async function toggle() {
		if (pending) return;
		pending = true;
		try {
			if ($showMode) {
				const ok = window.confirm(
					'Leave Show Mode?\n\nThis re-enables writes on every device whose per-device lock is open.'
				);
				if (!ok) {
					return;
				}
				await rpc('app.set_mode', { mode: 'normal', confirm: true });
				setShowMode(false);
			} else {
				await rpc('app.set_mode', { mode: 'show_mode' });
				setShowMode(true);
			}
		} catch (e) {
			console.error('toggle show mode', e);
		} finally {
			pending = false;
		}
	}
</script>

<button
	type="button"
	onclick={toggle}
	disabled={pending}
	class="rounded px-2.5 py-1 text-xs font-semibold transition {$showMode
		? 'bg-red-600 text-white hover:bg-red-700'
		: 'border border-gray-300 bg-white text-gray-700 hover:bg-gray-50'}"
	title={$showMode
		? 'Show Mode: all writes blocked. Click to leave (confirm required).'
		: 'Click to enter Show Mode (block all writes globally).'}
>
	{$showMode ? '🔒 SHOW MODE' : 'Show Mode'}
</button>
