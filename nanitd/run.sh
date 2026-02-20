#!/usr/bin/with-contenv bashio

declare log_level

log_level=$(bashio::config 'log_level' 'info')

bashio::log.info "Starting Nanit Daemon..."

export NANIT_HTTP_ADDR="0.0.0.0:8080"
export NANIT_SESSION_PATH="/data/session.json"
export NANIT_LOG_LEVEL="${log_level}"
export NANIT_HLS_ENABLED="true"
export NANIT_HLS_OUTPUT_DIR="/tmp/nanit-hls"

exec /usr/bin/nanitd 2>&1
