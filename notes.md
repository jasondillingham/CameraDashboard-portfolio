# Camera Dashboard Notes

## Decisions & Research

### Playback Speed Control (Not Feasible)
Playback speed requires the RTSP `Scale` header in the PLAY request, but go2rtc does not expose this parameter. The stream plays through an iframe (go2rtc `stream.html` → WebRTC), so there is no HTML5 video element to control `playbackRate` on. Would require either:
- go2rtc adding Scale header support
- Replacing the iframe approach with a custom WebRTC player that can control playback rate at the media level

### Direct RTSP to Browser (Not Possible)
Browsers do not support RTSP natively — there is no way to send an RTSP feed directly to a browser without a relay server. The current go2rtc approach is already the most efficient option: it acts as a lightweight relay converting RTSP → WebRTC with minimal overhead (no transcoding needed for H.264 streams). Alternatives like HLS/DASH add 10-30s latency. The go2rtc server must sit between the NVR and browser, but it uses very little CPU when just relaying (no transcode).

### NVR Only Records Main Stream
Hikvision NVRs only store recordings on the main stream (track `x01`). Attempting to play back substream recordings (track `x02`) fails with "wrong response on DESCRIBE". Both playback and export are forced to use main stream regardless of the user's quality toggle.

### FFmpeg Export: Audio Disabled
NVR cameras report audio codec as "none" which causes FFmpeg to crash (exit status 234). Audio is disabled with `-an` flag for all exports. If audio support is needed later, the codec detection would need to probe for valid audio first.

### FFmpeg Export: Duration Limit Required
The NVR does not close the RTSP connection after the recording ends — FFmpeg would hang indefinitely. The `-t` flag is used to limit the export duration to the requested time range + 5 second buffer.

### RTSP Playback URL: Trailing Slash Required
Hikvision NVRs require a trailing slash before the query parameters in playback URLs:
- Works: `/Streaming/tracks/101/?starttime=...&endtime=...`
- Fails: `/Streaming/tracks/101?starttime=...&endtime=...`

## Architecture Notes

### Stream Flow
1. App registers RTSP sources with go2rtc at startup (2 per channel: main + sub)
2. Browser loads iframe → go2rtc `stream.html` → WebRTC connection
3. go2rtc connects to NVR RTSP only when a viewer is active (on-demand)
4. H.265 streams get automatic FFmpeg transcoding fallback to H.264

### Playback Sessions
- Temporary go2rtc streams named `playback_{sessionID}`
- Max 4 concurrent playback sessions per NVR (LRU eviction)
- Sessions auto-cleanup after 5 minutes of inactivity
- Playback auto-advances to next recording segment when current ends

### Playback Auto-Advance Detection
The wall clock timer alone is unreliable for detecting when a recording segment ends — the NVR keeps the RTSP connection open after playback content finishes, and segment durations can be hours long. To solve this, the playhead tracker polls go2rtc's `/api/streams` API every 5 seconds. When the producer's `recv` bytes stop increasing for 2 consecutive checks (~10 seconds), the recording is considered finished and the next segment auto-plays. The wall clock timer remains as a fallback.

### Export Pipeline
`NVR (RTSP) → FFmpeg (transcode H.265→H.264) → MP4 file → Browser download`
- Transcodes with `-c:v libx264 -preset fast -crf 23`
- Uses `-movflags +faststart` for progressive download
- Progress tracked via FFmpeg stderr `time=` output
- Exports auto-delete after 30 minutes
