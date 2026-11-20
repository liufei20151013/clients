package config

import (
	"encoding/json"
	"go-client/global"
	"os"
	"path/filepath"
)

type AppConfig struct {
	FileName string  `json:"filename"`
	Windows  AppType `json:"windows"`
	MacOS    AppType `json:"macos"`
	Linux    AppType `json:"linux"`
}

type AppType struct {
	Terminal      []AppItem `json:"terminal"`
	FileTransfer  []AppItem `json:"filetransfer"`
	RemoteDesktop []AppItem `json:"remotedesktop"`
	Databases     []AppItem `json:"databases"`
}

type AppItem struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name"`
	Protocol    []string        `json:"protocol"`
	Type        string          `json:"type"`
	MatchFirst  []string        `json:"match_first"`
	Path        string          `json:"path"`
	ArgFormat   string          `json:"arg_format"`
	AutoIt      []AutoItCommand `json:"autoit"`
	IsInternal  bool            `json:"is_internal"`
	IsDefault   bool            `json:"is_default"`
	IsSet       bool            `json:"is_set"`
	// Bridge 非空表示这个客户端走“命令桥接”代填账号密码，见 CommandBridge。
	Bridge *CommandBridge `json:"bridge,omitempty"`
}

// CommandBridge 描述一种让第三方终端也能自动登录的办法。
//
// 这类终端（Tabby 就是典型）的命令行不接受密码，密码只能落在系统凭据里，
// 于是每次连接都会弹一次 macOS 钥匙串授权、要用户输电脑密码。
// 但它们能打开一个“本地终端”profile 去执行任意命令，所以改成：
// 客户端把本次连接写成一个小脚本（内部 exec 内置 client，密码用 -P 传），
// 终端里放一个固定的 local profile 指向这个脚本，拉起时只说“打开这个 profile”。
// 认证完全由内置 client 完成，和 iTerm2 那条路径同构：不进系统凭据、不弹框。
type CommandBridge struct {
	// ProfileName：终端里那个固定 profile 的名字，拉起时用 `cli profile <ProfileName>`。
	ProfileName string `json:"profile_name"`
	// ProfileID：写进终端配置里的 profile id，用来判断是否已安装、避免重复插入。
	ProfileID string `json:"profile_id"`
	// ScriptName：桥接脚本的文件名，实际落在客户端自己的配置目录下。
	ScriptName string `json:"script_name"`
	// ArgsTemplate：传给内置 client 的参数模板，占位符与 launch.template 一致。
	// 只写参数，不带程序名（程序名固定是内置 client，路径运行时才知道）。
	ArgsTemplate string `json:"args_template"`
	// ConfigPath：终端自己配置文件的路径，允许 ~ 前缀。
	ConfigPath string `json:"config_path"`
	// Shell：执行脚本的解释器，默认 /bin/sh。
	Shell string `json:"shell"`
	// RunFallback：终端还没读到新 profile 时（比如它正在运行、不会重读配置），
	// 是否退回 `cli run <脚本>`。这条能用，但终端会弹一次运行确认框。
	RunFallback bool `json:"run_fallback"`
}

type AutoItCommand struct {
	Cmd     string `json:"cmd"`
	Type    string `json:"type"`
	Element string `json:"element"`
}

func (a *AppItem) IsActive() bool {
	if a.IsSet {
		return true
	}
	return false
}

func (a *AppItem) IsSupportProtocol(protocol string) bool {
	for _, p := range a.Protocol {
		if p == protocol {
			return true
		}
	}
	return false
}

func (a *AppItem) IsMatchProtocol(protocol string) bool {
	for _, p := range a.MatchFirst {
		if p == protocol {
			return true
		}
	}
	return false
}

func GetConf() AppConfig {
	if GlobalConfig == nil {
		return getDefaultConfig()
	}
	return *GlobalConfig
}

var GlobalConfig *AppConfig

func getDefaultConfig() AppConfig {
	dir, _ := os.UserConfigDir()
	configDir := filepath.Join(dir, "jumpserver-client")

	// Plugin mode: build AppConfig from the current platform plugin directory.
	if pluginCfg, ok := loadPluginConfig(configDir); ok {
		GlobalConfig = pluginCfg
		return *GlobalConfig
	}

	filePath := filepath.Join(configDir, "config.json")
	jsonFile, err := os.Open(filePath)
	if err != nil {
		global.LOG.Error(err.Error())
		return AppConfig{}
	}
	defer func(jsonFile *os.File) {
		err := jsonFile.Close()
		if err != nil {
			global.LOG.Error(err.Error())
		}
	}(jsonFile)
	decoder := json.NewDecoder(jsonFile)
	err = decoder.Decode(&GlobalConfig)
	if err != nil {
		global.LOG.Error(err.Error())
		return AppConfig{}
	}
	return *GlobalConfig
}

// loadPluginConfig is implemented in pkg/plugin to avoid an import cycle.
var loadPluginConfig = func(configDir string) (*AppConfig, bool) {
	return nil, false
}

// SetPluginLoader registers the plugin config loader (called from plugin init).
func SetPluginLoader(loader func(string) (*AppConfig, bool)) {
	loadPluginConfig = loader
}
