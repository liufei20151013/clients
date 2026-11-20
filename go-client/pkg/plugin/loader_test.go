package plugin

import (
	"go-client/pkg/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppConfigSanitizesLegacyMacSelections(t *testing.T) {
	if osKey() != "macos" {
		t.Skip("macOS-specific plugin defaults")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	configDir := t.TempDir()
	config := `{"_plugins":{"enabled":true}}`
	state := `{
  "version": 1,
  "selections": {
    "terminal:ssh": "builtin.putty",
    "terminal:telnet": "builtin.putty",
    "filetransfer:sftp": "builtin.iterm-sftp"
  },
  "plugins": {}
}`

	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "plugins-state.json"), []byte(state), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, ok := LoadAppConfig(configDir)
	if !ok {
		t.Fatal("expected plugin config to load")
	}

	var terminal *string
	for _, item := range cfg.MacOS.Terminal {
		if item.Name == "terminal" && item.IsActive() && item.IsMatchProtocol("ssh") {
			name := item.DisplayName
			terminal = &name
			break
		}
	}
	if terminal == nil || *terminal != "Terminal" {
		t.Fatalf("expected ssh to use Terminal, got %#v", cfg.MacOS.Terminal)
	}

	for _, item := range cfg.MacOS.FileTransfer {
		if item.Name == "iterm-sftp" && item.IsMatchProtocol("sftp") {
			t.Fatalf("legacy iTerm SFTP should not match sftp by default: %#v", item)
		}
	}
}

func TestBuildMatchFirstUsesSelectionsOnly(t *testing.T) {
	selections := map[string]string{
		"terminal:ssh": "windows.putty",
	}
	got := buildMatchFirst("windows.putty", "terminal", []string{"ssh", "telnet"}, selections)
	if len(got) != 1 || got[0] != "ssh" {
		t.Fatalf("expected only selected ssh, got %#v", got)
	}

	got = buildMatchFirst("windows.putty", "terminal", []string{"ssh", "telnet"}, map[string]string{
		"terminal:ssh": "",
	})
	if len(got) != 0 {
		t.Fatalf("expected no match after explicit disable, got %#v", got)
	}

	got = buildMatchFirst("windows.putty", "terminal", []string{"ssh", "telnet"}, map[string]string{})
	if len(got) != 0 {
		t.Fatalf("expected no match when selection missing, got %#v", got)
	}
}

func TestLoadAppConfigDoesNotFallbackMatchFirst(t *testing.T) {
	if osKey() != "macos" {
		t.Skip("macOS-specific plugin defaults")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	configDir := t.TempDir()
	config := `{"_plugins":{"enabled":true}}`
	state := `{
  "version": 1,
  "selections": {
    "terminal:ssh": "builtin.terminal"
  },
  "plugins": {
    "builtin.terminal": {"enabled": true}
  }
}`

	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "plugins-state.json"), []byte(state), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, ok := LoadAppConfig(configDir)
	if !ok {
		t.Fatal("expected plugin config to load")
	}

	for _, item := range cfg.MacOS.Terminal {
		if item.Name == "terminal" && item.IsMatchProtocol("telnet") {
			t.Fatalf("expected no telnet fallback from connect.json match_first, got %#v", item)
		}
	}
}

func TestLoadAppConfigMacElectermDualCategory(t *testing.T) {
	if osKey() != "macos" {
		t.Skip("macOS-specific plugin")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	configDir := t.TempDir()
	// SSH 用 electerm，SFTP 用 filezilla：两个分类的启用状态必须互相独立。
	state := `{
  "version": 1,
  "selections": {
    "terminal:ssh": "macos.electerm",
    "filetransfer:sftp": "macos.filezilla"
  },
  "plugins": {}
}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"_plugins":{"enabled":true}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "plugins-state.json"), []byte(state), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, ok := LoadAppConfig(configDir)
	if !ok {
		t.Fatal("expected plugin config to load")
	}

	find := func(items []config.AppItem, name string) []config.AppItem {
		var found []config.AppItem
		for _, item := range items {
			if item.Name == name {
				found = append(found, item)
			}
		}
		return found
	}

	terminal := find(cfg.MacOS.Terminal, "electerm")
	if len(terminal) != 1 {
		t.Fatalf("electerm should appear once in terminal list, got %d", len(terminal))
	}
	ssh := terminal[0]
	if ssh.Type != "terminal" {
		t.Fatalf("terminal item should carry terminal type, got %q", ssh.Type)
	}
	if !ssh.IsMatchProtocol("ssh") || !ssh.IsActive() {
		t.Fatalf("electerm should be the selected ssh client: %#v", ssh)
	}
	if strings.Contains(ssh.ArgFormat, "-so") {
		t.Fatalf("ssh launch must not contain the sftp-only flag: %q", ssh.ArgFormat)
	}
	for _, want := range []string{"-l {username}", "-P {port}", "-pw {value}", "{host}"} {
		if !strings.Contains(ssh.ArgFormat, want) {
			t.Fatalf("ssh arg_format missing %q: %q", want, ssh.ArgFormat)
		}
	}

	transfer := find(cfg.MacOS.FileTransfer, "electerm")
	if len(transfer) != 1 {
		t.Fatalf("electerm should appear once in filetransfer list, got %d", len(transfer))
	}
	sftp := transfer[0]
	if sftp.Type != "filetransfer" {
		t.Fatalf("filetransfer item should carry filetransfer type, got %q", sftp.Type)
	}
	if !strings.Contains(sftp.ArgFormat, "-so") {
		t.Fatalf("sftp launch should keep the sftp-only flag: %q", sftp.ArgFormat)
	}
	if sftp.IsMatchProtocol("sftp") {
		t.Fatalf("sftp is selected for filezilla, electerm must not claim it: %#v", sftp)
	}
	if sftp.Path != ssh.Path {
		t.Fatalf("both categories must share one executable path: %q vs %q", sftp.Path, ssh.Path)
	}

	// 单分类的老插件不能被多分类逻辑弄坏：tabby 仍只在 terminal 出现一次。
	if got := len(find(cfg.MacOS.Terminal, "tabby")); got != 1 {
		t.Fatalf("tabby should still appear once in terminal list, got %d", got)
	}
	if got := len(find(cfg.MacOS.FileTransfer, "tabby")); got != 0 {
		t.Fatalf("tabby must not leak into filetransfer list, got %d", got)
	}
}

func TestLaunchForCategoryRequiresOverrideForMultiCategory(t *testing.T) {
	connect := platformConnect{
		Launch: map[string]interface{}{"type": "args", "template": "-so {host}"},
		LaunchByCategory: map[string]map[string]interface{}{
			"terminal": {"type": "args", "template": "{host}"},
		},
	}
	multi := []string{"terminal", "filetransfer"}

	// 多分类：写了覆盖的用覆盖
	launch, ok := connect.launchForCategory("terminal", multi)
	if !ok || launch["template"] != "{host}" {
		t.Fatalf("terminal should use its override, got %#v ok=%v", launch, ok)
	}

	// 多分类：没写覆盖的分类不回退到 launch，直接判为配置错误
	if launch, ok := connect.launchForCategory("filetransfer", multi); ok {
		t.Fatalf("多分类缺覆盖必须报错，不能回退用 launch，got %#v", launch)
	}

	// 单分类：仍直接用 launch
	single := []string{"filetransfer"}
	launch, ok = connect.launchForCategory("filetransfer", single)
	if !ok || launch["template"] != "-so {host}" {
		t.Fatalf("single-category plugin should use launch, got %#v ok=%v", launch, ok)
	}
}

func TestLoadAppConfigMacFileZillaSftp(t *testing.T) {
	if osKey() != "macos" {
		t.Skip("macOS-specific plugin")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	configDir := t.TempDir()
	configJSON := `{"_plugins":{"enabled":true}}`
	state := `{
  "version": 1,
  "selections": {
    "filetransfer:sftp": "macos.filezilla"
  },
  "plugins": {}
}`

	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "plugins-state.json"), []byte(state), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, ok := LoadAppConfig(configDir)
	if !ok {
		t.Fatal("expected plugin config to load")
	}

	var filezilla *config.AppItem
	for i := range cfg.MacOS.FileTransfer {
		item := cfg.MacOS.FileTransfer[i]
		if item.Name == "filezilla" {
			filezilla = &cfg.MacOS.FileTransfer[i]
			if !item.IsActive() || !item.IsMatchProtocol("sftp") {
				t.Fatalf("filezilla should be active and matched for sftp: %#v", item)
			}
		}
	}
	if filezilla == nil {
		t.Fatal("filezilla plugin not found in macOS filetransfer list")
	}

	wantPath := filepath.FromSlash("/Applications/FileZilla.app/Contents/MacOS/filezilla")
	if filezilla.Path != wantPath {
		t.Fatalf("unexpected filezilla path: %q", filezilla.Path)
	}

	wantArgFormat := "{protocol}://{username}:{value}@{host}:{port}"
	if filezilla.ArgFormat != wantArgFormat {
		t.Fatalf("unexpected filezilla arg_format: %q", filezilla.ArgFormat)
	}
}
