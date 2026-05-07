import { writable } from 'svelte/store';

export type RFState = {
	a_dbm: number;
	b_dbm: number;
	quality: number;
	dominant_source: string;
	packet_errors: number;
};

export type AudioState = {
	level_dbfs: number;
	peak_dbfs: number;
	peak_held_at: string;
	muted: boolean;
};

export type TXState = {
	present: boolean;
	model: string;
	battery_pct: number;
	runtime_min: number;
	battery_type: string;
	name_on_pack: string;
};

export type FreqState = {
	mhz: number;
	group: string;
	slot_index: number;
	encrypted: boolean;
};

export type LinkState = {
	connected: boolean;
	last_packet_time: string;
	error_rate: number;
};

export type ChannelSnapshot = {
	id: number;
	adapter_id: string;
	rf: RFState;
	audio: AudioState;
	tx: TXState;
	freq: FreqState;
	link: LinkState;
	stale: boolean;
	last_update: string;
};

export type Capabilities = {
	supports_mute: boolean;
	supports_encryption: boolean;
	supports_gain_adjust: boolean;
	supports_freq_adjust: boolean;
	supports_scan: boolean;
	gain_range_db: [number, number];
	freq_range_mhz: [number, number];
	vendor_ops?: string[];
};

export type ChannelDTO = {
	id: number;
	adapter_id: string;
	vendor: string;
	model: string;
	ref: string;
	name: string;
	natural_unit: string;
	direction: string;
	group?: string;
	slot_index?: number;
	capabilities: Capabilities;
	snapshot: ChannelSnapshot;
};

export type ChannelMap = Map<number, ChannelDTO>;

export const channels = writable<ChannelMap>(new Map());

export function setChannel(c: ChannelDTO) {
	channels.update((m) => {
		const next = new Map(m);
		next.set(c.id, c);
		return next;
	});
}

export function removeChannel(id: number) {
	channels.update((m) => {
		const next = new Map(m);
		next.delete(id);
		return next;
	});
}

type StatePatch = {
	ref: string;
	rf?: Partial<RFState>;
	audio?: Partial<AudioState>;
	tx?: Partial<TXState>;
	freq?: Partial<FreqState>;
	link?: Partial<LinkState>;
	vendor?: unknown;
};

export function applyPatch(id: number, patch: StatePatch) {
	channels.update((m) => {
		const c = m.get(id);
		if (!c) return m;
		const next = new Map(m);
		const snap = { ...c.snapshot };
		if (patch.rf) snap.rf = { ...snap.rf, ...patch.rf };
		if (patch.audio) snap.audio = { ...snap.audio, ...patch.audio };
		if (patch.tx) snap.tx = { ...snap.tx, ...patch.tx };
		if (patch.freq) snap.freq = { ...snap.freq, ...patch.freq };
		if (patch.link) snap.link = { ...snap.link, ...patch.link };
		next.set(id, { ...c, snapshot: snap });
		return next;
	});
}

export function markChannelStale(id: number, stale: boolean) {
	channels.update((m) => {
		const c = m.get(id);
		if (!c) return m;
		const next = new Map(m);
		next.set(id, { ...c, snapshot: { ...c.snapshot, stale } });
		return next;
	});
}

export function clearChannels() {
	channels.set(new Map());
}
