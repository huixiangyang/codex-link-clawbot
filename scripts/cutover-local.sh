#!/usr/bin/env bash

set -euo pipefail

old_pid="${1:?必须传入旧 weclaw PID}"
status_file="/root/.weclaw/cutover-status.log"
service_name="weclaw.service"

write_status() {
  printf '%s %s\n' "$(date --iso-8601=seconds)" "$1" >>"${status_file}"
}

write_status "开始切换，旧 PID=${old_pid}"

# 先记录直属子进程，防止父进程退出后 Codex App Server 成为孤儿。
mapfile -t old_children < <(pgrep -P "${old_pid}" || true)
if kill -0 "${old_pid}" 2>/dev/null; then
  kill -TERM "${old_pid}"
fi

for _ in $(seq 1 20); do
  if ! kill -0 "${old_pid}" 2>/dev/null; then
    break
  fi
  sleep 1
done

if kill -0 "${old_pid}" 2>/dev/null; then
  write_status "旧进程未按时退出，发送 SIGKILL"
  kill -KILL "${old_pid}"
fi

for child_pid in "${old_children[@]}"; do
  if kill -0 "${child_pid}" 2>/dev/null; then
    kill -TERM "${child_pid}" || true
  fi
done

if [[ -f /root/.weclaw/weclaw.pid ]] && [[ "$(</root/.weclaw/weclaw.pid)" == "${old_pid}" ]]; then
  rm -f /root/.weclaw/weclaw.pid
fi

systemctl --user daemon-reload
systemctl --user start "${service_name}"

for _ in $(seq 1 30); do
  if systemctl --user is-active --quiet "${service_name}" && curl --silent --fail --max-time 2 http://127.0.0.1:18011/health >/dev/null; then
    break
  fi
  sleep 1
done

if ! systemctl --user is-active --quiet "${service_name}"; then
  write_status "失败：systemd 服务未进入 active"
  exit 1
fi
if ! curl --silent --fail --max-time 2 http://127.0.0.1:18011/health >/dev/null; then
  write_status "失败：本地健康检查未通过"
  exit 1
fi

new_pid="$(systemctl --user show "${service_name}" --property=MainPID --value)"
version="$(/root/.local/bin/weclaw-codex-direct version)"
write_status "服务已恢复，新 PID=${new_pid}，版本=${version}"

account_file="$(find /root/.weclaw/accounts -maxdepth 1 -type f -name '*-im-bot.json' -print -quit)"
owner_user_id="$(jq -r '.ilink_user_id' "${account_file}")"
payload="$(jq -nc --arg to "${owner_user_id}" --arg text '微信桥接已完成会话管理升级。会话列表、切换、状态、归档与重启恢复已经启用，服务和主动发送链路正常。发送 /sessions 开始使用。' '{to:$to,text:$text}')"

curl --silent --fail --max-time 15 \
  --header 'Content-Type: application/json' \
  --data "${payload}" \
  http://127.0.0.1:18011/api/send >/dev/null

write_status "主动微信发送验证通过"
