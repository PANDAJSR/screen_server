export type SignalType =
  | 'hello'
  | 'welcome'
  | 'peer-joined'
  | 'peer-left'
  | 'ping'
  | 'pong'
  | 'offer'
  | 'answer'
  | 'candidate'
  | 'error'
  | 'input-mode'
  | 'input-mousemove'
  | 'input-mousemove-abs'
  | 'input-mousebtn'
  | 'input-scroll'
  | 'input-keydown'
  | 'input-keyup'
  | 'cursor-pos'
  | 'cursor-image'
  | 'screen-size';

export interface SignalMessage<TPayload = unknown> {
  type: SignalType;
  room?: string;
  from?: string;
  to?: string;
  payload?: TPayload;
}

export function createSignalingSocket(room: string): WebSocket {
  const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = new URL('/api/signaling/ws', `${wsProtocol}//${window.location.host}`);
  url.searchParams.set('room', room);
  return new WebSocket(url);
}
