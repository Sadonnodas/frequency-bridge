import { send } from './stores/ws';

export class RPCError extends Error {
	code: string;
	constructor(code: string, message: string) {
		super(message);
		this.code = code;
	}
}

type Pending = {
	resolve: (value: unknown) => void;
	reject: (err: Error) => void;
	timer: ReturnType<typeof setTimeout>;
};

const pending = new Map<string, Pending>();
let nextID = 0;

export function rpc<T = unknown>(method: string, params?: unknown, timeoutMs = 10_000): Promise<T> {
	const id = `c-${++nextID}-${Date.now()}`;
	return new Promise<T>((resolve, reject) => {
		const timer = setTimeout(() => {
			if (pending.has(id)) {
				pending.delete(id);
				reject(new RPCError('timeout', `rpc ${method} timed out`));
			}
		}, timeoutMs);
		pending.set(id, {
			resolve: (v) => resolve(v as T),
			reject,
			timer
		});
		send({ type: 'rpc', id, method, params: params ?? {} });
	});
}

export function deliverRPCResult(msg: { id?: string; ok?: boolean; result?: unknown; error?: { code?: string; message?: string } }) {
	if (!msg.id) return;
	const p = pending.get(msg.id);
	if (!p) return;
	pending.delete(msg.id);
	clearTimeout(p.timer);
	if (msg.ok) {
		p.resolve(msg.result);
	} else {
		const code = msg.error?.code ?? 'internal';
		const message = msg.error?.message ?? 'rpc failed';
		p.reject(new RPCError(code, message));
	}
}

// failPendingOnDisconnect rejects every pending RPC when the WS closes, so
// callers don't see hung promises. They'll get a 'disconnected' error code
// and can retry once the WS reconnects.
export function failPendingOnDisconnect() {
	for (const [id, p] of pending) {
		clearTimeout(p.timer);
		p.reject(new RPCError('disconnected', 'websocket closed before reply'));
		pending.delete(id);
	}
}
