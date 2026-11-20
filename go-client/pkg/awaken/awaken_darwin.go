package awaken

import (
	"fmt"
	"go-client/global"
	"go-client/pkg/config"
	"go.uber.org/zap"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func getCommandFromArgs(connectInfo map[string]string, argFormat string) string {
	for key, value := range connectInfo {
		argFormat = strings.Replace(argFormat, "{"+key+"}", value, 1)
	}
	return argFormat
}

// splitArgs 按空格拆分参数模板，并丢掉空片段。
// 模板里多一个空格（例如结尾空格）时，strings.Split 会产生空参数；
// 部分客户端（Tabby 的 quickConnect 需要两个位置参数）会把空参数算成一个位置参数，
// 轻则参数错位，重则直接崩在 CLI 解析里，窗口都不出来。
func splitArgs(commands string) []string {
	args := make([]string, 0, 8)
	for _, token := range strings.Split(commands, " ") {
		if token != "" {
			args = append(args, token)
		}
	}
	return args
}

// externalAppCommand 组装第三方（非内置）客户端的启动命令，同时把最终的 argv 写进日志，
// 便于事后定位“拉起了客户端但没有打开会话”这类问题。
func externalAppCommand(appLabel string, appPath string, argFormat string, connectInfo map[string]string) (*exec.Cmd, error) {
	commands := getCommandFromArgs(connectInfo, argFormat)
	args := splitArgs(commands)
	if len(args) == 0 {
		return nil, fmt.Errorf("%s application argument template is empty, check the plugin's launch.template", strings.ToUpper(appLabel))
	}
	if global.LOG != nil {
		global.LOG.Debug("launch external client",
			zap.String("protocol", appLabel),
			zap.String("path", appPath),
			zap.Strings("args", args),
		)
	}
	return exec.Command(appPath, args...), nil
}

// validateAppPath checks if the application path exists
func validateAppPath(appPath string) error {
	if appPath == "" {
		return fmt.Errorf("application path is empty")
	}
	// Check if path exists
	if _, err := os.Stat(appPath); os.IsNotExist(err) {
		return fmt.Errorf("application path does not exist: %s", appPath)
	}
	return nil
}

// keychainTrustTargets 把可执行文件路径换算成需要授予钥匙串访问权限的代码对象。
// 传 /Applications/Tabby.app/Contents/MacOS/Tabby 时，同时信任 .app 包本身：
// 钥匙串的访问控制是按代码签名比对的，两种路径落到同一个签名上，多写一条更稳。
func keychainTrustTargets(appPath string) []string {
	targets := make([]string, 0, 2)
	if idx := strings.Index(appPath, ".app/"); idx > 0 {
		targets = append(targets, appPath[:idx+len(".app")])
	}
	targets = append(targets, appPath)
	existed := targets[:0]
	for _, target := range targets {
		if _, err := os.Stat(target); err == nil {
			existed = append(existed, target)
		}
	}
	return existed
}

// seedKeychainPassword 按插件声明的 password_store，把本次连接的密码预置到 macOS 钥匙串。
// 基于 keytar 的客户端（Tabby 等）启动时会用 service/account 去读密码，
// 命中后直接走“已保存密码”认证，从而避免再弹一次密码框。
// 返回的 cleanup 会在客户端退出后删掉这条临时凭据，防止每个连接都往钥匙串里留一条。
// 失败不阻断启动：客户端仍会自己弹框，用户手工输一次即可。
func seedKeychainPassword(appItem *config.AppItem, connectInfo map[string]string, password string) (func(), error) {
	store := appItem.PasswordStore
	if store == nil || store.Type != "macos-keychain" {
		return nil, nil
	}
	service := strings.TrimSpace(getCommandFromArgs(connectInfo, store.Service))
	account := strings.TrimSpace(getCommandFromArgs(connectInfo, store.Account))
	if service == "" || account == "" {
		return nil, fmt.Errorf("password_store template is incomplete (service=%q, account=%q)", service, account)
	}
	if password == "" {
		return nil, fmt.Errorf("password is empty, skip keychain seeding")
	}

	// -U 覆盖已存在条目，避免上一轮残留的旧密码把新连接顶掉。
	// 密码只能走 argv：security 的 -w 省略取值时会从 /dev/tty 交互读取，
	// 客户端是后台拉起的、没有终端，只会卡住。
	args := []string{"add-generic-password", "-U", "-s", service, "-a", account}
	for _, target := range keychainTrustTargets(appItem.Path) {
		args = append(args, "-T", target)
	}
	args = append(args, "-w", password)

	out, err := exec.Command("/usr/bin/security", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("security add-generic-password failed (service=%q, account=%q): %v: %s",
			service, account, err, strings.TrimSpace(string(out)))
	}

	cleanup := func() {
		// 稍等一下再删：刚启动的客户端可能还没把密码读走。
		// 认证失败时客户端自己也会删，这里删不到只会返回非 0，忽略即可。
		time.Sleep(8 * time.Second)
		_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", account).Run()
	}
	return cleanup, nil
}

func awakenRDPCommand(filePath string, cfg *config.AppConfig) (*exec.Cmd, error) {
	global.LOG.Debug(filePath)
	cmd := exec.Command("open", filePath)
	return cmd, nil
}

func awakenVNCCommand(r *Rouse, cfg *config.AppConfig) (*exec.Cmd, error) {
	var appItem *config.AppItem
	appLst := cfg.MacOS.RemoteDesktop
	for _, app := range appLst {
		if app.IsActive() && app.IsMatchProtocol("vnc") {
			appItem = &app
			break
		}
	}
	if appItem == nil {
		return nil, nil
	}
	connectMap := map[string]string{
		"name":     r.getName(),
		"protocol": r.Protocol,
		"username": r.getUserName(),
		"value":    r.Value,
		"host":     r.Host,
		"port":     strconv.Itoa(r.Port),
	}
	if !appItem.IsInternal {
		if err := validateAppPath(appItem.Path); err != nil {
			return nil, fmt.Errorf("No VNC application configured or found (selected path: %s, reason: %v)", appItem.Path, err)
		}
	}
	cmd, buildErr := externalAppCommand("vnc", appItem.Path, appItem.ArgFormat, connectMap)
	if buildErr != nil {
		return nil, buildErr
	}
	// 设置环境变量（只对这个子进程有效）
	cmd.Env = append(os.Environ(),
		"VNC_USERNAME="+r.getUserName(),
		"VNC_PASSWORD="+r.Value,
	)
	return cmd, nil
}

func awakenSSHCommand(r *Rouse, cfg *config.AppConfig) (*exec.Cmd, error) {
	var appItem *config.AppItem
	var appLst []config.AppItem
	switch r.Protocol {
	case "ssh", "telnet":
		appLst = cfg.MacOS.Terminal
	case "sftp":
		appLst = append(cfg.MacOS.FileTransfer, cfg.MacOS.Terminal...)
	}

	for _, app := range appLst {
		if app.IsActive() && app.IsMatchProtocol(r.Protocol) {
			appItem = &app
			break
		}
	}
	if appItem == nil {
		return nil, nil
	}

	// telnet 协议使用 ssh 的配置参数格式
	protocol := r.Protocol
	if protocol == "telnet" {
		protocol = "ssh"
	}

	var cmd *exec.Cmd
	connectMap := map[string]string{
		"name":     r.getName(),
		"protocol": protocol,
		"username": r.getUserName(),
		"value":    r.Value,
		"host":     r.Host,
		"port":     strconv.Itoa(r.Port),
	}

	if appItem.IsInternal {
		currentPath, _ := filepath.Abs(filepath.Dir(os.Args[0]))
		commands := getCommandFromArgs(connectMap, appItem.ArgFormat)
		clientPath := filepath.Join(currentPath, "client")
		if appItem.Name == "iterm" {
			itermCmd := fmt.Sprintf(`%s %s`, clientPath, commands)

			scriptPath := filepath.Join(currentPath, "Scripts", "iterm2_loader.scpt")
			//command = fmt.Sprintf(`%s "%s"`, scriptPath, command)
			cmd = exec.Command("osascript", "-s", "h", scriptPath, itermCmd, "0")
		} else {
			cmd = exec.Command(
				"osascript", "-s", "h", "-e", fmt.Sprintf(`tell application "%s" to do script "%s %s" activate`,
					appItem.DisplayName, clientPath, commands),
			)
		}

	} else {
		appPath := appItem.Path
		if !appItem.IsInternal {
			if err := validateAppPath(appItem.Path); err != nil {
				return nil, fmt.Errorf("No %s application configured or found (selected path: %s, reason: %v)", strings.ToUpper(r.Protocol), appItem.Path, err)
			}
		}
		var err error
		cmd, err = externalAppCommand(r.Protocol, appPath, appItem.ArgFormat, connectMap)
		if err != nil {
			return nil, err
		}
		// 第三方客户端命令行不支持传密码，按插件声明把密码预置进钥匙串，
		// 客户端启动后自己取用，用户就不用再手工输一次。
		cleanup, seedErr := seedKeychainPassword(appItem, connectMap, r.Value)
		if seedErr != nil {
			if global.LOG != nil {
				global.LOG.Warn("seed keychain password failed",
					zap.String("app", appItem.Name),
					zap.Error(seedErr),
				)
			}
		}
		registerCleanup(cleanup)
	}
	return cmd, nil
}

func awakenDBCommand(r *Rouse, cfg *config.AppConfig) (*exec.Cmd, error) {
	var appItem *config.AppItem
	appLst := cfg.MacOS.Databases
	for _, app := range appLst {
		if app.IsActive() && app.IsMatchProtocol(r.Protocol) {
			appItem = &app
			break
		}
	}
	if appItem == nil {
		return nil, nil
	}
	appPath := appItem.Path
	connectMap := map[string]string{
		"name":     r.getName(),
		"protocol": r.Protocol,
		"username": r.getUserName(),
		"value":    r.Value,
		"host":     r.Host,
		"port":     strconv.Itoa(r.Port),
		"dbname":   r.DBName,
	}
	if r.Protocol == "oracle" {
		connectMap["dbname"] = r.getUserName()
	}
	if appItem.Name == "dbx" {
		switch r.Protocol {
		case "postgresql":
			connectMap["protocol"] = "postgres"
		case "sqlserver":
			connectMap["protocol"] = "mssql"
		default:
			connectMap["protocol"] = r.Protocol
		}
	}
	if appItem.IsInternal {
		var argFormat string
		switch r.Protocol {
		case "redis":
			argFormat = "redis-cli -h {host} -p {port} -a {username}@{value}"
		case "oracle":
			argFormat = "sqlplus {username}/{value}@{host}:{port}/{dbname}"
		case "postgresql":
			argFormat = "psql 'user={username} password={value} host={host} dbname={dbname} port={port}'"
		case "mysql", "mariadb":
			argFormat = "mysql -u {username} -p{value} -h {host} -P {port} {dbname}"
		case "sqlserver":
			argFormat = "sqlcmd -S {host},{port} -U {username} -P {value} -d {dbname}"
		case "mongodb":
			argFormat = "mongosh 'mongodb://{username}:{value}@{host}:{port}/{dbname}?authSource=admin'"
		}
		commands := getCommandFromArgs(connectMap, argFormat)
		cmd := exec.Command(
			"osascript", "-s", "h", "-e", fmt.Sprintf(`tell application "%s" to do script "%s" activate`,
				appItem.DisplayName, commands),
		)
		return cmd, nil
	} else {
		if !appItem.IsInternal {
			if err := validateAppPath(appItem.Path); err != nil {
				return nil, fmt.Errorf("No database application configured or found (selected path: %s, reason: %v)", appItem.Path, err)
			}
		}
		if r.Protocol == "sqlserver" && appItem.Name != "dbx" {
			connectMap["protocol"] = "mssql_jdbc_ms_new"
		}
		return externalAppCommand(r.Protocol, appPath, appItem.ArgFormat, connectMap)
	}
}

func awakenOtherCommand(r *Rouse, cfg *config.AppConfig) (*exec.Cmd, error) {
	var command string
	if r.Protocol == "ssh" {
		currentPath := filepath.Dir(os.Args[0])
		clientPath := filepath.Join(currentPath, "client")
		command = fmt.Sprintf("%s %s -P %s", clientPath, r.Command, r.Value)
	} else {
		command = r.Command
	}
	cmd := exec.Command(
		"osascript", "-s", "h",
		"-e", fmt.Sprintf(`tell application "%s" to do script "%s"`, "Terminal", command),
	)
	return cmd, nil
}
