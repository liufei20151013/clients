package plugin

import (
	"encoding/json"
	"go-client/global"
	"go-client/pkg/config"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"go.uber.org/zap"
)

const (
	categoryTerminal      = "terminal"
	categoryRemoteDesktop = "remotedesktop"
	categoryFileTransfer  = "filetransfer"
	categoryDatabases     = "databases"
)

type indexFile struct {
	Plugins []indexEntry `json:"plugins"`
}

type indexEntry struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Category stringList `json:"category"`
}

// stringList 兼容 "category": "terminal" 与 "category": ["terminal","filetransfer"] 两种写法。
// 一个插件可以同时属于多个分类（例如 electerm 既当 SSH 终端又当 SFTP 客户端）。
type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if single == "" {
			*s = nil
		} else {
			*s = stringList{single}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type pluginState struct {
	Version    int                        `json:"version"`
	Selections map[string]string          `json:"selections"`
	Plugins    map[string]pluginStateItem `json:"plugins"`
}

type pluginStateItem struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

type manifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Category    stringList        `json:"category"`
	Protocols   []string          `json:"protocols"`
	DownloadURL string            `json:"download_url"`
	Comment     map[string]string `json:"comment"`
}

type platformConnect struct {
	DisplayName      string                            `json:"display_name"`
	Executable       executableConfig                  `json:"executable"`
	Launch           map[string]interface{}            `json:"launch"`
	LaunchByCategory map[string]map[string]interface{} `json:"launch_by_category"`
	MatchFirst       []string                          `json:"match_first"`
	IsDefault        bool                              `json:"is_default"`
	IsSet            bool                              `json:"is_set"`
	IsInternal       bool                              `json:"is_internal"`
	Bridge           *config.CommandBridge             `json:"bridge"`
}

// launchForCategory 返回某个分类该用的启动定义。
//
// 单分类插件直接用 launch。多分类插件**必须**为每个分类在 launch_by_category 里
// 各写一份：同一款应用当终端和当文件传输工具时参数几乎必然不同（例如 electerm 的 -so
// 只在 SFTP 下合法），拿 launch 兜底等于这个分类必然连错，所以不兜底，直接判为配置错误。
func (c platformConnect) launchForCategory(category string, categories []string) (map[string]interface{}, bool) {
	if len(categories) > 1 {
		launch, ok := c.LaunchByCategory[category]
		if !ok || len(launch) == 0 {
			return nil, false
		}
		return launch, true
	}
	return c.Launch, len(c.Launch) > 0
}

type executableConfig struct {
	Type    string `json:"type"`
	Default string `json:"default"`
}

func osKey() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func resolveBuiltinDir() string {
	candidates := []string{}
	osName := osKey()

	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(base, "resources", "plugins", osName),
			filepath.Join(base, "..", "resources", "plugins", osName),
			filepath.Join(base, "..", "..", "plugins", osName),
			filepath.Join(base, "..", "..", "resources", "plugins", osName),
			filepath.Join(base, "plugins", osName),
			filepath.Join(base, "resources", "plugins", "builtin"),
			filepath.Join(base, "..", "resources", "plugins", "builtin"),
			filepath.Join(base, "..", "..", "plugins", "builtin"),
			filepath.Join(base, "..", "..", "resources", "plugins", "builtin"),
			filepath.Join(base, "plugins", "builtin"),
		)
	}

	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "plugins", osName),
			filepath.Join(cwd, "..", "plugins", osName),
			filepath.Join(cwd, "..", "..", "plugins", osName),
			filepath.Join(cwd, "plugins", "builtin"),
			filepath.Join(cwd, "..", "plugins", "builtin"),
			filepath.Join(cwd, "..", "..", "plugins", "builtin"),
		)
	}

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "index.json")); err == nil {
			return dir
		}
	}
	return ""
}

func resolveStatePath(configDir string) string {
	statePath := filepath.Join(configDir, "plugins-state.json")
	if _, err := os.Stat(statePath); err == nil {
		return statePath
	}

	osName := osKey()
	candidates := []string{
		filepath.Join("plugins", osName, "plugins-state.defaults.json"),
		filepath.Join("..", "plugins", osName, "plugins-state.defaults.json"),
		filepath.Join("..", "..", "plugins", osName, "plugins-state.defaults.json"),
		filepath.Join("plugins", "plugins-state.defaults.json"),
		filepath.Join("..", "plugins", "plugins-state.defaults.json"),
		filepath.Join("..", "..", "plugins", "plugins-state.defaults.json"),
	}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(base, "resources", "plugins", osName, "plugins-state.defaults.json"),
			filepath.Join(base, "..", "..", "plugins", osName, "plugins-state.defaults.json"),
			filepath.Join(base, "..", "..", "resources", "plugins", osName, "plugins-state.defaults.json"),
			filepath.Join(base, "resources", "plugins", "plugins-state.defaults.json"),
			filepath.Join(base, "..", "..", "plugins", "plugins-state.defaults.json"),
			filepath.Join(base, "..", "..", "resources", "plugins", "plugins-state.defaults.json"),
		)
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			_ = copyFile(path, statePath)
			return statePath
		}
	}
	return statePath
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func readJSON(path string, target interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readConnect(path, osName string) (platformConnect, bool, error) {
	var raw map[string]json.RawMessage
	if err := readJSON(path, &raw); err != nil {
		return platformConnect{}, false, err
	}

	if platformsRaw, ok := raw["platforms"]; ok {
		var platforms map[string]platformConnect
		if err := json.Unmarshal(platformsRaw, &platforms); err != nil {
			return platformConnect{}, false, err
		}
		connect, ok := platforms[osName]
		return connect, ok, nil
	}

	var connect platformConnect
	data, err := os.ReadFile(path)
	if err != nil {
		return platformConnect{}, false, err
	}
	if err := json.Unmarshal(data, &connect); err != nil {
		return platformConnect{}, false, err
	}
	return connect, true, nil
}

func sanitizeState(state *pluginState) bool {
	if state == nil || state.Selections == nil {
		return false
	}

	changed := false
	osName := osKey()
	for key, value := range state.Selections {
		if strings.HasPrefix(value, "builtin.") {
			state.Selections[key] = osName + strings.TrimPrefix(value, "builtin")
			changed = true
		}
	}
	for pluginID, item := range state.Plugins {
		if strings.HasPrefix(pluginID, "builtin.") {
			state.Plugins[osName+strings.TrimPrefix(pluginID, "builtin")] = item
			delete(state.Plugins, pluginID)
			changed = true
		}
	}

	if runtime.GOOS != "windows" {
		for _, key := range []string{"terminal:ssh", "terminal:telnet"} {
			if state.Selections[key] == osName+".putty" {
				state.Selections[key] = osName + ".terminal"
				changed = true
			}
		}
	}

	if runtime.GOOS == "linux" {
		switch state.Selections["remotedesktop:rdp"] {
		case "linux.mstsc", "linux.remmina":
			state.Selections["remotedesktop:rdp"] = "linux.xfreerdp"
			changed = true
		}
	}

	if state.Selections["filetransfer:sftp"] != osName+".iterm-sftp" {
		return changed
	}

	item, ok := state.Plugins[osName+".iterm-sftp"]
	if ok && (item.Enabled || strings.TrimSpace(item.Path) != "") {
		return changed
	}

	delete(state.Selections, "filetransfer:sftp")
	return true
}

func launchToArgFormat(launch map[string]interface{}) (string, []config.AutoItCommand) {
	launchType, _ := launch["type"].(string)
	switch launchType {
	case "autoit":
		raw, _ := json.Marshal(launch["steps"])
		var steps []config.AutoItCommand
		_ = json.Unmarshal(raw, &steps)
		return "", steps
	case "file":
		if template, ok := launch["arg_template"].(string); ok && template != "" {
			return template, nil
		}
		return "{file}", nil
	default:
		template, _ := launch["template"].(string)
		return template, nil
	}
}

func resolvePath(pluginID string, connect platformConnect, state pluginState) (string, bool, bool) {
	userPath := ""
	enabled := true
	hasUserPlugin := false
	if item, ok := state.Plugins[pluginID]; ok {
		hasUserPlugin = true
		userPath = strings.TrimSpace(item.Path)
		enabled = item.Enabled
	}

	path := connect.Executable.Default
	if userPath != "" {
		path = userPath
	}

	isSet := (connect.IsSet || userPath != "" || (connect.IsInternal && path != "" && hasUserPlugin)) && enabled
	return path, isSet, connect.IsInternal
}

func buildMatchFirst(pluginID, category string, protocols []string, selections map[string]string) []string {
	matched := []string{}
	for _, proto := range protocols {
		key := category + ":" + proto
		if selections[key] == pluginID {
			matched = append(matched, proto)
		}
	}
	return matched
}

func LoadAppConfig(configDir string) (*config.AppConfig, bool) {
	configPath := filepath.Join(configDir, "config.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, false
	}

	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false
	}

	pluginsMeta, ok := root["_plugins"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	enabled, _ := pluginsMeta["enabled"].(bool)
	if !enabled {
		return nil, false
	}

	builtinDir := resolveBuiltinDir()
	if builtinDir == "" {
		return nil, false
	}

	var index indexFile
	if err := readJSON(filepath.Join(builtinDir, "index.json"), &index); err != nil {
		return nil, false
	}

	statePath := resolveStatePath(configDir)
	var state pluginState
	_ = readJSON(statePath, &state)
	if state.Selections == nil {
		state.Selections = map[string]string{}
	}
	if state.Plugins == nil {
		state.Plugins = map[string]pluginStateItem{}
	}
	if sanitizeState(&state) {
		if data, err := json.MarshalIndent(state, "", "  "); err == nil {
			_ = os.WriteFile(statePath, data, 0644)
		}
	}

	osName := osKey()
	appType := config.AppType{}
	categoryItems := map[string]*[]config.AppItem{
		categoryTerminal:      &appType.Terminal,
		categoryRemoteDesktop: &appType.RemoteDesktop,
		categoryFileTransfer:  &appType.FileTransfer,
		categoryDatabases:     &appType.Databases,
	}

	for _, entry := range index.Plugins {
		pluginDir := filepath.Join(builtinDir, entry.ID)
		var manifest manifest

		if err := readJSON(filepath.Join(pluginDir, "manifest.json"), &manifest); err != nil {
			continue
		}
		platformConnect, ok, err := readConnect(filepath.Join(pluginDir, "connect.json"), osName)
		if err != nil || !ok || platformConnect.Executable.Type == "" {
			continue
		}

		displayName := platformConnect.DisplayName
		if displayName == "" {
			displayName = manifest.DisplayName
		}

		// 一个插件可以声明多个分类：每个分类生成一个 item，各自按自己的
		// selections 算 match_first，也各自用 launch_by_category 里的启动参数。
		// 多分类时该覆盖是必需的，缺了就说明打包者没想清楚这个分类该怎么启动，
		// 宁可让这个分类不注册（设置页里看不到它），也不要用另一套参数蒙过去。
		for _, category := range manifest.Category {
			categoryName := string(category)
			list, ok := categoryItems[categoryName]
			if !ok {
				continue
			}

			launch, ok := platformConnect.launchForCategory(categoryName, manifest.Category)
			if !ok {
				if global.LOG != nil {
					global.LOG.Error("multi-category plugin is missing launch_by_category for this category, not registering it",
						zap.String("plugin", entry.ID),
						zap.String("category", categoryName))
				}
				continue
			}

			argFormat, autoit := launchToArgFormat(launch)
			// 命令桥接的 profile 名只在 connect.json 里写一次，展示用的 arg_format 里替换掉占位符。
			if platformConnect.Bridge != nil && platformConnect.Bridge.ProfileName != "" {
				argFormat = strings.ReplaceAll(argFormat, "{bridge_profile}", platformConnect.Bridge.ProfileName)
			}
			matchFirst := buildMatchFirst(entry.ID, categoryName, manifest.Protocols, state.Selections)
			path, isSet, isInternal := resolvePath(entry.ID, platformConnect, state)
			if len(matchFirst) > 0 {
				isSet = true
			}

			*list = append(*list, config.AppItem{
				Name:        manifest.Name,
				DisplayName: displayName,
				Protocol:    manifest.Protocols,
				Type:        categoryName,
				MatchFirst:  matchFirst,
				Path:        path,
				ArgFormat:   argFormat,
				AutoIt:      autoit,
				IsInternal:  isInternal,
				IsDefault:   platformConnect.IsDefault,
				IsSet:       isSet,
				Bridge:      platformConnect.Bridge,
			})
		}
	}

	cfg := &config.AppConfig{}
	switch osName {
	case "windows":
		cfg.Windows = appType
	case "macos":
		cfg.MacOS = appType
	default:
		cfg.Linux = appType
	}

	return cfg, true
}
