package ilink

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// LogLabel 生成稳定但不可逆的短标签，日志不得直接写入微信账号标识。
func LogLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "id:none"
	}
	digest := sha256.Sum256([]byte(value))
	return "id:" + hex.EncodeToString(digest[:4])
}
