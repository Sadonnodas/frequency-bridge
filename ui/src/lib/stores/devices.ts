import { writable } from 'svelte/store';

export type DeviceStatus = {
	connected: boolean;
	since: string;
	last_error?: string;
	reconnect_count: number;
};

export type DeviceState = {
	id: string;
	connected: boolean;
	last_error: string;
	reconnect_count: number;
	write_enabled: boolean;
};

export type DeviceMap = Map<string, DeviceState>;

export const devices = writable<DeviceMap>(new Map());
export const showMode = writable<boolean>(false);

export type DeviceListPayload = {
	statuses?: Record<string, DeviceStatus>;
	write_enabled?: Record<string, boolean>;
	show_mode?: boolean;
};

export function loadFromDeviceList(payload: DeviceListPayload) {
	const statuses = payload.statuses ?? {};
	const writeEnabled = payload.write_enabled ?? {};
	const ids = new Set([...Object.keys(statuses), ...Object.keys(writeEnabled)]);

	devices.update((prev) => {
		const next: DeviceMap = new Map();
		for (const id of ids) {
			const s = statuses[id];
			next.set(id, {
				id,
				connected: s?.connected ?? false,
				last_error: s?.last_error ?? '',
				reconnect_count: s?.reconnect_count ?? 0,
				write_enabled: !!writeEnabled[id]
			});
		}
		// Keep entries we previously knew about that the snapshot omits — the
		// channel snapshot may already reference them and we don't want them
		// to flicker out.
		for (const [id, d] of prev) {
			if (!next.has(id)) next.set(id, d);
		}
		return next;
	});

	if (typeof payload.show_mode === 'boolean') {
		showMode.set(payload.show_mode);
	}
}

export function setDeviceLock(id: string, enabled: boolean) {
	devices.update((m) => {
		const next = new Map(m);
		const d = next.get(id);
		if (d) {
			next.set(id, { ...d, write_enabled: enabled });
		} else {
			next.set(id, {
				id,
				connected: false,
				last_error: '',
				reconnect_count: 0,
				write_enabled: enabled
			});
		}
		return next;
	});
}

export function applyDeviceStatus(deviceID: string, status: DeviceStatus) {
	devices.update((m) => {
		const next = new Map(m);
		const existing = next.get(deviceID);
		next.set(deviceID, {
			id: deviceID,
			connected: status.connected,
			last_error: status.last_error ?? '',
			reconnect_count: status.reconnect_count,
			write_enabled: existing?.write_enabled ?? false
		});
		return next;
	});
}

export function setShowMode(on: boolean) {
	showMode.set(on);
}
