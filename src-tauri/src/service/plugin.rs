use serde_json::{json, Map, Value};
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use tauri::AppHandle;
use tauri::Manager;

const CATEGORIES: &[&str] = &["terminal", "remotedesktop", "filetransfer", "databases"];

pub struct PluginService;

impl PluginService {
    fn os_key() -> &'static str {
        match std::env::consts::OS {
            "macos" => "macos",
            "windows" => "windows",
            "linux" => "linux",
            other => other,
        }
    }

    fn resolve_resource_dir(app: &AppHandle, candidates: &[&str], marker: &str) -> Option<PathBuf> {
        for candidate in candidates {
            let Ok(path) = app
                .path()
                .resolve(candidate, tauri::path::BaseDirectory::Resource)
            else {
                continue;
            };

            if path.join(marker).is_file() {
                log::info!("Resolved resource dir '{}' to {:?}", candidate, path);
                return Some(path);
            }
        }

        None
    }

    fn resolve_resource_file(app: &AppHandle, candidates: &[&str]) -> Option<PathBuf> {
        for candidate in candidates {
            let Ok(path) = app
                .path()
                .resolve(candidate, tauri::path::BaseDirectory::Resource)
            else {
                continue;
            };

            if path.is_file() {
                log::info!("Resolved resource file '{}' to {:?}", candidate, path);
                return Some(path);
            }
        }

        None
    }

    fn resolve_builtin_dir(app: &AppHandle) -> Option<PathBuf> {
        let os_key = Self::os_key();
        let resource_candidates = [
            format!("resources/plugins/{os_key}"),
            format!("plugins/{os_key}"),
            os_key.to_string(),
            "resources/plugins/builtin".to_string(),
            "plugins/builtin".to_string(),
            "builtin".to_string(),
        ];
        let resource_refs: Vec<&str> = resource_candidates.iter().map(String::as_str).collect();
        let resource = Self::resolve_resource_dir(app, &resource_refs, "index.json");

        if let Some(path) = resource {
            return Some(path);
        }

        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let candidates = [
            cwd.join("plugins").join(os_key),
            cwd.join("../plugins").join(os_key),
            cwd.join("../../plugins").join(os_key),
            cwd.join("plugins/builtin"),
            cwd.join("../plugins/builtin"),
            cwd.join("../../plugins/builtin"),
        ];
        let resolved = candidates
            .into_iter()
            .find(|p| p.join("index.json").is_file());

        if let Some(path) = resolved.as_ref() {
            log::info!("Resolved builtin plugins from cwd fallback: {:?}", path);
        }

        resolved
    }

    fn resolve_defaults_path(app: &AppHandle) -> Option<PathBuf> {
        let os_key = Self::os_key();
        let resource_candidates = [
            format!("resources/plugins/{os_key}/plugins-state.defaults.json"),
            format!("plugins/{os_key}/plugins-state.defaults.json"),
            format!("{os_key}/plugins-state.defaults.json"),
            "resources/plugins/plugins-state.defaults.json".to_string(),
            "plugins/plugins-state.defaults.json".to_string(),
            "plugins-state.defaults.json".to_string(),
        ];
        let resource_refs: Vec<&str> = resource_candidates.iter().map(String::as_str).collect();
        let resource = Self::resolve_resource_file(app, &resource_refs);

        if let Some(path) = resource {
            return Some(path);
        }

        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let candidates = [
            cwd.join("plugins")
                .join(os_key)
                .join("plugins-state.defaults.json"),
            cwd.join("../plugins")
                .join(os_key)
                .join("plugins-state.defaults.json"),
            cwd.join("plugins/plugins-state.defaults.json"),
            cwd.join("../plugins/plugins-state.defaults.json"),
        ];
        let resolved = candidates.into_iter().find(|p| p.is_file());

        if let Some(path) = resolved.as_ref() {
            log::info!("Resolved plugin defaults from cwd fallback: {:?}", path);
        }

        resolved
    }

    fn read_json(path: &Path) -> Result<Value, String> {
        let content = fs::read_to_string(path)
            .map_err(|e| format!("read {} failed: {}", path.display(), e))?;
        serde_json::from_str(&content)
            .map_err(|e| format!("parse {} failed: {}", path.display(), e))
    }

    fn sanitize_user_state(state: &mut Value) -> bool {
        let Some(state_obj) = state.as_object_mut() else {
            return false;
        };

        let mut changed = false;
        let os_key = Self::os_key();
        if let Some(selections) = state_obj
            .get_mut("selections")
            .and_then(|v| v.as_object_mut())
        {
            for value in selections.values_mut() {
                if let Some(plugin_id) = value.as_str() {
                    if let Some(suffix) = plugin_id.strip_prefix("builtin.") {
                        *value = Value::String(format!("{os_key}.{suffix}"));
                        changed = true;
                    }
                }
            }
        }
        if let Some(plugins) = state_obj.get_mut("plugins").and_then(|v| v.as_object_mut()) {
            let legacy_entries: Vec<(String, Value)> = plugins
                .iter()
                .filter_map(|(key, value)| {
                    key.strip_prefix("builtin.")
                        .map(|suffix| (format!("{os_key}.{suffix}"), value.clone()))
                })
                .collect();
            for (key, value) in legacy_entries {
                plugins.insert(key, value);
                changed = true;
            }
            plugins.retain(|key, _| !key.starts_with("builtin."));
        }

        if Self::os_key() != "windows" {
            if let Some(selections) = state_obj
                .get_mut("selections")
                .and_then(|v| v.as_object_mut())
            {
                for key in ["terminal:ssh", "terminal:telnet"] {
                    if selections.get(key).and_then(|v| v.as_str())
                        == Some(&format!("{os_key}.putty"))
                    {
                        selections.insert(
                            key.to_string(),
                            Value::String(format!("{os_key}.terminal")),
                        );
                        changed = true;
                    }
                }
            }
        }

        if Self::os_key() == "linux" {
            if let Some(selections) = state_obj
                .get_mut("selections")
                .and_then(|v| v.as_object_mut())
            {
                match selections.get("remotedesktop:rdp").and_then(|v| v.as_str()) {
                    Some("linux.mstsc") | Some("linux.remmina") => {
                        selections.insert(
                            "remotedesktop:rdp".to_string(),
                            Value::String("linux.xfreerdp".to_string()),
                        );
                        changed = true;
                    }
                    _ => {}
                }
            }
        }

        let should_remove_sftp_iterm = state_obj
            .get("selections")
            .and_then(|v| v.get("filetransfer:sftp"))
            .and_then(|v| v.as_str())
            == Some(&format!("{os_key}.iterm-sftp"));

        if !should_remove_sftp_iterm {
            return changed;
        }

        let has_user_override = state_obj
            .get("plugins")
            .and_then(|v| v.get(format!("{os_key}.iterm-sftp")))
            .and_then(|v| v.as_object())
            .map(|obj| {
                obj.get("enabled")
                    .and_then(|v| v.as_bool())
                    .unwrap_or(false)
                    || obj
                        .get("path")
                        .and_then(|v| v.as_str())
                        .map(|s| !s.trim().is_empty())
                        .unwrap_or(false)
            })
            .unwrap_or(false);

        if has_user_override {
            return changed;
        }

        if let Some(selections) = state_obj
            .get_mut("selections")
            .and_then(|v| v.as_object_mut())
        {
            if selections.remove("filetransfer:sftp").is_some() {
                changed = true;
            }
        }
        changed
    }

    fn launch_to_arg_format(launch: &Value) -> (String, Option<Value>) {
        let launch_type = launch
            .get("type")
            .and_then(|v| v.as_str())
            .unwrap_or("args");
        match launch_type {
            "autoit" => (String::new(), launch.get("steps").cloned()),
            "file" => {
                let template = launch
                    .get("arg_template")
                    .and_then(|v| v.as_str())
                    .unwrap_or("{file}");
                (template.to_string(), None)
            }
            _ => {
                let template = launch
                    .get("template")
                    .and_then(|v| v.as_str())
                    .unwrap_or("");
                (template.to_string(), None)
            }
        }
    }

    fn platform_connect(connect: &Value, os_key: &str) -> Option<Value> {
        if let Some(platform_connect) = connect.get("platforms").and_then(|p| p.get(os_key)) {
            return Some(platform_connect.clone());
        }

        if connect.get("executable").is_some() {
            return Some(connect.clone());
        }

        None
    }

    fn resolve_path(
        plugin_id: &str,
        platform_connect: &Value,
        user_state: &Value,
    ) -> (String, bool, bool) {
        let user_plugins = user_state.get("plugins").and_then(|v| v.as_object());
        let user_plugin = user_plugins.and_then(|m| m.get(plugin_id));

        let user_path = user_plugin
            .and_then(|p| p.get("path"))
            .and_then(|v| v.as_str())
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty());

        let exec_default = platform_connect
            .get("executable")
            .and_then(|e| e.get("default"))
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();

        let path = user_path.clone().unwrap_or(exec_default);

        let is_internal = platform_connect
            .get("is_internal")
            .and_then(|v| v.as_bool())
            .unwrap_or(false);

        let is_set = platform_connect
            .get("is_set")
            .and_then(|v| v.as_bool())
            .unwrap_or(false)
            || user_path.is_some()
            || (!path.is_empty() && is_internal && user_plugin.is_some());

        let enabled = user_plugin
            .and_then(|p| p.get("enabled"))
            .and_then(|v| v.as_bool())
            .unwrap_or(true);

        (path, is_set && enabled, is_internal)
    }

    fn executable_type(platform_connect: &Value) -> &str {
        platform_connect
            .get("executable")
            .and_then(|e| e.get("type"))
            .and_then(|v| v.as_str())
            .unwrap_or("")
    }

    /// category 允许写成 "terminal" 或 ["terminal","filetransfer"]：
    /// 一个插件可以同时服务多个分类（例如 electerm 既当 SSH 终端又当 SFTP 客户端）。
    fn category_list(entry: &Value) -> Vec<String> {
        match entry.get("category") {
            Some(Value::String(s)) => {
                if s.is_empty() {
                    Vec::new()
                } else {
                    vec![s.clone()]
                }
            }
            Some(Value::Array(items)) => items
                .iter()
                .filter_map(|v| v.as_str())
                .filter(|s| !s.is_empty())
                .map(|s| s.to_string())
                .collect(),
            _ => Vec::new(),
        }
    }

    /// 取某个分类该用的启动定义。
    ///
    /// 单分类插件直接用 `launch`。多分类插件**必须**在 `launch_by_category` 里为每个分类
    /// 各写一份：同一款应用当终端和当文件传输工具时参数几乎必然不同（例如 Electerm 的
    /// `-so` 只在 SFTP 下合法），拿 `launch` 兜底等于这个分类必然连错，
    /// 因此缺覆盖时返回 None，由调用方放弃该分类，而不是用另一套参数蒙过去。
    fn launch_for_category<'a>(
        platform_connect: &'a Value,
        category: &str,
        multi_category: bool,
    ) -> Option<&'a Value> {
        if multi_category {
            platform_connect
                .get("launch_by_category")
                .and_then(|m| m.get(category))
                .filter(|v| v.is_object())
        } else {
            platform_connect
                .get("launch")
                .filter(|v| v.is_object())
        }
    }

    fn user_path_exists(platform_connect: &Value, path: &str) -> bool {
        if Self::executable_type(platform_connect) != "user_path" {
            return true;
        }
        let trimmed = path.trim();
        !trimmed.is_empty() && Path::new(trimmed).is_file()
    }

    fn validate_user_path_executable(
        platform_connect: &Value,
        user_state: &Value,
        plugin_id: &str,
    ) -> Result<(), String> {
        if Self::executable_type(platform_connect) != "user_path" {
            return Ok(());
        }

        let (path, _, _) = Self::resolve_path(plugin_id, platform_connect, user_state);
        if Self::user_path_exists(platform_connect, &path) {
            return Ok(());
        }

        if path.trim().is_empty() {
            return Err("executable not found: (empty path)".to_string());
        }

        Err(format!("executable not found: {}", path.trim()))
    }

    fn build_match_first(
        plugin_id: &str,
        category: &str,
        protocols: &[Value],
        selections: &Map<String, Value>,
    ) -> Vec<Value> {
        let mut matched = Vec::new();
        for protocol in protocols {
            let Some(proto) = protocol.as_str() else {
                continue;
            };
            let key = format!("{category}:{proto}");
            if selections.get(&key).and_then(|v| v.as_str()) == Some(plugin_id) {
                matched.push(Value::String(proto.to_string()));
            }
        }
        matched
    }

    /// 把一个插件翻译成若干 app item：manifest 里声明了几个分类就产出几个，
    /// 每个分类的 item 各自带自己的启动参数与 match_first。
    fn plugin_to_app_items(
        plugin_id: &str,
        plugin_dir: &Path,
        os_key: &str,
        selections: &Map<String, Value>,
        user_state: &Value,
    ) -> Result<Vec<(String, Value)>, String> {
        let manifest = Self::read_json(&plugin_dir.join("manifest.json"))?;
        let connect = Self::read_json(&plugin_dir.join("connect.json"))?;

        let Some(platform_connect) = Self::platform_connect(&connect, os_key) else {
            return Ok(Vec::new());
        };

        let categories = Self::category_list(&manifest);
        if categories.is_empty() {
            return Ok(Vec::new());
        }
        let protocols = manifest
            .get("protocols")
            .and_then(|v| v.as_array())
            .cloned()
            .unwrap_or_default();

        let display_name = platform_connect
            .get("display_name")
            .and_then(|v| v.as_str())
            .or_else(|| manifest.get("display_name").and_then(|v| v.as_str()))
            .unwrap_or(plugin_id);

        let (path, base_is_set, is_internal) =
            Self::resolve_path(plugin_id, &platform_connect, user_state);

        let executable_type = Self::executable_type(&platform_connect);
        let path_exists = Self::user_path_exists(&platform_connect, &path);

        let is_default = platform_connect
            .get("is_default")
            .and_then(|v| v.as_bool())
            .unwrap_or(false);

        let mut items = Vec::with_capacity(categories.len());
        let multi_category = categories.len() > 1;
        for category in categories {
            // 多分类时 launch_by_category 是必需的：缺了就说明打包者没想清楚这个分类
            // 该怎么启动，宁可让这个分类不注册，也不要用另一套参数蒙过去。
            let Some(launch) =
                Self::launch_for_category(&platform_connect, &category, multi_category)
            else {
                log::error!(
                    "Multi-category plugin {} is missing launch_by_category for '{}', not registering it",
                    plugin_id,
                    category
                );
                continue;
            };
            let (mut arg_format, autoit) = Self::launch_to_arg_format(launch);
            // 命令桥接的 profile 名只在 connect.json 里写一次，展示用的 arg_format 里替换掉占位符。
            if let Some(profile_name) = platform_connect
                .get("bridge")
                .and_then(|b| b.get("profile_name"))
                .and_then(|v| v.as_str())
            {
                arg_format = arg_format.replace("{bridge_profile}", profile_name);
            }

            let match_first =
                Self::build_match_first(plugin_id, &category, &protocols, selections);
            // 每个分类的“已启用”状态独立：SSH 页启用了不代表 SFTP 页也启用。
            let is_set = base_is_set || !match_first.is_empty();

            let mut item = json!({
                "name": manifest.get("name").and_then(|v| v.as_str()).unwrap_or(""),
                "display_name": display_name,
                "protocol": protocols,
                "comment": manifest.get("comment").cloned().unwrap_or(json!({})),
                "download_url": manifest.get("download_url").and_then(|v| v.as_str()).unwrap_or(""),
                "type": category,
                "path": path,
                "arg_format": arg_format,
                "match_first": match_first,
                "is_internal": is_internal,
                "is_default": is_default,
                "is_set": is_set,
                "executable_type": executable_type,
                "path_exists": path_exists,
                "_plugin_id": plugin_id,
            });

            if let Some(steps) = autoit.clone() {
                item.as_object_mut()
                    .unwrap()
                    .insert("autoit".to_string(), steps);
            }

            // bridge 原样透传：go-client 拉起客户端时会按它把本次连接写成一个桥接脚本，
            // 让终端跑固定的本地 profile 去 exec 内置 client，从而免框、免系统凭据地自动登录。
            if let Some(bridge) = platform_connect.get("bridge") {
                item.as_object_mut()
                    .unwrap()
                    .insert("bridge".to_string(), bridge.clone());
            }

            items.push((category, item));
        }

        Ok(items)
    }

    fn load_user_state(app: &AppHandle, config_dir: &Path) -> Value {
        let state_path = config_dir.join("plugins-state.json");
        if state_path.is_file() {
            if let Ok(mut state) = Self::read_json(&state_path) {
                if Self::sanitize_user_state(&mut state) {
                    if let Ok(pretty) = serde_json::to_string_pretty(&state) {
                        let _ = fs::write(&state_path, pretty);
                    }
                }
                return state;
            }
        }

        if let Some(defaults_path) = Self::resolve_defaults_path(app) {
            if let Ok(mut state) = Self::read_json(&defaults_path) {
                Self::sanitize_user_state(&mut state);
                if let Ok(pretty) = serde_json::to_string_pretty(&state) {
                    let _ = fs::write(&state_path, pretty);
                } else {
                    let _ = fs::copy(&defaults_path, &state_path);
                }
                return state;
            }
        }

        json!({ "version": 1, "selections": {}, "plugins": {} })
    }

    pub fn build_app_config(app: &AppHandle, config_dir: &Path) -> Result<Value, String> {
        let builtin_dir = Self::resolve_builtin_dir(app)
            .ok_or_else(|| "platform plugins directory not found".to_string())?;
        let index = Self::read_json(&builtin_dir.join("index.json"))?;
        let plugins = index
            .get("plugins")
            .and_then(|v| v.as_array())
            .ok_or_else(|| "invalid platform plugins index.json".to_string())?;

        let user_state = Self::load_user_state(app, config_dir);
        let selections = user_state
            .get("selections")
            .and_then(|v| v.as_object())
            .cloned()
            .unwrap_or_default();

        let os_key = Self::os_key();
        let mut per_category: HashMap<String, Vec<Value>> = HashMap::new();
        for cat in CATEGORIES {
            per_category.insert((*cat).to_string(), Vec::new());
        }

        for entry in plugins {
            let plugin_id = entry
                .get("id")
                .and_then(|v| v.as_str())
                .ok_or_else(|| "plugin entry missing id".to_string())?;

            let plugin_dir = builtin_dir.join(plugin_id);
            if !plugin_dir.is_dir() {
                log::warn!("Plugin directory not found: {:?}", plugin_dir);
                continue;
            }

            for (category, item) in
                Self::plugin_to_app_items(plugin_id, &plugin_dir, os_key, &selections, &user_state)?
            {
                if let Some(list) = per_category.get_mut(category.as_str()) {
                    list.push(item);
                }
            }
        }

        // Strip internal _plugin_id before returning to frontend.
        // Selections alone drive match_first — no connect.json fallback.
        for items in per_category.values_mut() {
            for item in items.iter_mut() {
                if let Some(obj) = item.as_object_mut() {
                    obj.remove("_plugin_id");
                }
            }
        }

        Ok(json!({
            "terminal": per_category.get("terminal").cloned().unwrap_or_default(),
            "remotedesktop": per_category.get("remotedesktop").cloned().unwrap_or_default(),
            "filetransfer": per_category.get("filetransfer").cloned().unwrap_or_default(),
            "databases": per_category.get("databases").cloned().unwrap_or_default(),
        }))
    }

    pub fn is_plugins_enabled(config: &Value) -> bool {
        config
            .get("_plugins")
            .and_then(|p| p.get("enabled"))
            .and_then(|v| v.as_bool())
            .unwrap_or(false)
    }

    fn find_plugin_id_by_name(builtin_dir: &Path, name: &str, category: &str) -> Option<String> {
        let index = Self::read_json(&builtin_dir.join("index.json")).ok()?;
        index.get("plugins")?.as_array()?.iter().find_map(|entry| {
            let id = entry.get("id")?.as_str()?;
            let entry_name = entry.get("name")?.as_str()?;
            // category 可以是字符串或数组：只要该插件服务这个分类就算匹配。
            if entry_name == name && Self::category_list(entry).iter().any(|c| c == category) {
                Some(id.to_string())
            } else {
                None
            }
        })
    }

    pub fn update_selection(
        app: &AppHandle,
        config_dir: &Path,
        category: &str,
        protocol: &str,
        name: &str,
        new_path: Option<String>,
        enabled: bool,
    ) -> Result<Value, String> {
        let builtin_dir = Self::resolve_builtin_dir(app)
            .ok_or_else(|| "platform plugins directory not found".to_string())?;
        let plugin_id = Self::find_plugin_id_by_name(&builtin_dir, name, category)
            .ok_or_else(|| format!("plugin '{name}' not found in category '{category}'"))?;

        let state_path = config_dir.join("plugins-state.json");
        let mut state = Self::load_user_state(app, config_dir);

        let os_key = Self::os_key();
        let connect = Self::read_json(&builtin_dir.join(&plugin_id).join("connect.json"))?;
        let platform_connect = Self::platform_connect(&connect, os_key)
            .ok_or_else(|| format!("plugin '{name}' has no config for OS '{os_key}'"))?;

        if let Some(p) = new_path {
            let trimmed = p.trim();
            if !trimmed.is_empty() {
                let state_obj = state.as_object_mut().ok_or("invalid plugins-state")?;
                let plugins = state_obj
                    .entry("plugins")
                    .or_insert(json!({}))
                    .as_object_mut()
                    .unwrap();
                let entry = plugins
                    .entry(plugin_id.clone())
                    .or_insert(json!({}))
                    .as_object_mut()
                    .unwrap();
                entry.insert("path".into(), Value::String(trimmed.to_string()));
                entry.insert("enabled".into(), Value::Bool(true));

                let pretty = serde_json::to_string_pretty(&state)
                    .map_err(|e| format!("serialize plugins-state.json failed: {}", e))?;
                fs::write(&state_path, pretty)
                    .map_err(|e| format!("write plugins-state.json failed: {}", e))?;

                return Self::build_app_config(app, config_dir);
            }
        }

        if enabled {
            Self::validate_user_path_executable(&platform_connect, &state, &plugin_id)?;
        }

        let state_obj = state.as_object_mut().ok_or("invalid plugins-state")?;
        let selection_key = format!("{category}:{protocol}");
        {
            let selections = state_obj
                .entry("selections")
                .or_insert(json!({}))
                .as_object_mut()
                .unwrap();
            if enabled {
                selections.insert(selection_key, Value::String(plugin_id.clone()));
            } else {
                // Keep the key with an empty value so defaults are not re-applied
                // (allows disabling even when a protocol has only one app).
                selections.insert(selection_key, Value::String(String::new()));
            }
        }
        if enabled {
            let entry = state_obj
                .entry("plugins")
                .or_insert(json!({}))
                .as_object_mut()
                .unwrap()
                .entry(plugin_id)
                .or_insert(json!({}))
                .as_object_mut()
                .unwrap();
            entry.insert("enabled".into(), Value::Bool(true));
        }

        let pretty = serde_json::to_string_pretty(&state)
            .map_err(|e| format!("serialize plugins-state.json failed: {}", e))?;
        fs::write(&state_path, pretty)
            .map_err(|e| format!("write plugins-state.json failed: {}", e))?;

        Self::build_app_config(app, config_dir)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn category_list_accepts_string_and_array() {
        let single = json!({ "category": "terminal" });
        assert_eq!(PluginService::category_list(&single), vec!["terminal"]);

        let dual = json!({ "category": ["terminal", "filetransfer"] });
        assert_eq!(
            PluginService::category_list(&dual),
            vec!["terminal", "filetransfer"]
        );

        assert!(PluginService::category_list(&json!({})).is_empty());
        assert!(PluginService::category_list(&json!({ "category": "" })).is_empty());
    }

    #[test]
    fn multi_category_plugin_requires_launch_by_category() {
        let connect = json!({
            "launch": { "type": "args", "template": "-so {host}" },
            "launch_by_category": {
                "terminal": { "type": "args", "template": "{host}" }
            }
        });

        // 多分类：写了覆盖的分类用覆盖
        assert_eq!(
            PluginService::launch_for_category(&connect, "terminal", true)
                .and_then(|v| v.get("template"))
                .and_then(|v| v.as_str()),
            Some("{host}")
        );
        // 多分类：没写覆盖的分类不回退到 launch，直接判为配置错误
        assert!(
            PluginService::launch_for_category(&connect, "filetransfer", true).is_none(),
            "多分类插件缺覆盖时必须报配置错误，不能回退用 launch 的参数"
        );

        // 单分类：仍直接用 launch
        assert_eq!(
            PluginService::launch_for_category(&connect, "filetransfer", false)
                .and_then(|v| v.get("template"))
                .and_then(|v| v.as_str()),
            Some("-so {host}")
        );
    }

    #[test]
    fn find_plugin_id_by_name_matches_multi_category_plugin() {
        let dir = std::env::temp_dir().join(format!("jms-plugin-idx-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        fs::write(
            dir.join("index.json"),
            r#"{
              "plugins": [
                { "id": "macos.electerm", "name": "electerm",
                  "category": ["terminal", "filetransfer"] },
                { "id": "macos.tabby", "name": "tabby", "category": "terminal" }
              ]
            }"#,
        )
        .unwrap();

        // 多分类插件在两个分类下都能被“启用”按钮找到
        assert_eq!(
            PluginService::find_plugin_id_by_name(&dir, "electerm", "terminal"),
            Some("macos.electerm".to_string())
        );
        assert_eq!(
            PluginService::find_plugin_id_by_name(&dir, "electerm", "filetransfer"),
            Some("macos.electerm".to_string())
        );
        // 单分类插件行为不变
        assert_eq!(
            PluginService::find_plugin_id_by_name(&dir, "tabby", "terminal"),
            Some("macos.tabby".to_string())
        );
        assert_eq!(
            PluginService::find_plugin_id_by_name(&dir, "tabby", "filetransfer"),
            None
        );

        let _ = fs::remove_dir_all(&dir);
    }
}
