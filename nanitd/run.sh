#!/usr/bin/with-contenv bashio

declare log_level
declare camera_ip

log_level=$(bashio::config 'log_level' 'info')
camera_ip=$(bashio::config 'camera_ip' '')

bashio::log.info "Starting Nanit Daemon..."

export NANIT_HTTP_ADDR="0.0.0.0:8080"
export NANIT_SESSION_PATH="/data/session.json"
export NANIT_LOG_LEVEL="${log_level}"
export NANIT_HLS_ENABLED="true"
export NANIT_HLS_OUTPUT_DIR="/tmp/nanit-hls"

if bashio::var.has_value "${camera_ip}"; then
    export NANIT_CAMERA_IP="${camera_ip}"
    bashio::log.info "Using camera IP: ${camera_ip}"
fi

exec /usr/bin/nanitd 2>&1
