package awaken

import (
	"go-client/pkg/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBridge() *config.CommandBridge {
	return &config.CommandBridge{
		ProfileName:  "JumpServer",
		ProfileID:    "jumpserver-client",
		ScriptName:   "tabby-bridge.sh",
		ArgsTemplate: "{protocol} {username}@{host} -p {port} -P {value}",
		ConfigPath:   "~/Library/Application Support/tabby/config.yaml",
		Shell:        "/bin/sh",
		RunFallback:  true,
	}
}

func TestInsertProfileIntoEmptyList(t *testing.T) {
	original := "version: 8\nprofiles: []\ngroups: []\n"
	bridge := testBridge()
	next, changed := insertProfile(original, bridge, bridgeProfileBlock(bridge, "/tmp/x.sh"))
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(next, "profiles:\n  - id: jumpserver-client") {
		t.Fatalf("profile not inserted correctly:\n%s", next)
	}
	if !strings.Contains(next, "groups: []") {
		t.Fatal("existing keys must be preserved")
	}
	// 缩进必须能续在 profiles: 下面，不能把 groups 吞进去
	lines := strings.Split(next, "\n")
	idx := -1
	for i, l := range lines {
		if l == "groups: []" {
			idx = i
		}
	}
	if idx < 0 || lines[idx-1] != "    weight: 0.0" {
		t.Fatalf("unexpected block end:\n%s", next)
	}
}

func TestInsertProfileIntoExistingList(t *testing.T) {
	original := strings.Join([]string{
		"version: 8",
		"profiles:",
		"  - id: local-1",
		"    name: Local",
		"    type: local",
		"groups: []",
		"",
	}, "\n")
	bridge := testBridge()
	next, changed := insertProfile(original, bridge, bridgeProfileBlock(bridge, "/tmp/x.sh"))
	if !changed {
		t.Fatal("expected change")
	}
	if !strings.Contains(next, "  - id: local-1") || !strings.Contains(next, "  - id: jumpserver-client") {
		t.Fatalf("both profiles should exist:\n%s", next)
	}
	if strings.Index(next, "jumpserver-client") > strings.Index(next, "groups: []") {
		t.Fatalf("profile inserted after next top-level key:\n%s", next)
	}
}

func TestInsertProfileIdempotent(t *testing.T) {
	bridge := testBridge()
	block := bridgeProfileBlock(bridge, "/tmp/x.sh")
	first, changed := insertProfile("version: 8\nprofiles: []\n", bridge, block)
	if !changed {
		t.Fatal("expected first insert to change")
	}
	second, changedAgain := insertProfile(first, bridge, block)
	if changedAgain {
		t.Fatal("second insert must be a no-op")
	}
	if second != first {
		t.Fatal("content must stay identical")
	}
}

func TestInsertProfileWithoutProfilesKey(t *testing.T) {
	bridge := testBridge()
	next, changed := insertProfile("version: 8\ngroups: []\n", bridge, bridgeProfileBlock(bridge, "/tmp/x.sh"))
	if !changed || !strings.Contains(next, "profiles:\n  - id: jumpserver-client") {
		t.Fatalf("expected appended profiles block:\n%s", next)
	}
}

func TestBridgeProfileBlockQuotesPath(t *testing.T) {
	bridge := testBridge()
	block := bridgeProfileBlock(bridge, "/Users/a b/Library/Application Support/tabby/bridge.sh")
	if !strings.Contains(block, `- '/Users/a b/Library/Application Support/tabby/bridge.sh'`) {
		t.Fatalf("path with spaces must be quoted:\n%s", block)
	}
	if !strings.Contains(block, "      command: /bin/sh") {
		t.Fatalf("shell must be written:\n%s", block)
	}
}

func TestBridgeScriptQuotesArguments(t *testing.T) {
	args := []string{"ssh", "JMS-abc@10.1.12.56", "-p", "2222", "-P", "p'wd"}
	script := bridgeScript("/bin/sh", "/Applications/JumpServer Client.app/client", args)
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatalf("missing shebang:\n%s", script)
	}
	if !strings.Contains(script, `exec '/Applications/JumpServer Client.app/client'`) {
		t.Fatalf("client path must be quoted:\n%s", script)
	}
	if !strings.Contains(script, `'p'"'"'wd'`) {
		t.Fatalf("single quote must be escaped for shell:\n%s", script)
	}
}

// TestBridgeCommandEndToEnd 走完整链路：渲染参数 -> 写脚本 -> 装 profile -> 返回启动命令。
// 用临时 HOME 隔离，不会碰到用户真实的 Tabby 配置。
func TestBridgeCommandEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// clientBinaryPath 要求 <os.Args[0] 所在目录>/client 存在，测试二进制目录里造一个。
	binDir := filepath.Dir(os.Args[0])
	fakeClient := filepath.Join(binDir, "client")
	if err := os.WriteFile(fakeClient, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("create fake client: %v", err)
	}
	defer os.Remove(fakeClient)

	configPath := filepath.Join(home, "Library", "Application Support", "tabby", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 8\nprofiles: []\ngroups: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bridge := testBridge()
	bridge.ConfigPath = configPath
	appItem := &config.AppItem{
		Name:        "ssh",
		DisplayName: "Tabby",
		// 不存在的路径，保证 pgrep 找不到进程 -> 当作冷启动，走 profile 分支。
		Path:   filepath.Join(home, "not-running-tabby"),
		Bridge: bridge,
	}
	connectMap := map[string]string{
		"protocol": "ssh",
		"username": "JMS-7e0206e8",
		"host":     "10.1.12.56",
		"port":     "2222",
		"value":    "Bw3J1nj5Yy8YVMWq",
	}

	cmd, err := bridgeCommand(appItem, connectMap)
	if err != nil {
		t.Fatalf("bridgeCommand: %v", err)
	}
	if got, want := strings.Join(cmd.Args, " "), appItem.Path+" profile JumpServer"; got != want {
		t.Fatalf("launch args = %q, want %q", got, want)
	}

	scriptPath := filepath.Join(home, "Library", "Application Support", "jumpserver-client", "tabby-bridge.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read bridge script: %v", err)
	}
	wantArgs := "exec '" + fakeClient + "' 'ssh' 'JMS-7e0206e8@10.1.12.56' '-p' '2222' '-P' 'Bw3J1nj5Yy8YVMWq'"
	if !strings.Contains(string(script), wantArgs) {
		t.Fatalf("script missing args line:\n%s", script)
	}
	if info, err := os.Stat(scriptPath); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("script must be executable, mode=%v err=%v", info.Mode().Perm(), err)
	}

	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "  - id: jumpserver-client") ||
		!strings.Contains(string(written), "- '"+scriptPath+"'") {
		t.Fatalf("profile not installed:\n%s", written)
	}

	// 第二次调用要幂等，不能再改写配置。
	if _, err := bridgeCommand(appItem, connectMap); err != nil {
		t.Fatalf("second bridgeCommand: %v", err)
	}
	again, _ := os.ReadFile(configPath)
	if string(again) != string(written) {
		t.Fatalf("config must not change on second launch:\n%s", again)
	}
}

func TestParseElapsed(t *testing.T) {
	cases := map[string]time.Duration{
		"05:30":      5*time.Minute + 30*time.Second,
		"1:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:02:03": 2*24*time.Hour + time.Hour + 2*time.Minute + 3*time.Second,
		"10:00":      10 * time.Minute,
	}
	for input, want := range cases {
		got, ok := parseElapsed(input)
		if !ok || got != want {
			t.Fatalf("parseElapsed(%q) = %v, %v; want %v", input, got, ok, want)
		}
	}
	if _, ok := parseElapsed(""); ok {
		t.Fatal("empty input must not parse")
	}
}
