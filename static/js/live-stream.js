// Live stream viewer — reads config from window.STREAM_CONFIG
// go2rtc scripts (video-rtc.js, video-stream.js) are loaded via static
// imports in the inline module in camera_dashboard.html.
const { go2rtcProxy, streamName } = window.STREAM_CONFIG;
const mode = 'webrtc,mse,hls';

// Debug logger (admin only - panel may not exist for non-admins)
const dbg = document.getElementById('stream-debug');
function log(msg) {
    if (!dbg) return;
    const t = new Date().toLocaleTimeString();
    dbg.innerHTML += '<div>[' + t + '] ' + msg + '</div>';
    dbg.scrollTop = dbg.scrollHeight;
    console.log('[stream-debug]', msg);
}

log('isAppleMobile=' + isAppleMobile + ' mode=' + mode);
log('MediaSource=' + ('MediaSource' in window) + ' ManagedMediaSource=' + ('ManagedMediaSource' in window));

await customElements.whenDefined('video-stream');

// Override go2rtc's aggressive playbackRate catchup on Apple devices.
// go2rtc sets playbackRate=gap which causes Safari to oscillate between
// fast-forward and stalling. This clamps the rate and jumps to live instead.
function trimBuffer(video) {
    try {
        var ms = video.ms_ || video.mediaSource_ || video.webkitMediaSource;
        if (!ms && video.srcObject && video.srcObject instanceof MediaSource) ms = video.srcObject;
        if (!ms) {
            // Walk up to the video-stream element to find its MediaSource
            var el = video.closest('video-stream');
            if (el && el.pc && el.pc.ms) ms = el.pc.ms;
        }
        if (!ms || ms.readyState !== 'open') return false;
        var buffers = ms.sourceBuffers;
        if (!buffers || !buffers.length) return false;
        var trimmed = false;
        for (var i = 0; i < buffers.length; i++) {
            var sb = buffers[i];
            if (sb.updating) continue;
            if (sb.buffered && sb.buffered.length > 0) {
                var start = sb.buffered.start(0);
                var removeEnd = video.currentTime - 2;
                if (removeEnd > start) {
                    sb.remove(start, removeEnd);
                    trimmed = true;
                }
            }
        }
        return trimmed;
    } catch (e) {
        return false;
    }
}

function startBufferGuard(video) {
    if (!isAppleMobile) return null;
    return setInterval(() => {
        if (!video || !video.buffered || !video.buffered.length) return;
        const end = video.buffered.end(video.buffered.length - 1);
        const start = video.buffered.start(0);
        const gap = end - video.currentTime;
        const totalBuffered = end - start;

        // Trim old buffer data to prevent SourceBuffer from filling up
        if (totalBuffered > 30) {
            log('Buffer large (' + totalBuffered.toFixed(1) + 's), trimming');
            trimBuffer(video);
        }

        if (gap > 1.5) {
            video.currentTime = end - 0.3;
            video.playbackRate = 1.0;
        } else if (gap > 0.5) {
            video.playbackRate = 1.2;
        } else {
            video.playbackRate = 1.0;
        }
    }, 1000);
}

var activeBufferGuard = null;
var activeStallWatcher = null;
var currentStreamName = null;
var stallRetryCount = 0;
const MAX_AUTO_RETRIES = 3;
const STALL_TIMEOUT_MS = 3000;

// Create the stall overlay (hidden by default)
function getOrCreateStallOverlay() {
    var overlay = document.getElementById('stall-overlay');
    if (overlay) return overlay;
    overlay = document.createElement('div');
    overlay.id = 'stall-overlay';
    overlay.style.cssText = 'display:none; position:absolute; top:0; left:0; width:100%; height:100%; background:rgba(0,0,0,0.7); z-index:10; cursor:pointer; justify-content:center; align-items:center; flex-direction:column;';
    overlay.innerHTML = '<div style="color:#fff;font-size:1.1rem;margin-bottom:8px;">Stream stalled</div>' +
        '<div style="color:rgba(255,255,255,0.6);font-size:0.85rem;">Tap to reconnect</div>';
    overlay.addEventListener('click', function() {
        overlay.style.display = 'none';
        stallRetryCount = 0;
        reconnectStream();
    });
    var container = document.querySelector('.video-container');
    if (container) {
        container.style.position = 'relative';
        container.appendChild(overlay);
    }
    return overlay;
}

function showStallOverlay() {
    var overlay = getOrCreateStallOverlay();
    overlay.style.display = 'flex';
}

function hideStallOverlay() {
    var overlay = document.getElementById('stall-overlay');
    if (overlay) overlay.style.display = 'none';
}

// Cleanly close a video-stream element's WebSocket and RTCPeerConnection.
// go2rtc's disconnectedCallback skips cleanup when background=true,
// so we must call ondisconnect() explicitly before removing the element.
function closeStream(el) {
    if (!el) return;
    try {
        el.background = false;
        if (typeof el.ondisconnect === 'function') el.ondisconnect();
    } catch (e) {}
}

function reconnectStream() {
    if (!currentStreamName) return;
    log('Reconnecting stream: ' + currentStreamName + ' (retry ' + stallRetryCount + ')');
    hideStallOverlay();

    var container = document.querySelector('.video-container');
    var old = document.getElementById('video-stream');
    if (old) { closeStream(old); old.remove(); }

    var fresh = document.createElement('video-stream');
    fresh.id = 'video-stream';
    fresh.style.cssText = 'width:100%; height:100%; display:block;';
    container.insertBefore(fresh, container.firstChild);

    var loadingEl = document.getElementById('video-loading');
    if (loadingEl) loadingEl.style.display = '';

    startStream(fresh, currentStreamName);
}

function startStallWatcher(video) {
    if (activeStallWatcher) clearInterval(activeStallWatcher);
    var lastTime = -1;
    var stalledSince = 0;

    activeStallWatcher = setInterval(function() {
        if (!video || video.paused || video.ended) return;
        var now = video.currentTime;
        if (now === lastTime) {
            if (stalledSince === 0) stalledSince = Date.now();
            var stallDuration = Date.now() - stalledSince;
            if (stallDuration >= STALL_TIMEOUT_MS) {
                log('Stall detected (' + (stallDuration / 1000).toFixed(1) + 's)');
                if (stallRetryCount < MAX_AUTO_RETRIES) {
                    stallRetryCount++;
                    reconnectStream();
                } else {
                    log('Max auto-retries reached, showing overlay');
                    clearInterval(activeStallWatcher);
                    activeStallWatcher = null;
                    showStallOverlay();
                }
            }
        } else {
            stalledSince = 0;
            stallRetryCount = 0;
        }
        lastTime = now;
    }, 1000);
}

function startStream(el, name) {
    currentStreamName = name;
    el.background = true;
    el.mode = mode;
    el.src = new URL(go2rtcProxy + '/api/ws?src=' + encodeURIComponent(name), location.href);
    log('Stream started: ' + name);

    if (activeBufferGuard) clearInterval(activeBufferGuard);
    if (activeStallWatcher) clearInterval(activeStallWatcher);

    // Force autoplay, hide controls, and fit video properly
    setTimeout(function() {
        const video = el.querySelector('video');
        if (video) {
            video.controls = false;
            video.autoplay = true;
            video.playsInline = true;
            video.muted = true;
            video.style.objectFit = 'contain';
            video.style.width = '100%';
            video.style.height = '100%';
            video.play().catch(function() {});

            log('Video: readyState=' + video.readyState + ' paused=' + video.paused);

            activeBufferGuard = startBufferGuard(video);
            startStallWatcher(video);

            const loadingEl = document.getElementById('video-loading');
            if (loadingEl) {
                video.addEventListener('playing', () => {
                    loadingEl.style.display = 'none';
                    hideStallOverlay();
                    log('Video playing - resolution: ' + video.videoWidth + 'x' + video.videoHeight);
                }, {once: true});
                setTimeout(() => { loadingEl.style.display = 'none'; }, 5000);
            }

            // Log resolution once loaded
            video.addEventListener('loadeddata', () => {
                log('Video loaded: ' + video.videoWidth + 'x' + video.videoHeight);
            });

            video.addEventListener('error', () => {
                log('Video error: ' + (video.error ? video.error.message : 'unknown'));
                if (stallRetryCount < MAX_AUTO_RETRIES) {
                    stallRetryCount++;
                    setTimeout(reconnectStream, 1000);
                } else {
                    showStallOverlay();
                }
            });
        }
    }, 500);
}

const el = document.getElementById('video-stream');
if (el) {
    startStream(el, streamName);
}

// Expose switchStream globally for the SD/HD buttons
window.switchStream = function(name, btn) {
    document.querySelectorAll('.quality-btn').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    const loadingEl = document.getElementById('video-loading');
    if (loadingEl) loadingEl.style.display = '';
    log('Switching stream to: ' + name);
    stallRetryCount = 0;
    hideStallOverlay();

    // Replace the video-stream element entirely to force a clean reconnect
    const container = document.querySelector('.video-container');
    const old = document.getElementById('video-stream');
    if (old) { closeStream(old); old.remove(); }

    const fresh = document.createElement('video-stream');
    fresh.id = 'video-stream';
    fresh.style.cssText = 'width:100%; height:100%; display:block;';
    container.insertBefore(fresh, container.firstChild);

    startStream(fresh, name);
};
