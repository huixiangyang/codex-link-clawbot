package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const githubRepo = "huixiangyang/weclaw"

const (
	maxReleaseBinaryBytes = 100 << 20
	maxChecksumBytes      = 1 << 20
)

var updateHTTPClient = &http.Client{Timeout: 2 * time.Minute}

func init() {
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(upgradeCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the current version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("weclaw %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update weclaw to the latest version and restart",
	RunE:  runUpdate,
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Update weclaw to the latest version and restart (alias for update)",
	RunE:  runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("self-update only supports linux/amd64 and linux/arm64")
	}

	// 1. Get latest version
	fmt.Println("Checking for updates...")
	latest, err := getLatestVersion()
	if err != nil {
		return fmt.Errorf("failed to check latest version: %w", err)
	}

	if latest == Version {
		fmt.Printf("Already up to date (%s)\n", Version)
		return nil
	}

	fmt.Printf("Current: %s -> Latest: %s\n", Version, latest)

	// 2. 同批下载主程序、SILK 编码器与校验清单，避免只升级一半。
	mainFilename := fmt.Sprintf("weclaw_linux_%s", runtime.GOARCH)
	silkFilename := fmt.Sprintf("weclaw_silk_encoder_linux_%s", runtime.GOARCH)
	releaseBaseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", githubRepo, latest)

	checksumFile, err := downloadFile(releaseBaseURL+"/checksums.txt", maxChecksumBytes)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	defer os.Remove(checksumFile)
	checksums, err := os.ReadFile(checksumFile)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}

	fmt.Printf("Downloading %s and %s...\n", mainFilename, silkFilename)
	mainFile, err := downloadFile(releaseBaseURL+"/"+mainFilename, maxReleaseBinaryBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", mainFilename, err)
	}
	defer os.Remove(mainFile)
	silkFile, err := downloadFile(releaseBaseURL+"/"+silkFilename, maxReleaseBinaryBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", silkFilename, err)
	}
	defer os.Remove(silkFile)
	if err := verifyReleaseChecksum(mainFile, mainFilename, checksums); err != nil {
		return err
	}
	if err := verifyReleaseChecksum(silkFile, silkFilename, checksums); err != nil {
		return err
	}

	// 3. 先替换编码器，再替换主程序；主程序永远不会指向缺失的新版编码器。
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	// Resolve symlinks
	if resolved, err := resolveSymlink(exePath); err == nil {
		exePath = resolved
	}
	silkPath := filepath.Join(filepath.Dir(exePath), "weclaw-silk-encoder")

	if err := replaceBinary(silkFile, silkPath); err != nil {
		return fmt.Errorf("replace SILK encoder: %w", err)
	}
	if err := replaceBinary(mainFile, exePath); err != nil {
		return fmt.Errorf("replace main binary: %w", err)
	}

	fmt.Printf("Updated to %s\n", latest)

	// 4. Restart if running in background
	pid, pidErr := readPid()
	if pidErr == nil && processExists(pid) {
		fmt.Println("Stopping old process...")
		if p, err := os.FindProcess(pid); err == nil {
			p.Signal(os.Interrupt)
		}
		// Wait for old process to exit
		for i := 0; i < 20; i++ {
			if !processExists(pid) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		os.Remove(pidFile())

		fmt.Println("Starting new version...")
		if err := runDaemon(); err != nil {
			log.Printf("Failed to restart: %v", err)
			fmt.Println("Update complete. Please run 'weclaw start' manually.")
		}
	} else {
		fmt.Println("Update complete. Run 'weclaw start' to start.")
	}

	return nil
}

func getLatestVersion() (string, error) {
	resp, err := updateHTTPClient.Get(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	return release.TagName, nil
}

func downloadFile(url string, maxBytes int64) (string, error) {
	resp, err := updateHTTPClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "weclaw-update-*")
	if err != nil {
		return "", err
	}

	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if written > maxBytes {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}

	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}

	return tmp.Name(), nil
}

func verifyReleaseChecksum(path, filename string, manifest []byte) error {
	want := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		want = strings.ToLower(fields[0])
		break
	}
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("checksum for %s is missing or invalid", filename)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s for checksum: %w", filename, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash %s: %w", filename, err)
	}
	got := fmt.Sprintf("%x", hash.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s", filename)
	}
	return nil
}

func replaceBinary(src, dst string) error {
	// Check if we can write directly
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Try with sudo on Unix
	if runtime.GOOS != "windows" {
		fmt.Printf("Installing to %s (requires sudo)...\n", dst)
		cmd := exec.Command("sudo", "cp", src, dst)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("cannot write to %s", dst)
}

func resolveSymlink(path string) (string, error) {
	for {
		target, err := os.Readlink(path)
		if err != nil {
			return path, nil
		}
		if !strings.HasPrefix(target, "/") {
			// Relative symlink
			dir := path[:strings.LastIndex(path, "/")+1]
			target = dir + target
		}
		path = target
	}
}
