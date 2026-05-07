import { writable } from 'svelte/store';
import {
	applyPatch,
	clearChannels,
	markChannelStale,
	removeChannel,
	setChannel,
	type ChannelDTO
} from './channels';

export type ConnectionStatus = 'connecting' | 'open' | 'closed';

export const connectionStatus = writable<ConnectionStatus>('closed');
export const sessionID = writable<string | null>(null);

const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10_000;

let ws: WebSocket | null = null;
let backoff = RECONNECT_MIN_MS;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let started = false;

type WSMessage = { type: string } & Record<string, unknown>;

function wsURL(): string {
	const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
	return `${proto}//${window.location.host}/ws`;
}

export function connect() {
	if (typeof window === 'undefined') return;
	if (started) return;
	started = true;
	open();
}

function open() {
	if (ws && ws.readyState !== WebSocket.CLOSED) return;
	connectionStatus.set('connecting');
	ws = new WebSocket(wsURL());

	ws.onopen = () => {
		connectionStatus.set('open');
		backoff = RECONNECT_MIN_MS;
		send({
			type: 'client_hello',
			client_version: '0.1.0',
			screen: { w: window.innerWidth, h: window.innerHeight },
			capabilities: {}
		});
		send({ type: 'subscribe', subscription_id: 'lifecycle', topic: 'channel.lifecycle' });
		send({ type: 'subscribe', subscription_id: 'state', topic: 'channel.state.*' });
		send({ type: 'subscribe', subscription_id: 'devices', topic: 'device.status' });
	};

	ws.onmessage = (ev) => {
		try {
			const msg = JSON.parse(ev.data) as WSMessage;
			handle(msg);
		} catch (err) {
			console.error('ws: malformed json', err, ev.data);
		}
	};

	ws.onerror = (err) => {
		console.error('ws error', err);
	};

	ws.onclose = () => {
		connectionStatus.set('closed');
		ws = null;
		// Mark every known channel stale so the UI can show grey-out.
		clearStale(true);
		scheduleReconnect();
	};
}

function clearStale(stale: boolean) {
	// Iterate channels via the store update; we don't keep a separate cache here.
	// markChannelStale loops in the store update.
	// For simplicity just call channels.update with a transformer.
	import('./channels').then((mod) => {
		mod.channels.update((m) => {
			const next = new Map(m);
			for (const [id, c] of next) {
				next.set(id, { ...c, snapshot: { ...c.snapshot, stale } });
			}
			return next;
		});
	});
	void markChannelStale; // keep import live for tree-shake stability
}

function scheduleReconnect() {
	if (reconnectTimer) return;
	reconnectTimer = setTimeout(() => {
		reconnectTimer = null;
		open();
	}, backoff);
	backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
}

function handle(msg: WSMessage) {
	switch (msg.type) {
		case 'hello':
			sessionID.set((msg.session_id as string) ?? null);
			break;
		case 'subscription.confirmed': {
			const snap = msg.snapshot as { channels?: ChannelDTO[] } | undefined;
			if (snap?.channels) {
				clearChannels();
				for (const c of snap.channels) setChannel(c);
			}
			break;
		}
		case 'channel.added':
			setChannel(msg.channel as ChannelDTO);
			break;
		case 'channel.removed':
			removeChannel(msg.channel_id as number);
			break;
		case 'channel.state':
			applyPatch(
				msg.channel_id as number,
				msg.patch as Parameters<typeof applyPatch>[1]
			);
			break;
		case 'device.status':
			// Phase 1: we don't render device status separately yet.
			break;
		case 'alert.fired':
			// Phase 1: alert handling lives in Phase 5+.
			break;
		case 'pong':
			break;
		default:
			console.debug('ws unknown message', msg);
	}
}

export function send(payload: unknown) {
	if (!ws || ws.readyState !== WebSocket.OPEN) return;
	ws.send(JSON.stringify(payload));
}

export function close() {
	started = false;
	if (reconnectTimer) {
		clearTimeout(reconnectTimer);
		reconnectTimer = null;
	}
	if (ws) {
		ws.close();
		ws = null;
	}
}
