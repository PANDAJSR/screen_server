import { useEffect, useMemo, useRef, useState } from 'react';
import { createSignalingSocket, type SignalMessage } from './signaling';

type ConnectionState = 'connecting' | 'open' | 'closed' | 'error';

interface RemoteCursorProps {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  cursorPos: { x: number; y: number };
  screenSize: { width: number; height: number };
}

function RemoteCursor({ videoRef, cursorPos, screenSize }: RemoteCursorProps) {
  const [position, setPosition] = useState({ x: 0, y: 0 });

  useEffect(() => {
    const updatePosition = () => {
      const video = videoRef.current;
      if (!video) return;

      const containerRect = video.getBoundingClientRect();
      const videoWidth = video.videoWidth || screenSize.width;
      const videoHeight = video.videoHeight || screenSize.height;

      const containerAspect = containerRect.width / containerRect.height;
      const videoAspect = videoWidth / videoHeight;

      let contentLeft = containerRect.left;
      let contentTop = containerRect.top;
      let contentWidth = containerRect.width;
      let contentHeight = containerRect.height;

      if (containerAspect > videoAspect) {
        contentWidth = containerRect.height * videoAspect;
        contentLeft = containerRect.left + (containerRect.width - contentWidth) / 2;
      } else {
        contentHeight = containerRect.width / videoAspect;
        contentTop = containerRect.top + (containerRect.height - contentHeight) / 2;
      }

      const percentX = cursorPos.x / screenSize.width;
      const percentY = cursorPos.y / screenSize.height;

      const cursorX = contentLeft + percentX * contentWidth;
      const cursorY = contentTop + percentY * contentHeight;

      setPosition({ x: cursorX, y: cursorY });
    };

    updatePosition();

    const interval = setInterval(updatePosition, 1000 / 30);
    const resizeObserver = new ResizeObserver(updatePosition);
    if (videoRef.current) {
      resizeObserver.observe(videoRef.current);
    }
    return () => {
      clearInterval(interval);
      resizeObserver.disconnect();
    };
  }, [videoRef, cursorPos, screenSize]);

  return (
    <div
      className="remoteCursor"
      style={{
        position: 'fixed',
        left: position.x,
        top: position.y,
        pointerEvents: 'none',
        zIndex: 9999,
      }}
    />
  );
}

function StatusIcon({ state }: { state: ConnectionState }) {
  return (
    <svg className={`statusIcon status-${state}`} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <circle cx="12" cy="12" r="10" />
      <circle className="statusDot" cx="12" cy="12" r="4" fill="currentColor" stroke="none" />
    </svg>
  );
}

function RefreshIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <path d="M23 4v6h-6M1 20v-6h6" />
      <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15" />
    </svg>
  );
}

function InputIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
      <rect x="2" y="6" width="20" height="12" rx="2" />
      <path d="M6 10h.01M6 14h.01M10 10h.01M10 14h.01M14 10h.01M14 14h.01M18 10h.01M18 14h.01" strokeLinecap="round" />
      <rect x="9" y="3" width="6" height="4" rx="1" />
      <path d="M7 21h10" strokeLinecap="round" />
    </svg>
  );
}

function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggleRow">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggleTrack">
        <span className="toggleThumb" />
      </span>
      <span className="toggleLabel">{label}</span>
    </label>
  );
}

export function App() {
  const room = useMemo(() => new URLSearchParams(window.location.search).get('room') ?? 'default', []);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const startedRef = useRef(false);
  const inputEnabledRef = useRef(false);
  const inputLockedRef = useRef(false);
  const lastMouseRef = useRef({ x: 0, y: 0 });
  const [connectionKey, setConnectionKey] = useState(0);
  const [state, setState] = useState<ConnectionState>('connecting');
  const [clientId, setClientId] = useState<string>('');
  const [pcState, setPcState] = useState<RTCPeerConnectionState>('new');
  const [iceState, setIceState] = useState<RTCIceConnectionState>('new');
  const [videoStats, setVideoStats] = useState('waiting');
  const [clock, setClock] = useState(() => new Date());
  const [inputEnabled, setInputEnabled] = useState(false);
  const [inputLocked, setInputLocked] = useState(false);
  const [cursorPos, setCursorPos] = useState({ x: 0, y: 0 });
  const [screenSize, setScreenSize] = useState({ width: 1920, height: 1080 });
  const [statusOpen, setStatusOpen] = useState(false);
  const [inputMenuOpen, setInputMenuOpen] = useState(false);
  const [mouseEnabled, setMouseEnabled] = useState(true);
  const [keyboardEnabled, setKeyboardEnabled] = useState(true);
  const [roundTripMs, setRoundTripMs] = useState<number | null>(null);
  const [fps, setFps] = useState<number | null>(null);

  const statusButtonRef = useRef<HTMLButtonElement | null>(null);
  const inputButtonRef = useRef<HTMLButtonElement | null>(null);
  const statusPopoverRef = useRef<HTMLDivElement | null>(null);
  const inputPopoverRef = useRef<HTMLDivElement | null>(null);

  const prevFramesRef = useRef<number | null>(null);
  const prevTimeRef = useRef<number | null>(null);

  const setInputEnabledSync = (enabled: boolean) => {
    inputEnabledRef.current = enabled;
    setInputEnabled(enabled);
    if (!enabled && document.pointerLockElement) {
      document.exitPointerLock();
    }
  };

  useEffect(() => {
    const sendSignal = (message: SignalMessage) => {
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        return;
      }
      ws.send(JSON.stringify(message));
    };

    const sendInput = (type: SignalMessage['type'], payload: unknown) => {
      if (!inputEnabledRef.current) return;
      if (type === 'input-mousemove' || type === 'input-mousebtn' || type === 'input-scroll') {
        if (!mouseEnabled) return;
      }
      if (type === 'input-keydown' || type === 'input-keyup') {
        if (!keyboardEnabled) return;
      }
      sendSignal({ type, payload });
    };

    const handleMouseMove = (e: MouseEvent) => {
      if (!mouseEnabled) return;
      if (!inputEnabledRef.current) return;
      if (document.pointerLockElement !== videoRef.current) return;
      const dx = e.movementX;
      const dy = e.movementY;
      lastMouseRef.current = { x: lastMouseRef.current.x + dx, y: lastMouseRef.current.y + dy };
      sendInput('input-mousemove', { x: dx, y: dy });
    };

    const handleMouseDown = (e: MouseEvent) => {
      if (!mouseEnabled) return;
      if (!inputEnabledRef.current) return;
      const btn = e.button;
      let button = 1;
      if (btn === 0) button = 1;
      else if (btn === 2) button = 2;
      else if (btn === 1) button = 4;
      sendInput('input-mousebtn', { button, pressed: true, x: e.clientX, y: e.clientY });
    };

    const handleMouseUp = (e: MouseEvent) => {
      if (!mouseEnabled) return;
      if (!inputEnabledRef.current) return;
      const btn = e.button;
      let button = 1;
      if (btn === 0) button = 1;
      else if (btn === 2) button = 2;
      else if (btn === 1) button = 4;
      sendInput('input-mousebtn', { button, pressed: false, x: e.clientX, y: e.clientY });
    };

    const handleWheel = (e: WheelEvent) => {
      if (!mouseEnabled) return;
      if (!inputEnabledRef.current) return;
      e.preventDefault();
      sendInput('input-scroll', { dx: e.deltaX, dy: e.deltaY });
    };

    const handleKeyDown = (e: KeyboardEvent) => {
      if (!keyboardEnabled) return;
      if (!inputEnabledRef.current) return;
      if (e.repeat) return;

      if (
        e.keyCode === 32 ||
        e.keyCode === 33 ||
        e.keyCode === 34 ||
        e.keyCode === 35 ||
        e.keyCode === 36 ||
        e.keyCode === 37 ||
        e.keyCode === 38 ||
        e.keyCode === 39 ||
        e.keyCode === 40 ||
        e.keyCode === 9
      ) {
        e.preventDefault();
      }

      sendInput('input-keydown', { keyCode: e.keyCode });
    };

    const handleKeyUp = (e: KeyboardEvent) => {
      if (!keyboardEnabled) return;
      if (!inputEnabledRef.current) return;
      sendInput('input-keyup', { keyCode: e.keyCode });
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

      pc.addTransceiver('video', { direction: 'recvonly' });

      pc.ontrack = (event) => {
        const [stream] = event.streams;
        if (videoRef.current && stream) {
          videoRef.current.srcObject = stream;
          videoRef.current.play().catch(() => undefined);
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
      sendSignal({ type: 'hello', payload: { userAgent: navigator.userAgent } });

      pingTimer = window.setInterval(() => {
        sendSignal({ type: 'ping', payload: { ts: Date.now() } });
      }, 5000);

      statsTimer = window.setInterval(() => {
        const video = videoRef.current;
        const quality = video?.getVideoPlaybackQuality?.();
        if (!video || !quality) {
          return;
        }
        const currentTime = video.currentTime;
        const totalFrames = quality.totalVideoFrames;
        const droppedFrames = quality.droppedVideoFrames;
        setVideoStats(
          `time=${currentTime.toFixed(2)}s decoded=${totalFrames} dropped=${droppedFrames}`,
        );
        if (prevTimeRef.current !== null && prevFramesRef.current !== null) {
          const dt = currentTime - prevTimeRef.current;
          const df = totalFrames - prevFramesRef.current;
          if (dt > 0) {
            setFps(df / dt);
          }
        }
        prevTimeRef.current = currentTime;
        prevFramesRef.current = totalFrames;
      }, 1000);
      clockTimer = window.setInterval(() => {
        setClock(new Date());
      }, 100);
    };

    ws.onmessage = async (event) => {
      const message = JSON.parse(event.data) as SignalMessage<{ clientId?: string; ts?: number }>;

      if (message.type === 'pong' && message.payload && typeof message.payload === 'object' && 'ts' in message.payload) {
        const sent = (message.payload as { ts: number }).ts;
        setRoundTripMs(Date.now() - sent);
        return;
      }

      if (message.type === 'welcome' && message.payload?.clientId) {
        setClientId(message.payload.clientId);
        try {
          await startPeer();
        } catch (error) {
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
      } else if (message.type === 'cursor-pos' && message.payload) {
        const pos = message.payload as { x: number; y: number };
        setCursorPos({ x: pos.x, y: pos.y });
      } else if (message.type === 'screen-size' && message.payload) {
        const size = message.payload as { width: number; height: number };
        setScreenSize({ width: size.width, height: size.height });
      }
    };

    ws.onerror = () => {
      setState('error');
    };

    ws.onclose = () => {
      setState('closed');
    };

    const handlePointerLockChange = () => {
      const locked = document.pointerLockElement === videoRef.current;
      inputLockedRef.current = locked;
      setInputLocked(locked);
    };

    const handleDocumentClick = (e: MouseEvent) => {
      if (!inputEnabledRef.current) return;
      if (!mouseEnabled) return;
      const video = videoRef.current;
      if (!video) return;
      if (e.target === video || video.contains(e.target as Node)) {
        if (document.pointerLockElement !== video) {
          video.requestPointerLock();
        }
      }
    };

    document.addEventListener('pointerlockchange', handlePointerLockChange);
    document.addEventListener('mousemove', handleMouseMove);
    document.addEventListener('mousedown', handleMouseDown);
    document.addEventListener('mouseup', handleMouseUp);
    document.addEventListener('wheel', handleWheel, { passive: false });
    document.addEventListener('keydown', handleKeyDown);
    document.addEventListener('keyup', handleKeyUp);
    document.addEventListener('click', handleDocumentClick);

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
      if (document.pointerLockElement === videoRef.current) {
        document.exitPointerLock();
      }
      document.removeEventListener('pointerlockchange', handlePointerLockChange);
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mousedown', handleMouseDown);
      document.removeEventListener('mouseup', handleMouseUp);
      document.removeEventListener('wheel', handleWheel);
      document.removeEventListener('keydown', handleKeyDown);
      document.removeEventListener('keyup', handleKeyUp);
      document.removeEventListener('click', handleDocumentClick);
      pcRef.current?.close();
      pcRef.current = null;
      ws.close();
      wsRef.current = null;
      startedRef.current = false;
      if (videoRef.current) {
        videoRef.current.srcObject = null;
      }
    };
  }, [room, connectionKey, mouseEnabled, keyboardEnabled]);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (
        statusOpen &&
        statusPopoverRef.current &&
        statusButtonRef.current &&
        !statusPopoverRef.current.contains(e.target as Node) &&
        !statusButtonRef.current.contains(e.target as Node)
      ) {
        setStatusOpen(false);
      }
      if (
        inputMenuOpen &&
        inputPopoverRef.current &&
        inputButtonRef.current &&
        !inputPopoverRef.current.contains(e.target as Node) &&
        !inputButtonRef.current.contains(e.target as Node)
      ) {
        setInputMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [statusOpen, inputMenuOpen]);

  const reconnect = () => {
    if (document.pointerLockElement === videoRef.current) {
      document.exitPointerLock();
    }
    setStatusOpen(false);
    setInputMenuOpen(false);
    setConnectionKey((k) => k + 1);
  };

  return (
    <main className="shell">
      <section className="iconBar" aria-label="Controls">
        <button
          ref={statusButtonRef}
          className="iconButtonWrap"
          title="Connection status"
          onClick={() => setStatusOpen((open) => !open)}
        >
          <StatusIcon state={state} />
        </button>
        <button
          className="iconButtonWrap"
          title="Reconnect"
          onClick={reconnect}
        >
          <RefreshIcon />
        </button>
        <button
          ref={inputButtonRef}
          className={`iconButtonWrap ${inputMenuOpen ? 'active' : ''}`}
          title="Input mapping"
          onClick={() => setInputMenuOpen((open) => !open)}
        >
          <InputIcon />
        </button>
      </section>

      {statusOpen && (
        <div className="popover statusPopover" ref={statusPopoverRef}>
          <h3>Status</h3>
          <dl className="meta">
            <div>
              <dt>State</dt>
              <dd>{state}</dd>
            </div>
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
              <dt>FPS</dt>
              <dd>{fps !== null ? fps.toFixed(1) : '-'}</dd>
            </div>
            <div>
              <dt>Latency</dt>
              <dd>{roundTripMs !== null ? `${roundTripMs.toFixed(1)} ms` : '-'}</dd>
            </div>
            <div>
              <dt>Video</dt>
              <dd>{videoStats}</dd>
            </div>
            <div>
              <dt>Clock</dt>
              <dd>
                {clock.toLocaleTimeString()}.{clock.getMilliseconds().toString().padStart(3, '0')}
              </dd>
            </div>
          </dl>
        </div>
      )}

      {inputMenuOpen && (
        <div className="popover inputPopover" ref={inputPopoverRef}>
          <h3>Input mapping</h3>
          <Toggle
            label="Enable input"
            checked={inputEnabled}
            onChange={setInputEnabledSync}
          />
          <Toggle
            label="Mouse"
            checked={mouseEnabled}
            onChange={setMouseEnabled}
          />
          <Toggle
            label="Keyboard"
            checked={keyboardEnabled}
            onChange={setKeyboardEnabled}
          />
          <div className="inputHint">
            {inputLocked ? 'Click or ESC to release' : 'Click video to lock mouse'}
          </div>
        </div>
      )}

      <section className="viewer">
        <video
          ref={videoRef}
          autoPlay
          playsInline
          muted
          controls={false}
        />
        {inputEnabled && (
          <div className="inputOverlay">
            {inputLocked ? (
              <span>Click or ESC to release</span>
            ) : (
              <span>Click video to lock mouse</span>
            )}
          </div>
        )}
        {inputEnabled && (
          <RemoteCursor
            videoRef={videoRef}
            cursorPos={cursorPos}
            screenSize={screenSize}
          />
        )}
      </section>
    </main>
  );
}
