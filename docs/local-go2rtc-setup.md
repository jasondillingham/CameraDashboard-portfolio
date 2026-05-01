# Local go2rtc Setup Guide — Branch Deployment

Deploying a local go2rtc instance at each branch keeps RTSP processing on the LAN and only sends WebRTC output over the VPN. This dramatically improves stream reliability and reduces bandwidth.

## Architecture

```
Before:  Browser → VPN → go2rtc (staging) → VPN → NVR (branch LAN)
After:   Browser → VPN → go2rtc (branch LAN) → NVR (branch LAN)
```

## Prerequisites

- Linux box on the same LAN as the branch NVR (any Ubuntu/Debian with 2+ cores, 2GB+ RAM)
- SSH access to the box
- NVR IP, port, and RTSP credentials
- Firewall access to open ports 1984/tcp and 8555/tcp

## Quick Reference — Existing Deployments

| Branch | NVR IP | go2rtc Host | go2rtc URL |
|--------|--------|-------------|------------|
| Branch A | 10.0.20.21 | branch-a-01 (10.0.20.242) | http://10.0.20.242:1984 |
| Branch B | 10.0.30.40 | branch-b-01 (10.0.30.242) | http://10.0.30.242:1984 |

NVR credentials (shared across all NVRs): `dashboard` / `AnnuallyShor1919`

## Step-by-Step Setup

### 1. Install go2rtc binary

```bash
ssh deploy@<BOX_IP>
curl -sL https://github.com/AlexxIT/go2rtc/releases/latest/download/go2rtc_linux_amd64 -o /tmp/go2rtc
chmod +x /tmp/go2rtc
sudo mv /tmp/go2rtc /opt/go2rtc
sudo mkdir -p /opt/go2rtc-config
```

### 2. Install ffmpeg (required for H.264 transcoding and snapshots)

```bash
sudo apt-get update -qq && sudo apt-get install -y -qq ffmpeg
```

### 3. Create config

Replace `<NVR_IP>`, `<NVR_ID>`, and `<CHANNELS>` for the branch. The script below generates the full config:

```bash
# Set these for your branch
NVR_ID="branch_a"
NVR_IP="10.0.20.21"
NVR_USER="dashboard"
NVR_PASS="AnnuallyShor1919"
CHANNELS=16

# Generate config
cat <<EOF | sudo tee /opt/go2rtc-config/go2rtc.yaml
api:
  username: "admin"
  password: "${NVR_PASS}"
  listen: ":1984"

ffmpeg:
  bin: ffmpeg
  h264: "-c:v libx264 -g 30 -preset ultrafast -tune zerolatency"

webrtc:
  listen: ":8555/tcp"

streams:
EOF

for ch in $(seq 1 $CHANNELS); do
    cat <<EOF | sudo tee -a /opt/go2rtc-config/go2rtc.yaml
  ${NVR_ID}_cam${ch}:
    - rtsp://${NVR_USER}:${NVR_PASS}@${NVR_IP}:554/Streaming/Channels/${ch}01
    - "ffmpeg:${NVR_ID}_cam${ch}#video=h264"
  ${NVR_ID}_cam${ch}_sub:
    - rtsp://${NVR_USER}:${NVR_PASS}@${NVR_IP}:554/Streaming/Channels/${ch}02
    - "ffmpeg:${NVR_ID}_cam${ch}_sub#video=h264"
EOF
done
```

### 4. Create systemd service

```bash
sudo tee /etc/systemd/system/go2rtc.service > /dev/null <<'EOF'
[Unit]
Description=go2rtc - camera streaming server
After=network.target

[Service]
Type=simple
ExecStart=/opt/go2rtc -config /opt/go2rtc-config/go2rtc.yaml
Restart=on-failure
RestartSec=5
SuccessExitStatus=100
RestartForceExitStatus=100

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable go2rtc
sudo systemctl start go2rtc
```

### 5. Open firewall ports

```bash
sudo ufw allow 1984/tcp comment 'go2rtc API'
sudo ufw allow 8555/tcp comment 'go2rtc WebRTC'
```

### 6. Verify

```bash
# Check service is running
sudo systemctl status go2rtc

# Check streams are configured
curl -s http://localhost:1984/api/streams | python3 -c "import json,sys; d=json.load(sys.stdin); print(f'{len(d)} streams')"

# Test frame capture (proves NVR RTSP connectivity)
curl -s -o /dev/null -w '%{http_code} %{size_download}bytes' 'http://localhost:1984/api/frame.jpeg?src=<NVR_ID>_cam1_sub'
# Should return: 200 <size>bytes
```

### 7. Wire into CameraDashboard

Edit `configs/camera_config.json` and add `go2rtcUrl` to the NVR entry:

```json
{
  "id": "<NVR_ID>",
  "name": "<Branch Name>",
  "ip": "<NVR_IP>",
  "port": 554,
  "channels": 16,
  "go2rtcUrl": "http://admin:<NVR_PASS>@<BOX_IP>:1984"
}
```

The URL must include the basic auth credentials (`admin:<password>`) since the go2rtc
API requires authentication. The CameraDashboard strips credentials from log output.

Deploy to **both** staging and production:

Then deploy the updated config and restart:

```bash
# Staging
rsync -az configs/camera_config.json deploy@10.0.0.10:/opt/cameradashboard/config/camera_config.json
ssh deploy@10.0.0.10 "sudo systemctl restart cameradashboard"

# Production
make prod-deploy-config
make prod-restart
```

Verify in the logs:

```bash
ssh deploy@10.0.0.10 "sudo journalctl -u cameradashboard --since '1 min ago' --no-pager | grep -i '<NVR_ID>'"
```

You should see:
- `NVR <id> using local go2rtc at http://<BOX_IP>:1984`
- `Registered per-NVR go2rtc proxy /go2rtc-nvr/<id>/ -> http://<BOX_IP>:1984`
- `Skipping stream registration for NVR <id> (local go2rtc at ...)`

### 8. Remove streams from central go2rtc config

The central go2rtc on the staging server (`10.0.0.10`) still has the branch's streams
in its YAML config. These must be removed so the central server doesn't hold open RTSP
connections to the branch NVR over the VPN.

The go2rtc service runs from the **Dashboard** project config (not CameraDashboard):

```bash
# Remove the branch streams from the central go2rtc YAML
ssh deploy@10.0.0.10 "sudo sed -i '/<NVR_ID>_cam/,+2d' /opt/dashboard/config/go2rtc.yaml"

# Also clean the CameraDashboard copy if it exists
ssh deploy@10.0.0.10 "sudo sed -i '/<NVR_ID>_cam/,+2d' /opt/cameradashboard/config/go2rtc.yaml 2>/dev/null"

# Restart go2rtc to drop the in-memory streams
ssh deploy@10.0.0.10 "sudo systemctl restart go2rtc"

# Verify no streams remain for this branch
ssh deploy@10.0.0.10 "curl -s http://localhost:1984/api/streams | python3 -c \"import json,sys; d=json.load(sys.stdin); print([k for k in d if '<NVR_ID>' in k])\""
# Should print: []
```

**Important**: The go2rtc systemd service uses `/opt/dashboard/config/go2rtc.yaml`
(not the cameradashboard copy). Both files should be cleaned, but the dashboard one is
what go2rtc actually loads on startup.

## Branch NVR Reference

| Branch | NVR ID | NVR IP | Subnet | Channels |
|--------|--------|--------|--------|----------|
| HQ Office | hq_office | 10.0.10.3 | 10.0.10.x | 16 |
| HQ Sales | hq_sales | 10.0.10.15 | 10.0.10.x | 32 |
| HQ Shipping | hq_shipping | 10.0.10.16 | 10.0.10.x | 16 |
| HQ Solar | hq_solar | 10.0.10.4 | 10.0.10.x | 16 |
| Branch D | branch_d | 10.0.50.64 | 10.0.50.x | 32 |
| Branch C | branch_c | 10.0.40.77 | 10.0.40.x | 16 |
| Branch E | branch_e | 10.0.60.54 | 10.0.60.x | 16 |
| Branch F | branch_f | 10.0.70.116 | 10.0.70.x | 16 |
| Branch B | branch_b | 10.0.30.40 | 10.0.30.x | 16 |
| Branch A | branch_a | 10.0.20.21 | 10.0.20.x | 16 |

Note: The 4 HQ NVRs are on the same LAN as the staging server (10.0.0.10) which already runs go2rtc locally — no local box needed there.

## Performance Tuning

The go2rtc config includes ffmpeg flags optimized for low-latency streaming over VPN:

```yaml
ffmpeg:
  bin: ffmpeg
  h264: "-c:v libx264 -g 30 -preset ultrafast -tune zerolatency"
```

What these do:
- **`-preset ultrafast`**: Minimizes ffmpeg startup time — trades compression efficiency for
  speed. The transcode begins immediately instead of buffering frames for quality analysis.
- **`-tune zerolatency`**: Disables look-ahead and frame reordering so encoded frames are
  available to the viewer as fast as possible.
- **`-g 30`**: Sets a 1-second GOP (keyframe interval at 30fps). The browser can't render
  video until it receives a keyframe, so shorter GOPs mean faster stream startup.

### What to expect on first click

When a viewer first opens a camera, go2rtc needs to:
1. Connect to the NVR via RTSP (~1-2s handshake, local LAN)
2. Wait for the NVR to send the first keyframe (~1-4s depending on camera GOP setting)
3. Start ffmpeg transcode if the stream is H.265 (instant with ultrafast preset)
4. Negotiate WebRTC/MSE with the browser through the VPN proxy

Total cold-start is typically 3-6 seconds. Subsequent views are faster because go2rtc
keeps the RTSP connection alive briefly after the last viewer disconnects.

### NVR-side optimization (optional)

If you have access to the Hikvision NVR settings, reducing the camera's I-frame interval
(GOP) from the default (often 50-100 frames) to 30 frames will cut the keyframe wait time
in half. This slightly increases recording storage but significantly improves live view
startup. This setting is under: Configuration > Video/Audio > Sub-stream > I Frame Interval.

## Troubleshooting

**"Stream stalled" on a specific camera**: Check if the camera is connected to the NVR. SSH to the go2rtc box and test: `curl -s -o /dev/null -w '%{http_code}' 'http://localhost:1984/api/frame.jpeg?src=<stream_name>'` — a 500 with an RTSP error means the camera/channel is offline on the NVR.

**go2rtc web UI not loading remotely**: Check UFW — ports 1984 and 8555 must be open. `sudo ufw status`

**Streams work locally but not from dashboard**: Check that the `go2rtcUrl` in `camera_config.json` is reachable from the staging server: `curl -s http://<BOX_IP>:1984/api/streams` from `10.0.0.10`.

**HD streams unreliable**: HD streams are 4-8 Mbps. If the VPN link is saturated, stick with SD. The sub-stream (SD) is ~512kbps and much more VPN-friendly.

## SSH Access to Branch Linux Boxes

```bash
# Standard credentials for CMS Linux boxes
SSHPASS='What@Mess1919' sshpass -e ssh -o StrictHostKeyChecking=no \
  -o PreferredAuthentications=password deploy@<BOX_IP>
```
