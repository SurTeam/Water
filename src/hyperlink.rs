//! Client-side OSC 8 targets and default-application opening.
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

/// Decode a file URL without interpreting its hostname as an SSH destination.
/// The terminal's owning connection supplies the authenticated destination.
pub fn file_url_path(uri: &str) -> Result<PathBuf, String> {
    let rest = uri.strip_prefix("file://").ok_or("不是文件链接")?;
    let start = rest.find('/').ok_or("文件链接缺少绝对路径")?;
    let encoded = rest[start..].split(['?', '#']).next().unwrap_or("");
    let mut bytes = Vec::with_capacity(encoded.len());
    let mut input = encoded.as_bytes().iter().copied();
    while let Some(byte) = input.next() {
        if byte == b'%' {
            let high = input.next().and_then(|v| (v as char).to_digit(16));
            let low = input.next().and_then(|v| (v as char).to_digit(16));
            bytes.push(match (high, low) {
                (Some(high), Some(low)) => (high * 16 + low) as u8,
                _ => return Err("文件链接包含无效转义".into()),
            });
        } else {
            bytes.push(byte);
        }
    }
    let path = String::from_utf8(bytes).map_err(|_| "文件路径不是有效 UTF-8")?;
    if path.chars().any(char::is_control) || !Path::new(&path).is_absolute() {
        return Err("无效的文件路径".into());
    }
    Ok(PathBuf::from(path))
}

pub fn download_directory(value: &str) -> Result<PathBuf, String> {
    if value.chars().any(char::is_control) {
        return Err("无效的下载目录".into());
    }
    let value = value.trim();
    if value.is_empty() || value.chars().any(char::is_control) {
        return Err("无效的下载目录".into());
    }
    if value == "~" || value.starts_with("~/") {
        let home = std::env::var_os("HOME").ok_or("无法确定用户主目录")?;
        Ok(PathBuf::from(home).join(value.strip_prefix("~/").unwrap_or("")))
    } else {
        let path = PathBuf::from(value);
        if !path.is_absolute() {
            return Err("下载目录必须是绝对路径或以 ~/ 开头".into());
        }
        Ok(path)
    }
}

pub fn open_target(target: &str) -> Result<(), String> {
    let scheme = target.split_once(':').map(|(scheme, _)| scheme);
    if !scheme.is_some_and(|scheme| {
        !scheme.is_empty()
            && scheme.as_bytes()[0].is_ascii_alphabetic()
            && scheme
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || b"+.-".contains(&byte))
    }) {
        return Err("无效的超链接协议".into());
    }
    if target.chars().any(char::is_control) {
        return Err("超链接包含控制字符".into());
    }
    if scheme == Some("file") {
        open_path(&file_url_path(target)?)
    } else {
        launch_default_application(target)
    }
}

pub fn open_path(path: &Path) -> Result<(), String> {
    launch_default_application(path.as_os_str())
}

fn launch_default_application(target: impl AsRef<std::ffi::OsStr>) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    let program = "open";
    #[cfg(not(target_os = "macos"))]
    let program = "xdg-open";
    let mut child = Command::new(program)
        .arg(target)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| e.to_string())?;
    std::thread::spawn(move || {
        let _ = child.wait();
    });
    Ok(())
}

/// Preserve each download in a separate directory, avoiding accidental overwrites.
pub fn download_remote(destination: &str, uri: &str, directory: &str) -> Result<PathBuf, String> {
    let destination =
        crate::remote::validate_ssh_destination(destination).map_err(|e| e.to_string())?;
    let source = file_url_path(uri)?;
    let filename = source.file_name().ok_or("无法下载远程根目录")?;
    let parent = download_directory(directory)?.join(uuid::Uuid::new_v4().to_string());
    std::fs::create_dir_all(&parent).map_err(|e| e.to_string())?;
    let output = Command::new("scp")
        .arg("-o")
        .arg(format!(
            "ControlPath={}",
            crate::remote::control_master_socket(&destination).display()
        ))
        .args(["-r", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--"])
        .arg(format!("{destination}:{}", source.display()))
        .arg(&parent)
        .output()
        .map_err(|e| e.to_string())?;
    if !output.status.success() {
        return Err(format!(
            "下载失败：{}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(parent.join(filename))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn file_urls_decode_paths_and_reject_invalid_escapes() {
        assert_eq!(
            file_url_path("file://remote/tmp/a%20b%23c").unwrap(),
            PathBuf::from("/tmp/a b#c")
        );
        assert_eq!(
            file_url_path("file:///tmp/%E4%B8%AD%E6%96%87").unwrap(),
            PathBuf::from("/tmp/中文")
        );
        assert_eq!(
            file_url_path("file:///tmp/a%23b#fragment").unwrap(),
            PathBuf::from("/tmp/a#b")
        );
        for uri in [
            "https://example.com/a",
            "file://host",
            "file:///tmp/%",
            "file:///tmp/%00",
            "file:///tmp/%ZZ",
        ] {
            assert!(file_url_path(uri).is_err(), "{uri}");
        }
    }

    #[test]
    fn download_directory_requires_an_absolute_or_home_path() {
        assert!(
            download_directory("~/Downloads/Water")
                .unwrap()
                .is_absolute()
        );
        assert_eq!(
            download_directory("/tmp/water-downloads").unwrap(),
            PathBuf::from("/tmp/water-downloads")
        );
        for directory in ["", "relative", "~someone/files", "/tmp/\n"] {
            assert!(download_directory(directory).is_err(), "{directory}");
        }
    }
}
