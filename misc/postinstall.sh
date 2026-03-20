#!/bin/sh
systemctl daemon-reload
systemctl enable systemd-supervisord.service

if systemctl is-enabled --quiet systemd-supervisord.service; then
    systemctl restart systemd-supervisord.service || true
fi
