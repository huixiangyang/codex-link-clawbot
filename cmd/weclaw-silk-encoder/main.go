// weclaw-silk-encoder 将 stdin 中的 16 kHz 单声道 s16le PCM 编码到 stdout。
// 独立进程用于隔离上游 SILK 实现中的不安全指针，避免编码器故障拖垮主服务。
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	silk "github.com/wdvxdr1123/go-silk"
)

const (
	sampleRate = 16000
	bitRate    = 25000
	frameBytes = sampleRate * 2 * 20 / 1000
	maxPCM     = sampleRate * 2 * 300
)

func main() {
	if len(os.Args) != 1 {
		fail("不接受命令行参数")
	}
	pcm, err := io.ReadAll(io.LimitReader(os.Stdin, maxPCM+1))
	if err != nil {
		fail("读取 PCM: %v", err)
	}
	if len(pcm) == 0 || len(pcm) > maxPCM || len(pcm)%frameBytes != 0 {
		fail("PCM 必须为不超过 5 分钟的完整 20ms 帧")
	}
	encoded, err := silk.EncodePcmBuffToSilk(pcm, sampleRate, bitRate, true)
	if err != nil {
		fail("编码 SILK: %v", err)
	}
	if len(encoded) <= 10 || !bytes.Equal(encoded[:10], []byte("\x02#!SILK_V3")) {
		fail("编码器返回无效的腾讯 SILK V3")
	}
	if _, err := os.Stdout.Write(encoded); err != nil {
		fail("写入 SILK: %v", err)
	}
}

func fail(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
