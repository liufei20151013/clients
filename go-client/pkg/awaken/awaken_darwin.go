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
		if appItem.Bridge != nil {
			// 命令桥接：让终端开一个 profile 跑桥接脚本，脚本里 exec 内置 client，
			// 认证交给 client，不经过终端自身的凭据存储，也就不会弹电脑密码。
			cmd, err = bridgeCommand(appItem, connectMap)
		} else {
			cmd, err = externalAppCommand(r.Protocol, appPath, appItem.ArgFormat, connectMap)
		}
		if err != nil {
			return nil, err
		}
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
