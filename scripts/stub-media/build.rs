#![allow(clippy::disallowed_methods, reason = "build scripts are exempt")]
#[cfg(target_os = "macos")]
fn main() {
    use std::{env, path::PathBuf, process::Command};

    let sdk_path = String::from_utf8(
        Command::new("xcrun")
            .args(["--sdk", "macosx", "--show-sdk-path"])
            .output()
            .unwrap()
            .stdout,
    )
    .unwrap();
    let sdk_path = sdk_path.trim_end();

    println!("cargo:rerun-if-changed=src/bindings.h");
    let bindings = bindgen::Builder::default()
        .header("src/bindings.h")
        .clang_arg(format!("-isysroot{}", sdk_path))
        .clang_arg("-xobjective-c")
        .allowlist_type("CMItemIndex")
        .allowlist_type("CMSampleTimingInfo")
        .allowlist_type("CMVideoCodecType")
        .allowlist_type("VTEncodeInfoFlags")
        .allowlist_function("CMTimeMake")
        .allowlist_var("kCVPixelFormatType_.*")
        .allowlist_var("kCVReturn.*")
        .allowlist_var("VTEncodeInfoFlags_.*")
        .allowlist_var("kCMVideoCodecType_.*")
        .allowlist_var("kCMTime.*")
        .allowlist_var("kCMSampleAttachmentKey_.*")
        .parse_callbacks(Box::new(bindgen::CargoCallbacks::new()))
        .layout_tests(false)
        .generate()
        .expect("unable to generate bindings");

    let out_path = PathBuf::from(env::var("OUT_DIR").unwrap());
    bindings
        .write_to_file(out_path.join("bindings.rs"))
        .expect("couldn't write dispatch bindings");
}

#[cfg(not(target_os = "macos"))]
fn main() {
    // Cross-compiling to macOS on a non-macOS host: the target cfg
    // (target_os = "macos") is true, so bindings.rs expands include!.
    // Write stub definitions for the symbols media.rs re-exports.
    let out = std::env::var("OUT_DIR").unwrap();
    std::fs::write(
        format!("{out}/bindings.rs"),
        r#"
// Cross-compile stub: satisfies the include! in bindings.rs on non-macOS hosts.

pub type CMItemIndex = u32;
#[repr(C)]
#[derive(Debug, Copy, Clone)]
pub struct CMSampleTimingInfo {
    pub duration: CMTime,
    pub presentationTimeStamp: CMTime,
    pub decodeTimeStamp: CMTime,
}
#[repr(C)]
#[derive(Debug, Copy, Clone)]
pub struct CMTime { pub value: i64, pub timescale: i32, pub flags: u32, pub epoch: u64 }
#[repr(C)]
#[derive(Debug, Copy, Clone)]
pub struct CMVideoCodecType { _marker: [u8; 0] }
#[repr(C)]
#[derive(Debug, Copy, Clone)]
pub struct VTEncodeInfoFlags { _marker: [u8; 0] }
pub type CVReturn = i32;
unsafe extern "C" {
    pub fn CMTimeMake(value: i64, timescale: i32) -> CMTime;
    pub fn CMSampleBufferGetSampleTimingInfo(
        buffer: *mut std::os::raw::c_void,
        sample: CMItemIndex,
        out: *mut CMSampleTimingInfo,
    ) -> CVReturn;
}
pub const kCMTimeInvalid: CMTime = CMTime { value: -1, timescale: 0, flags: 0, epoch: 0 };
pub const kCMVideoCodecType_H264: u32 = 0;
pub const kCMSampleAttachmentKey_NotSync: u64 = 0;
pub const kCVReturnSuccess: CVReturn = 0;
pub const kCVPixelFormatType_32BGRA: u32 = 0;
pub const kCVPixelFormatType_420YpCbCr8BiPlanarFullRange: u32 = 0;
pub const kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange: u32 = 0;
pub const kCVPixelFormatType_420YpCbCr8Planar: u32 = 0;
"#,
    )
    .expect("write stub bindings.rs");
}