// Playback viewer — reads config from window.PLAYBACK_CONFIG
var _pc = window.PLAYBACK_CONFIG;
var go2rtcProxy = _pc.go2rtcProxy;
var nvrID = _pc.nvrID;
var cameraChannel = _pc.cameraChannel;
var cameraName = _pc.cameraName;
var nvrName = _pc.nvrName;
var cameraID = _pc.cameraID;

function playbackApp() {
    return {
        selectedDate: _pc.selectedDate,
        quality: 'sub',
        segments: [],
        currentSegmentIndex: -1,
        currentSession: null,
        playbackStart: '',
        playbackEnd: '',
        loading: false,
        searching: false,
        searchError: '',
        playheadPosition: 0,
        playheadCurrentSec: 0,
        playheadTimer: null,
        exportJob: null,
        exportPollTimer: null,
        exportStartTime: null,
        showExportModal: false,
        exportRangeStart: '08:00',
        exportRangeEnd: '09:00',
        exportName: '',
        paused: false,
        speed: 1,
        dragging: false,
        hoverTime: '',
        hoverPosition: 0,
        exportToast: null,
        exportToastTimer: null,

        init() {
            this.searchRecordings();

            // Stop playback session on page leave
            window.addEventListener('beforeunload', () => {
                if (this.currentSession) {
                    navigator.sendBeacon('/cameras/playback/stop?session=' + this.currentSession.id);
                }
            });
        },

        // Parse ISO time string to seconds-of-day (ignoring timezone)
        isoToSeconds(iso) {
            // Extract HH:MM:SS from "YYYY-MM-DDTHH:MM:SSZ" or "YYYY-MM-DDTHH:MM:SS.000Z"
            const m = iso.match(/T(\d{2}):(\d{2}):(\d{2})/);
            if (!m) return 0;
            return parseInt(m[1]) * 3600 + parseInt(m[2]) * 60 + parseInt(m[3]);
        },

        // Build an ISO string for a given seconds-of-day on the selected date
        secondsToISO(secs) {
            const h = Math.floor(secs / 3600);
            const m = Math.floor((secs % 3600) / 60);
            const s = secs % 60;
            const pad = (n) => n < 10 ? '0' + n : '' + n;
            return this.selectedDate + 'T' + pad(h) + ':' + pad(m) + ':' + pad(s) + 'Z';
        },

        async searchRecordings() {
            this.searching = true;
            this.searchError = '';
            this.segments = [];
            try {
                const resp = await fetch('/cameras/recordings/search?nvr=' + nvrID + '&channel=' + cameraChannel + '&date=' + this.selectedDate);
                if (resp.ok) {
                    const data = await resp.json();
                    // Store raw ISO strings - NVR times are local time labeled as Z
                    this.segments = (data.segments || []).map(s => ({
                        ...s,
                        startISO: s.startTime,
                        endISO: s.endTime,
                        startSec: this.isoToSeconds(s.startTime),
                        endSec: this.isoToSeconds(s.endTime)
                    }));
                    // Auto-play first segment
                    if (this.segments.length > 0) {
                        this.playSegment(0);
                    }
                } else {
                    const text = await resp.text();
                    console.error('[playback] recording search failed: HTTP ' + resp.status + ' - ' + (text || resp.statusText));
                    this.searchError = 'Search failed: ' + (text || resp.statusText);
                }
            } catch (e) {
                console.error('Failed to search recordings:', e);
                this.searchError = 'Search failed: ' + e.message;
            }
            this.searching = false;
        },

        segmentLeft(seg) {
            return (seg.startSec / 86400) * 100;
        },

        segmentWidth(seg) {
            const duration = seg.endSec - seg.startSec;
            return Math.max((duration / 86400) * 100, 0.2);
        },

        formatTime(iso) {
            const m = (typeof iso === 'string' ? iso : '').match(/T(\d{2}):(\d{2}):(\d{2})/);
            if (!m) return '';
            var h = parseInt(m[1]);
            var suffix = h < 12 ? 'AM' : 'PM';
            if (h === 0) h = 12;
            else if (h > 12) h -= 12;
            return h + ':' + m[2] + ':' + m[3] + ' ' + suffix;
        },

        // Convert seconds-of-day to a display string like "2:30:15 PM"
        secsToDisplay(secs) {
            var h = Math.floor(secs / 3600);
            var m = Math.floor((secs % 3600) / 60);
            var s = Math.floor(secs % 60);
            var suffix = h < 12 ? 'AM' : 'PM';
            if (h === 0) h = 12;
            else if (h > 12) h -= 12;
            var pad = function(n) { return n < 10 ? '0' + n : '' + n; };
            return h + ':' + pad(m) + ':' + pad(s) + ' ' + suffix;
        },

        getTimelineBar() {
            if (!this._timelineBar) {
                this._timelineBar = this.$refs.timelineBar || document.querySelector('.timeline-bar');
            }
            return this._timelineBar;
        },

        timelinePctToSec(event) {
            var bar = this.getTimelineBar();
            if (!bar) return 0;
            var rect = bar.getBoundingClientRect();
            var clientX = event.touches ? event.touches[0].clientX : event.clientX;
            var x = Math.max(0, Math.min(clientX - rect.left, rect.width));
            return (x / rect.width) * 86400;
        },

        timelineHover(event) {
            if (this.dragging) return;
            var sec = this.timelinePctToSec(event);
            this.hoverTime = this.secsToDisplay(sec);
            this.hoverPosition = (sec / 86400) * 100;
        },

        startDrag(event) {
            this.dragging = true;
            this.hoverTime = '';
            this._dragPct = 0;

            var playhead = document.querySelector('.timeline-playhead');
            var tooltip = document.querySelector('.playhead-time-tooltip');
            var bar = document.querySelector('.timeline-bar');
            if (!playhead || !bar) return;

            var self = this;
            if (tooltip) tooltip.style.display = 'block';

            function update(e) {
                var clientX = e.touches ? e.touches[0].clientX : e.clientX;
                var rect = bar.getBoundingClientRect();
                var x = Math.max(0, Math.min(clientX - rect.left, rect.width));
                var pct = (x / rect.width) * 100;
                self._dragPct = pct;
                playhead.style.left = pct + '%';
                var sec = (pct / 100) * 86400;
                if (tooltip) tooltip.textContent = self.secsToDisplay(sec);
            }

            function onMove(e) {
                if (e.touches) e.preventDefault();
                update(e);
            }

            function onUp() {
                window.removeEventListener('mousemove', onMove);
                window.removeEventListener('mouseup', onUp);
                window.removeEventListener('touchmove', onMove);
                window.removeEventListener('touchend', onUp);
                if (tooltip) tooltip.style.display = 'none';
                self.dragging = false;
                var sec = Math.floor((self._dragPct / 100) * 86400);
                self.seekToTimelineSec(sec);
            }

            update(event);
            window.addEventListener('mousemove', onMove);
            window.addEventListener('mouseup', onUp);
            window.addEventListener('touchmove', onMove, { passive: false });
            window.addEventListener('touchend', onUp);
        },

        seekToTimelineSec(clickSec) {
            // Find the segment that contains this time
            var containingSegment = null;
            for (var i = 0; i < this.segments.length; i++) {
                var seg = this.segments[i];
                if (clickSec >= seg.startSec && clickSec <= seg.endSec) {
                    containingSegment = seg;
                    break;
                }
            }

            if (!containingSegment) {
                // Find nearest segment
                var minDist = Infinity;
                for (var i = 0; i < this.segments.length; i++) {
                    var seg = this.segments[i];
                    var midSec = (seg.startSec + seg.endSec) / 2;
                    var dist = Math.abs(clickSec - midSec);
                    if (dist < minDist) {
                        minDist = dist;
                        containingSegment = seg;
                    }
                }
            }

            if (!containingSegment) return;

            // Update current segment index
            var idx = this.segments.indexOf(containingSegment);
            this.currentSegmentIndex = idx >= 0 ? idx : 0;

            // Seek to the clicked time within the segment (clamped to segment bounds)
            var seekSec = Math.max(containingSegment.startSec, Math.min(clickSec, containingSegment.endSec));
            var seekISO = this.secondsToISO(seekSec);
            this.seekTo(seekISO, containingSegment.endISO);
        },

        playSegment(index) {
            if (index < 0 || index >= this.segments.length) return;
            this.currentSegmentIndex = index;
            const seg = this.segments[index];
            this.seekTo(seg.startISO, seg.endISO);
        },

        playNextSegment() {
            if (this.currentSegmentIndex < this.segments.length - 1) {
                var nextSeg = this.segments[this.currentSegmentIndex + 1];
                console.log('[playback] advancing from segment ' + this.currentSegmentIndex + ' to ' + (this.currentSegmentIndex + 1) + ': ' + (nextSeg ? nextSeg.startISO + ' -> ' + nextSeg.endISO : 'unknown'));
                this.playSegment(this.currentSegmentIndex + 1);
            } else {
                console.log('[playback] reached last segment (' + this.currentSegmentIndex + '), stopping');
            }
        },

        playPrevSegment() {
            if (this.currentSegmentIndex > 0) {
                this.playSegment(this.currentSegmentIndex - 1);
            }
        },

        getPlaybackVideo() {
            // go2rtc's video-stream creates a <video> inside .video-container
            var container = document.querySelector('.playback-wrapper .video-container');
            if (!container) return null;
            return container.querySelector('video');
        },

        togglePause() {
            var video = this.getPlaybackVideo();
            if (!video) {
                console.log('togglePause: no video element found');
                return;
            }
            console.log('togglePause: video.paused=' + video.paused, 'video.readyState=' + video.readyState);
            if (this.paused) {
                video.play().catch(function(e) { console.log('play failed:', e); });
                this.paused = false;
            } else {
                video.pause();
                this.paused = true;
            }
        },

        setSpeed(newSpeed) {
            if (newSpeed === this.speed) return;
            var oldSpeed = this.speed;
            this.speed = newSpeed;
            // Re-seek at current playhead position with new speed
            if (this.currentSession && this.playbackEnd) {
                var resumeISO = this.playheadCurrentSec > 0
                    ? this.secondsToISO(Math.floor(this.playheadCurrentSec))
                    : this.playbackStart;
                console.log('setSpeed: ' + oldSpeed + 'x -> ' + newSpeed + 'x, playheadCurrentSec=' + this.playheadCurrentSec + ', resumeISO=' + resumeISO + ', playbackStart=' + this.playbackStart);
                this.seekTo(resumeISO, this.playbackEnd);
            }
        },

        async seekTo(startTime, endTime) {
            this.loading = true;
            this.paused = false;
            this.playbackStart = startTime;
            this.playbackEnd = endTime;

            const body = {
                nvrId: nvrID,
                channel: cameraChannel,
                startTime: startTime,
                endTime: endTime,
                quality: this.quality,
                speed: this.speed,
                sessionId: this.currentSession ? this.currentSession.id : ''
            };

            try {
                const resp = await fetch('/cameras/playback/seek', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body)
                });

                if (resp.ok) {
                    this.currentSession = await resp.json();
                    this.startPlaybackStream(this.currentSession.streamName);
                    this.startPlayheadTracking(startTime, endTime);
                } else {
                    var text = await resp.text();
                    this.searchError = 'Seek failed: ' + (text || resp.statusText);
                }
            } catch (e) {
                console.error('Seek failed:', e);
                this.searchError = 'Seek failed: ' + e.message;
            }
            this.loading = false;
        },

        startPlaybackStream(streamName) {
            // Destroy and recreate the video-stream element for a clean reconnect
            const container = document.querySelector('.playback-wrapper .video-container');
            if (!container) return;
            const old = document.getElementById('playback-stream');
            if (old) {
                try { old.background = false; if (typeof old.ondisconnect === 'function') old.ondisconnect(); } catch(e) { console.error('[playback] stream cleanup error:', e); }
                old.remove();
            }

            const fresh = document.createElement('video-stream');
            fresh.id = 'playback-stream';
            fresh.style.cssText = 'width:100%; height:100%; display:block;';
            container.insertBefore(fresh, container.firstChild);

            // Use all modes (webrtc, then mse, then hls fallback) for playback
            fresh.mode = 'webrtc,mse,hls';
            fresh.src = new URL(go2rtcProxy + '/api/ws?src=' + encodeURIComponent(streamName), location.href);
        },

        toggleFullscreen() {
            var wrapper = document.querySelector('.playback-wrapper .video-wrapper');
            if (!wrapper) return;
            if (document.fullscreenElement) {
                document.exitFullscreen();
            } else {
                wrapper.requestFullscreen().catch(function(e) {
                    console.log('Fullscreen failed:', e);
                });
            }
        },

        startPlayheadTracking(startTime, endTime) {
            if (this.playheadTimer) clearInterval(this.playheadTimer);

            const startSec = this.isoToSeconds(startTime);
            var endSec = this.isoToSeconds(endTime);
            // Handle segments that cross midnight (endSec < startSec)
            if (endSec < startSec) endSec += 86400;
            const totalDuration = endSec - startSec;
            const startedAt = Date.now();
            let lastRecvBytes = -1;
            let staleCount = 0;
            let checkCounter = 0;

            this.playheadCurrentSec = startSec;
            this.playheadPosition = (startSec / 86400) * 100;

            this.playheadTimer = setInterval(async () => {
                const elapsed = (Date.now() - startedAt) / 1000;

                // Time-based advance (fallback) — account for playback speed
                var scaledElapsed = elapsed * (this.speed || 1);
                if (scaledElapsed >= totalDuration) {
                    console.log('[playback-health] ADVANCING: time-based, elapsed=' + Math.round(elapsed) + 's, scaledElapsed=' + Math.round(scaledElapsed) + 's, totalDuration=' + totalDuration + 's, speed=' + this.speed + 'x');
                    clearInterval(this.playheadTimer);
                    this.playNextSegment();
                    return;
                }

                const currentSec = startSec + scaledElapsed;
                this.playheadCurrentSec = currentSec;
                this.playheadPosition = (currentSec / 86400) * 100;

                // Stream health check via go2rtc API every 5 seconds (after 20s buffer)
                checkCounter++;
                if (this.currentSession && elapsed > 20 && checkCounter % 5 === 0) {
                    try {
                        const resp = await fetch(go2rtcProxy + '/api/streams');
                        if (resp.ok) {
                            const streams = await resp.json();
                            const info = streams[this.currentSession.streamName];

                            // Stream removed — source definitely gone
                            if (!info) {
                                staleCount++;
                                console.log('[playback-health] stream not found in go2rtc, staleCount=' + staleCount + '/3, elapsed=' + Math.round(elapsed) + 's');
                                if (staleCount >= 3) {
                                    console.log('[playback-health] ADVANCING: stream removed after ' + staleCount + ' checks');
                                    clearInterval(this.playheadTimer);
                                    this.playNextSegment();
                                    return;
                                }
                            // No producers yet — RTSP may still be connecting over VPN
                            } else if (!info.producers || info.producers.length === 0) {
                                staleCount++;
                                console.log('[playback-health] no producers, staleCount=' + staleCount + '/4, elapsed=' + Math.round(elapsed) + 's');
                                if (staleCount >= 4) {
                                    console.log('[playback-health] ADVANCING: no producers after ' + staleCount + ' checks');
                                    clearInterval(this.playheadTimer);
                                    this.playNextSegment();
                                    return;
                                }
                            } else {
                                // Check if data is still flowing (recv bytes increasing)
                                let currentBytes = 0;
                                for (const p of info.producers) {
                                    currentBytes += (p.recv || 0);
                                }
                                if (lastRecvBytes >= 0 && currentBytes <= lastRecvBytes) {
                                    staleCount++;
                                    console.log('[playback-health] recv stalled at ' + currentBytes + ' bytes, staleCount=' + staleCount + '/4, elapsed=' + Math.round(elapsed) + 's');
                                    if (staleCount >= 4) {
                                        console.log('[playback-health] ADVANCING: no new data after ' + staleCount + ' checks (' + currentBytes + ' bytes)');
                                        clearInterval(this.playheadTimer);
                                        this.playNextSegment();
                                        return;
                                    }
                                } else {
                                    if (staleCount > 0) {
                                        console.log('[playback-health] recovered, recv=' + currentBytes + ' (was ' + lastRecvBytes + '), resetting staleCount');
                                    }
                                    staleCount = 0;
                                }
                                lastRecvBytes = currentBytes;
                            }
                        }
                    } catch (e) {
                        console.log('[playback-health] fetch error: ' + e.message);
                    }
                }
            }, 1000);
        },

        openExportModal() {
            if (this.playbackStart && this.playbackEnd) {
                // Extract HH:MM directly from ISO string — NVR times are local time labeled as Z
                var sm = this.playbackStart.match(/T(\d{2}:\d{2})/);
                var em = this.playbackEnd.match(/T(\d{2}:\d{2})/);
                if (sm) this.exportRangeStart = sm[1];
                if (em) this.exportRangeEnd = em[1];
            }
            this.exportName = '';
            this.showExportModal = true;
        },

        exportCurrentClipFromModal() {
            this.showExportModal = false;
            this.doExport(this.playbackStart, this.playbackEnd);
        },

        exportRangeFromModal() {
            this.showExportModal = false;
            var startISO = this.selectedDate + 'T' + this.exportRangeStart + ':00';
            var endISO = this.selectedDate + 'T' + this.exportRangeEnd + ':00';
            console.log('Export range:', startISO, 'to', endISO);
            this.doExport(startISO, endISO);
        },

        formatExportTime(iso) {
            try {
                var d = new Date(iso);
                return d.toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' });
            } catch(_) { return iso; }
        },

        showExportToast(msg, type) {
            if (this.exportToastTimer) clearTimeout(this.exportToastTimer);
            this.exportToast = { message: msg, type: type || 'success' };
            this.exportToastTimer = setTimeout(() => { this.exportToast = null; }, 5000);
        },

        async doExport(startTime, endTime) {
            const body = {
                nvrId: nvrID,
                channel: cameraChannel,
                cameraName: cameraName,
                nvrName: nvrName,
                startTime: startTime,
                endTime: endTime,
                quality: this.quality,
                exportName: this.exportName || ''
            };

            try {
                const resp = await fetch('/cameras/export/start', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body)
                });

                if (resp.ok) {
                    this.showExportToast(
                        'Export queued: ' + cameraName + ' from ' +
                        this.formatExportTime(startTime) + ' to ' +
                        this.formatExportTime(endTime),
                        'success'
                    );
                } else {
                    this.showExportToast('Failed to queue export', 'error');
                }
            } catch (e) {
                console.error('Export start failed:', e);
                this.showExportToast('Failed to queue export', 'error');
            }
        },

        exportETA() {
            if (!this.exportJob || !this.exportStartTime || this.exportJob.progress <= 0) return '';
            var elapsed = (Date.now() - this.exportStartTime) / 1000;
            var pct = this.exportJob.progress / 100;
            var totalEst = elapsed / pct;
            var remaining = Math.max(0, Math.round(totalEst - elapsed));
            // Smooth: blend with previous ETA to prevent jumping
            if (this._lastETA !== undefined && this._lastETA > 0) {
                remaining = Math.round(this._lastETA * 0.7 + remaining * 0.3);
            }
            this._lastETA = remaining;
            if (remaining < 60) return remaining + 's left';
            var m = Math.floor(remaining / 60);
            var s = remaining % 60;
            return m + 'm ' + s + 's left';
        },

        pollExportProgress() {
            if (this.exportPollTimer) clearInterval(this.exportPollTimer);
            if (!this.exportJob) return;

            var pollErrors = 0;
            this.exportPollTimer = setInterval(async () => {
                try {
                    const resp = await fetch('/cameras/export/progress/' + this.exportJob.id);
                    if (resp.ok) {
                        pollErrors = 0;
                        this.exportJob = await resp.json();
                        if (this.exportJob.status === 'complete' || this.exportJob.status === 'failed') {
                            clearInterval(this.exportPollTimer);
                        }
                    } else {
                        pollErrors++;
                        console.error('[export] progress poll returned ' + resp.status + ' (attempt ' + pollErrors + ')');
                        if (pollErrors >= 5) {
                            clearInterval(this.exportPollTimer);
                            this.showExportToast('Lost connection to export job', 'error');
                        }
                    }
                } catch (e) {
                    pollErrors++;
                    console.error('[export] progress poll failed: ' + e.message + ' (attempt ' + pollErrors + ')');
                    if (pollErrors >= 5) {
                        clearInterval(this.exportPollTimer);
                        this.showExportToast('Lost connection to export job', 'error');
                    }
                }
            }, 1000);
        },

        async cancelExport() {
            if (!this.exportJob) return;
            try {
                var resp = await fetch('/cameras/export/cancel/' + this.exportJob.id, { method: 'POST' });
                if (!resp.ok) {
                    console.error('[export] cancel returned HTTP ' + resp.status);
                    this.showExportToast('Failed to cancel export', 'error');
                    return;
                }
                this.exportJob.status = 'failed';
                this.exportJob.error = 'Cancelled by user';
                if (this.exportPollTimer) clearInterval(this.exportPollTimer);
            } catch (e) {
                console.error('[export] cancel failed:', e);
                this.showExportToast('Failed to cancel export: ' + e.message, 'error');
            }
        },

        formatFileSize(bytes) {
            if (!bytes) return '';
            if (bytes >= 1073741824) return (bytes / 1073741824).toFixed(1) + ' GB';
            if (bytes >= 1048576) return (bytes / 1048576).toFixed(1) + ' MB';
            if (bytes >= 1024) return (bytes / 1024).toFixed(1) + ' KB';
            return bytes + ' B';
        }
    };
}
