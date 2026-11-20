package awaken

import (
	"fmt"
	"go-client/global"
	"go-client/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// 命令桥接（CommandBridge）相关实现，只在 macOS 上使用。
//
// 背景：Tabby 这类终端的命令行不接受密码（quickConnect 只解析 user@host:port），
// 密码只能走系统凭据，于是每次连接都要弹一次 macOS 钥匙串授权、要求用户输电脑密码。
// 但它们能打开一个“本地终端”profile 执行任意命令，所以改成：
//   1. 把本次连接写成一个脚本：exec <内置 client> ssh <JMS-token 用户>@<host> -p <port> -P <密码>
//   2. 在终端配置里放一个固定的 local profile 指向这个脚本（只需安装一次）
//   3. 拉起终端时说 “profile <名字>”，tab 里跑的就是内置 client
// 认证因此完全由 client 完成，与 iTerm2 那条路径同构：不碰系统凭据、不弹任何框。

const bridgeProfileIcon = "fas fa-terminal"

// clientBinaryPath 返回内置 client 的绝对路径，与内置分支的算法保持一致。
func clientBinaryPath() (string, error) {
	currentPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return "", err
	}
	clientPath := filepath.Join(currentPath, "client")
	if _, err := os.Stat(clientPath); err != nil {
		return "", fmt.Errorf("builtin client not found at %s: %v", clientPath, err)
	}
	return clientPath, nil
}

// appConfigDir 返回客户端自己的配置目录（~/.config 或 ~/Library/Application Support 下的 jumpserver-client）。
func appConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jumpserver-client"), nil
}

// shellQuote 用单引号包裹一个参数，供生成的脚本使用。
// 单引号内除单引号本身外无特殊含义，单引号用 '"'"' 拼接。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// bridgeScript 生成桥接脚本内容：先 cd 到 home，再 exec 内置 client。
func bridgeScript(shell string, clientPath string, args []string) string {
	if shell == "" {
		shell = "/bin/sh"
	}
	var b strings.Builder
	b.WriteString("#!" + shell + "\n")
	b.WriteString("# 由 JumpServer Client 自动生成，每次连接都会覆盖，请勿手工修改。\n")
	b.WriteString("cd \"${HOME:-/}\" 2>/dev/null || true\n")
	b.WriteString("exec " + shellQuote(clientPath))
	for _, arg := range args {
		b.WriteString(" " + shellQuote(arg))
	}
	b.WriteString("\n")
	return b.String()
}

// writeFileAtomic 先写临时文件再改名，保证中途失败不会留下半截文件。
// 注意 macOS 15 起带 com.apple.provenance 的文件不允许被“别的进程/别的构建”改名覆盖
// （会报 operation not permitted），这种情况下退回「先删后改名」，再不行就原地写。
func writeFileAtomic(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bridge-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err == nil {
		if err := os.Rename(tmpName, path); err == nil {
			return nil
		}
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

// writeBridgeScript 原子写入脚本并赋予执行权限，返回脚本绝对路径。
func writeBridgeScript(scriptName string, content string) (string, error) {
	dir, err := appConfigDir()
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, scriptName)
	if err := writeFileAtomic(target, []byte(content), 0700); err != nil {
		return "", err
	}
	return target, nil
}

// expandHome 展开以 ~ 开头的路径。
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// bridgeProfileBlock 生成写进终端配置的 profile 片段（2 空格缩进的 YAML 列表项）。
func bridgeProfileBlock(bridge *config.CommandBridge, scriptPath string) string {
	shell := bridge.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	var b strings.Builder
	b.WriteString("  - id: " + bridge.ProfileID + "\n")
	b.WriteString("    name: " + bridge.ProfileName + "\n")
	b.WriteString("    type: local\n")
	b.WriteString("    options:\n")
	b.WriteString("      command: " + shell + "\n")
	b.WriteString("      args:\n")
	b.WriteString("        - " + shellQuote(scriptPath) + "\n")
	b.WriteString("    icon: " + bridgeProfileIcon + "\n")
	b.WriteString("    weight: 0.0\n")
	return b.String()
}

// insertProfile 把 profile 片段插进终端配置文本，返回新文本。
// 终端配置是 js-yaml 生成的，顶层键顶格、列表项 2 空格缩进，按这个约定做纯文本插入，
// 比引一个 YAML 库整份重排更安全（不会动用户其它配置）。
func insertProfile(content string, bridge *config.CommandBridge, block string) (string, bool) {
	if strings.Contains(content, "id: "+bridge.ProfileID) {
		return content, false
	}

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "profiles: []" {
			lines[i] = "profiles:"
			rest := append([]string{}, lines[i+1:]...)
			merged := append(append(lines[:i+1], strings.Split(strings.TrimSuffix(block, "\n"), "\n")...), rest...)
			return strings.Join(merged, "\n"), true
		}
		if trimmed != "profiles:" {
			continue
		}
		// 已有 profiles 列表：找到这个列表的结尾（下一行顶格非空行之前）。
		insertAt := len(lines)
		for j := i + 1; j < len(lines); j++ {
			candidate := lines[j]
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			if !strings.HasPrefix(candidate, " ") {
				insertAt = j
				break
			}
		}
		head := append([]string{}, lines[:insertAt]...)
		tail := append([]string{}, lines[insertAt:]...)
		merged := append(append(head, strings.Split(strings.TrimSuffix(block, "\n"), "\n")...), tail...)
		return strings.Join(merged, "\n"), true
	}

	// 没有 profiles 键：追加到文件末尾。
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + "profiles:\n" + block, true
}

// ensureBridgeProfile 确保终端配置里有我们的 profile（幂等）。
// 返回 installed 表示本次是否新写入；写入后会落一个 marker 文件，用来判断
// 正在运行的终端到底有没有读到这条配置。
func ensureBridgeProfile(bridge *config.CommandBridge, scriptPath string) (installed bool, err error) {
	configPath := expandHome(bridge.ConfigPath)
	content := ""
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		content = string(data)
	} else if !os.IsNotExist(readErr) {
		return false, readErr
	}

	block := bridgeProfileBlock(bridge, scriptPath)
	next, changed := insertProfile(content, bridge, block)
	if !changed {
		return false, nil
	}

	// 改别人的配置文件之前先留一份，出问题能手工恢复。
	if content != "" {
		if backupErr := os.WriteFile(markerBackupPath(), []byte(content), 0600); backupErr != nil && global.LOG != nil {
			global.LOG.Warn("backup terminal config failed", zap.String("path", configPath), zap.Error(backupErr))
		}
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(configPath, []byte(next), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// markerBackupPath 是终端配置的备份位置（放在客户端自己的配置目录里，不污染用户目录）。
func markerBackupPath() string {
	dir, err := appConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tabby-config-backup.yaml")
	}
	return filepath.Join(dir, "terminal-config-backup.yaml")
}

// markerPath 记录 profile 安装时间，用来判断运行中的终端是否已加载它。
func markerPath(bridge *config.CommandBridge) string {
	dir, err := appConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "."+bridge.ProfileID+".installed")
}

// touchMarker 更新/创建安装标记。
func touchMarker(bridge *config.CommandBridge) {
	path := markerPath(bridge)
	if path == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	if _, err := os.Stat(path); err != nil {
		_ = os.WriteFile(path, []byte(now.Format(time.RFC3339)), 0600)
	}
}

// markerAge 返回安装标记距今多久；没有标记时返回 -1。
func markerAge(bridge *config.CommandBridge) time.Duration {
	path := markerPath(bridge)
	if path == "" {
		return -1
	}
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return time.Since(info.ModTime())
}

// processUptime 返回命令行匹配 pattern 的最早一个进程已运行的时长；没有匹配进程时 ok=false。
func processUptime(pattern string) (time.Duration, bool) {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return 0, false
	}
	var oldest time.Duration
	found := false
	for _, field := range strings.Fields(string(out)) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue
		}
		etimeOut, etimeErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "etime=").Output()
		if etimeErr != nil {
			continue
		}
		d, parseOK := parseElapsed(strings.TrimSpace(string(etimeOut)))
		if !parseOK {
			continue
		}
		if !found || d > oldest {
			oldest = d
			found = true
		}
	}
	return oldest, found
}

// parseElapsed 解析 ps 的 etime 格式：[[dd-]hh:]mm:ss。
func parseElapsed(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	var days int
	if idx := strings.Index(value, "-"); idx >= 0 {
		parsed, err := strconv.Atoi(value[:idx])
		if err != nil {
			return 0, false
		}
		days = parsed
		value = value[idx+1:]
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	nums := make([]int, len(parts))
	for i, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		nums[i] = parsed
	}
	var hours, minutes, seconds int
	if len(nums) == 3 {
		hours, minutes, seconds = nums[0], nums[1], nums[2]
	} else {
		minutes, seconds = nums[0], nums[1]
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second, true
}

// terminalLoadedProfile 判断正在运行的终端是否已经读到我们写进去的 profile。
// 终端只在启动时读取配置（没有文件监听），所以：
//   - 终端没在运行 → 这次冷启动一定会读到，可以用 profile；
//   - 终端在运行 → 只有它的启动时刻晚于 profile 安装时刻，才说明它读到了。
func terminalLoadedProfile(appPath string, bridge *config.CommandBridge) bool {
	age := markerAge(bridge)
	if age < 0 {
		return false
	}
	uptime, running := processUptime(appPath)
	if !running {
		return true
	}
	return uptime <= age
}

// bridgeCommand 组装“让终端跑桥接脚本”的启动命令。
func bridgeCommand(appItem *config.AppItem, connectMap map[string]string) (*exec.Cmd, error) {
	bridge := appItem.Bridge
	if bridge == nil {
		return nil, fmt.Errorf("bridge config is missing")
	}

	clientPath, err := clientBinaryPath()
	if err != nil {
		return nil, err
	}
	args := splitArgs(getCommandFromArgs(connectMap, bridge.ArgsTemplate))
	if len(args) == 0 {
		return nil, fmt.Errorf("%s bridge args template is empty, check the plugin's bridge.args_template", strings.ToUpper(appItem.Name))
	}

	scriptPath, err := writeBridgeScript(bridge.ScriptName, bridgeScript(bridge.Shell, clientPath, args))
	if err != nil {
		return nil, fmt.Errorf("write bridge script failed: %v", err)
	}

	installed, profileErr := ensureBridgeProfile(bridge, scriptPath)
	if profileErr != nil && global.LOG != nil {
		global.LOG.Warn("install terminal profile failed",
			zap.String("app", appItem.Name),
			zap.String("config", bridge.ConfigPath),
			zap.Error(profileErr),
		)
	}
	if profileErr == nil {
		if installed {
			touchMarker(bridge)
		} else if markerAge(bridge) < 0 {
			// profile 之前就在，但没有 marker：按“刚安装”处理，宁可多一次确认框，
			// 也不要让终端报 profile not found。
			touchMarker(bridge)
		}
	}

	useProfile := profileErr == nil && terminalLoadedProfile(appItem.Path, bridge)
	if global.LOG != nil {
		global.LOG.Debug("launch bridge client",
			zap.String("protocol", appItem.Name),
			zap.String("path", appItem.Path),
			zap.String("script", scriptPath),
			zap.Bool("profile_ready", useProfile),
			zap.Strings("args", args),
		)
	}

	if useProfile {
		return exec.Command(appItem.Path, "profile", bridge.ProfileName), nil
	}
	if !bridge.RunFallback {
		return nil, fmt.Errorf("terminal has not loaded the %q profile yet, please restart %s once", bridge.ProfileName, appItem.DisplayName)
	}
	// 终端没读到新 profile（通常是它正在运行、不会重读配置）：退回 cli run <脚本>。
	// 这条能用，但终端会弹一次“是否运行”的确认框。
	if global.LOG != nil {
		global.LOG.Warn("terminal profile not loaded yet, falling back to run (a confirmation dialog will appear once)",
			zap.String("app", appItem.DisplayName),
			zap.String("profile", bridge.ProfileName),
		)
	}
	return exec.Command(appItem.Path, "run", scriptPath), nil
}
