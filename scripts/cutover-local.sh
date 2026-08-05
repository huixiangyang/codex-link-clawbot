#!/usr/bin/env bash

set -euo pipefail
umask 077

expected_version="${1:?必须传入期望版本，例如 v2.3.2-audio.1}"
ready_message="${2:-WeClaw ${expected_version} 已启动，可以继续使用。}"
service_name="${WECLAW_SERVICE_NAME:-weclaw.service}"
api_base="${WECLAW_API_URL:-http://127.0.0.1:18011}"
binary_path="${WECLAW_BINARY:-$(command -v weclaw)}"
account_name_pattern="${WECLAW_ACCOUNT_PATTERN:-*-im-bot.json}"

user_home_dir="$(getent passwd "$(id -u)" | cut -d: -f6)"
if [[ -z "${user_home_dir}" ]]; then
  printf '无法解析当前用户主目录\n' >&2
  exit 1
fi
state_dir="${WECLAW_STATE_DIR:-${user_home_dir}/.weclaw}"
status_file="${state_dir}/cutover-status.log"

mkdir -p "${state_dir}"
chmod 0700 "${state_dir}"

write_status() {
  printf '%s %s\n' "$(date --iso-8601=seconds)" "$1" >>"${status_file}"
}

write_status "开始切换，期望版本=${expected_version}，服务=${service_name}"
systemctl --user restart "${service_name}"

ready=0
for _ in $(seq 1 30); do
  if systemctl --user is-active --quiet "${service_name}" && curl --silent --fail --max-time 2 "${api_base}/health" >/dev/null; then
    ready=1
    break
  fi
  sleep 1
done

if [[ "${ready}" -ne 1 ]]; then
  write_status "失败：服务或健康检查未在 30 秒内恢复"
  exit 1
fi

actual_version="$("${binary_path}" version)"
if [[ "${actual_version}" != "weclaw ${expected_version} ("* ]]; then
  write_status "失败：实际版本=${actual_version}"
  exit 1
fi

mapfile -t account_files < <(find "${state_dir}/accounts" -maxdepth 1 -type f -name "${account_name_pattern}" -print | sort)
if [[ "${#account_files[@]}" -eq 0 ]]; then
  write_status "失败：没有可用的微信账号凭据"
  exit 1
fi
owner_user_id="$(jq -er '.ilink_user_id | select(type == "string" and length > 0)' "${account_files[0]}")"
payload="$(jq -nc --arg to "${owner_user_id}" --arg text "${ready_message}" '{to:$to,text:$text}')"

curl --silent --fail --max-time 15 \
  --header 'Content-Type: application/json' \
  --data-binary "${payload}" \
  "${api_base}/api/send" >/dev/null

main_pid="$(systemctl --user show "${service_name}" --property=MainPID --value)"
write_status "切换完成，新 PID=${main_pid}，版本=${actual_version}，微信通知成功"
