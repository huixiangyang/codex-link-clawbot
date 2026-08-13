#!/bin/sh
set -eu

VOICE_NAME="${1:-zh_CN-huayan-medium}"
PIPER_ROOT="${CODEX_LINK_CLAWBOT_PIPER_ROOT:-${HOME}/.codex-link-clawbot/tts/piper}"
VENV_DIR="${PIPER_ROOT}/venv"
VOICE_DIR="${PIPER_ROOT}/voices"

if ! command -v uv >/dev/null 2>&1; then
  echo "缺少 uv，无法创建隔离的 Piper 运行环境。" >&2
  exit 1
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
	echo "缺少 ffmpeg，无法生成微信 MP3 音频。" >&2
  exit 1
fi

# Piper 及模型只写入 codex-link-clawbot 私有目录，不污染系统 Python。
install -d -m 700 "${PIPER_ROOT}" "${VOICE_DIR}"
uv venv --allow-existing "${VENV_DIR}"
uv pip install --python "${VENV_DIR}/bin/python" "piper-tts==1.4.1" "pathvalidate==3.3.1"
"${VENV_DIR}/bin/python" -m piper.download_voices --force-redownload --download-dir "${VOICE_DIR}" "${VOICE_NAME}"

MODEL_PATH="${VOICE_DIR}/${VOICE_NAME}.onnx"
CONFIG_PATH="${MODEL_PATH}.json"
if [ ! -s "${MODEL_PATH}" ] || [ ! -s "${CONFIG_PATH}" ]; then
  echo "Piper 模型下载不完整。" >&2
  exit 1
fi
chmod 600 "${MODEL_PATH}" "${CONFIG_PATH}"

echo "Piper 已安装。"
echo "ffmpeg_command=$(command -v ffmpeg)"
echo "command=${VENV_DIR}/bin/piper"
echo "model=${MODEL_PATH}"
echo "model_config=${CONFIG_PATH}"
