#!/bin/bash
# Copy SQLite database from staging to production.
# Run from any machine with SSH access to both servers.

set -e

STAGING_HOST="deploy@10.0.0.10"
STAGING_DB="/opt/cameradashboard/data/cameradashboard.db"

PROD_HOST="deploy@app-01"
PROD_DIR="/opt/cameradashboard/data"
PROD_DB="$PROD_DIR/cameradashboard.db"

TMP="/tmp/cameradashboard.db"

echo "=== Stopping prod service ==="
ssh $PROD_HOST 'sudo systemctl stop cameradashboard'

echo "=== Ensuring prod data directory exists ==="
ssh $PROD_HOST "mkdir -p $PROD_DIR"

echo "=== Copying DB from staging to local ==="
scp "$STAGING_HOST:$STAGING_DB" "$TMP"

echo "=== Copying DB from local to prod ==="
scp "$TMP" "$PROD_HOST:/tmp/cameradashboard.db"
ssh $PROD_HOST "sudo mv /tmp/cameradashboard.db $PROD_DB && sudo rm -f ${PROD_DB}-wal ${PROD_DB}-shm && sudo chown deploy:deploy $PROD_DB"
rm -f "$TMP"

echo "=== Starting prod service ==="
ssh $PROD_HOST 'sudo systemctl start cameradashboard'

echo "=== Verifying ==="
ssh $PROD_HOST 'sudo systemctl status cameradashboard --no-pager'

echo ""
echo "Done! SQLite database copied from staging to production."
