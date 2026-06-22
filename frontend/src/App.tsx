import { useEffect, useMemo, useRef, useState } from 'react';
import { createSignalingSocket, type SignalMessage } from './signaling';

type ConnectionState = 'connecting' | 'open' | 'closed' | 'error';

interface LogLine {
  at: string;
  direction: 'in' | 'out' | 'system';
  text: string;
}

export function App() {
  const room = useMemo(() => new URLSearchParams(window.location.search).get('room') ?? 'default', []);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const startedRef = useRef(false);
  const [state, setState] = useState<ConnectionState>('connecting');
  const [clientId, setClientId] = useState<string>('');
  const [pcState, setPcState] = useState<RTCPeerConnectionState>('new');
  const [iceState, setIceState] = useState<RTCIceConnectionState>('new');
  const [videoStats, setVideoStats] = useState('waiting');
  const [clock, setClock] = useState(() => new Date());
  const [logs, setLogs] = useState<LogLine[]>([]);

  useEffect(() => {
    const log = (direction: LogLine['direction'], text: string) => {
      setLogs((current) => [
        { at: new Date().toLocaleTimeString(), direction, text },
        ...current,
      ].slice(0, 40));
    };

    const sendSignal = (message: SignalMessage) => {
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        return;
      }
      ws.send(JSON.stringify(message));
      if (message.type !== 'candidate') {
        log('out', JSON.stringify(message));
      }
    };

    const startPeer = async () => {
      if (startedRef.current) {
        return;
      }
      startedRef.current = true;

      const pc = new RTCPeerConnection({
        iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
      });
      pcRef.current = pc;

      // The browser is receive-only. Screen capture, cursor exclusion, encode,
      // and bitrate control all live on the Go side.
      pc.addTransceiver('video', { direction: 'recvonly' });

      pc.ontrack = (event) => {
        const [stream] = event.streams;
        if (videoRef.current && stream) {
          videoRef.current.srcObject = stream;
          videoRef.current.play().catch(() => undefined);
          log('system', `remote ${event.track.kind} track attached`);
        }
      };

      pc.onicecandidate = (event) => {
        if (event.candidate) {
          sendSignal({ type: 'candidate', payload: event.candidate.toJSON() });
        }
      };

      pc.onconnectionstatechange = () => setPcState(pc.connectionState);
      pc.oniceconnectionstatechange = () => setIceState(pc.iceConnectionState);

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      if (pc.localDescription) {
        sendSignal({ type: 'offer', payload: pc.localDescription.toJSON() });
      }
    };

    let pingTimer: number | undefined;
    let statsTimer: number | undefined;
    let clockTimer: number | undefined;
    const ws = createSignalingSocket(room);
    wsRef.current = ws;

    ws.onopen = () => {
      setState('open');
      log('system', 'WebSocket connected');
      sendSignal({ type: 'hello', payload: { userAgent: navigator.userAgent } });

      pingTimer = window.setInterval(() => {
        sendSignal({ type: 'ping' });
      }, 5000);

      statsTimer = window.setInterval(() => {
        const video = videoRef.current;
        const quality = video?.getVideoPlaybackQuality?.();
        if (!video || !quality) {
          return;
        }
        setVideoStats(
          `time=${video.currentTime.toFixed(2)}s decoded=${quality.totalVideoFrames} dropped=${quality.droppedVideoFrames}`,
        );
      }, 1000);
      clockTimer = window.setInterval(() => {
        setClock(new Date());
      }, 100);
    };

    ws.onmessage = async (event) => {
      const message = JSON.parse(event.data) as SignalMessage<{ clientId?: string }>;
      if (message.type !== 'candidate') {
        log('in', event.data);
      }

      if (message.type === 'welcome' && message.payload?.clientId) {
        setClientId(message.payload.clientId);
        try {
          await startPeer();
        } catch (error) {
          log('system', `start peer failed: ${String(error)}`);
          setState('error');
        }
        return;
      }

      const pc = pcRef.current;
      if (!pc) {
        return;
      }

      if (message.type === 'answer') {
        await pc.setRemoteDescription(message.payload as RTCSessionDescriptionInit);
      } else if (message.type === 'candidate' && message.payload) {
        await pc.addIceCandidate(message.payload as RTCIceCandidateInit);
      } else if (message.type === 'error') {
        setState('error');
      }
    };

    ws.onerror = () => {
      setState('error');
      log('system', 'WebSocket error');
    };

    ws.onclose = () => {
      setState('closed');
      log('system', 'WebSocket closed');
    };

    return () => {
      if (pingTimer !== undefined) {
        window.clearInterval(pingTimer);
      }
      if (statsTimer !== undefined) {
        window.clearInterval(statsTimer);
      }
      if (clockTimer !== undefined) {
        window.clearInterval(clockTimer);
      }
      pcRef.current?.close();
      pcRef.current = null;
      ws.close();
      wsRef.current = null;
      startedRef.current = false;
      if (videoRef.current) {
        videoRef.current.srcObject = null;
      }
    };
  }, [room]);

  return (
    <main className="shell">
      <section className="toolbar" aria-label="Connection status">
        <div>
          <h1>Screen Server</h1>
          <p>Room: {room}</p>
        </div>
        <div className={`status status-${state}`}>
          <span />
          {state}
        </div>
      </section>

      <section className="viewer">
        <video ref={videoRef} autoPlay playsInline muted controls={false} />
        <div className="videoHud" data-testid="video-clock">
          {clock.toLocaleTimeString()}.{clock.getMilliseconds().toString().padStart(3, '0')}
        </div>
      </section>

      <section className="panel">
        <dl className="meta">
          <div>
            <dt>Client ID</dt>
            <dd>{clientId || 'pending'}</dd>
          </div>
          <div>
            <dt>Peer</dt>
            <dd>{pcState}</dd>
          </div>
          <div>
            <dt>ICE</dt>
            <dd>{iceState}</dd>
          </div>
          <div>
            <dt>Video</dt>
            <dd>{videoStats}</dd>
          </div>
          <div>
            <dt>Signaling</dt>
            <dd>/api/signaling/ws</dd>
          </div>
        </dl>
      </section>

      <section className="panel logPanel">
        <h2>Signal Log</h2>
        <div className="logList">
          {logs.length === 0 ? (
            <p className="empty">Waiting for signaling events.</p>
          ) : (
            logs.map((line, index) => (
              <div className={`logLine ${line.direction}`} key={`${line.at}-${index}`}>
                <time>{line.at}</time>
                <code>{line.text}</code>
              </div>
            ))
          )}
        </div>
      </section>
    </main>
  );
}
