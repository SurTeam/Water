"""Preserve native clipboard formats in memory during an owned desktop test."""
from contextlib import contextmanager
import ctypes as c
from ctypes import wintypes as w
import time
import threading
import queue

@contextmanager
def temporary_text(text):
    user=c.WinDLL('user32',use_last_error=True)
    kernel=c.WinDLL('kernel32',use_last_error=True)
    gdi=c.WinDLL('gdi32',use_last_error=True)
    user.CreateWindowExW.argtypes=[w.DWORD,w.LPCWSTR,w.LPCWSTR,w.DWORD,c.c_int,c.c_int,c.c_int,c.c_int,w.HWND,w.HMENU,w.HINSTANCE,c.c_void_p]
    user.CreateWindowExW.restype=w.HWND
    user.OpenClipboard.argtypes=[w.HWND]
    user.GetClipboardData.argtypes=[w.UINT];user.GetClipboardData.restype=w.HANDLE
    user.SetClipboardData.argtypes=[w.UINT,w.HANDLE];user.SetClipboardData.restype=w.HANDLE
    user.EnumClipboardFormats.argtypes=[w.UINT];user.EnumClipboardFormats.restype=w.UINT
    user.CopyImage.argtypes=[w.HANDLE,w.UINT,c.c_int,c.c_int,w.UINT];user.CopyImage.restype=w.HANDLE
    user.DestroyWindow.argtypes=[w.HWND]
    kernel.GlobalAlloc.argtypes=[w.UINT,c.c_size_t];kernel.GlobalAlloc.restype=w.HANDLE
    kernel.GlobalSize.argtypes=[w.HANDLE];kernel.GlobalSize.restype=c.c_size_t
    kernel.GlobalLock.argtypes=[w.HANDLE];kernel.GlobalLock.restype=c.c_void_p
    kernel.GlobalUnlock.argtypes=[w.HANDLE]
    kernel.GlobalFree.argtypes=[w.HANDLE]
    gdi.CopyEnhMetaFileW.argtypes=[w.HANDLE,w.LPCWSTR];gdi.CopyEnhMetaFileW.restype=w.HANDLE
    gdi.DeleteEnhMetaFile.argtypes=[w.HANDLE]
    gdi.DeleteObject.argtypes=[w.HANDLE]
    # Clipboard replacement synchronously notifies its owner window. Keep that
    # window on a pumping thread while the main test waits for Water RPCs.
    user.GetMessageW.argtypes=[c.POINTER(w.MSG),w.HWND,w.UINT,w.UINT]
    user.TranslateMessage.argtypes=[c.POINTER(w.MSG)]
    user.DispatchMessageW.argtypes=[c.POINTER(w.MSG)]
    user.DispatchMessageW.restype=c.c_ssize_t
    user.PostThreadMessageW.argtypes=[w.DWORD,w.UINT,w.WPARAM,w.LPARAM]
    initialized=queue.Queue()
    def window_loop():
        window=user.CreateWindowExW(0,'STATIC','Water clipboard fixture',0,0,0,0,0,w.HWND(-3),None,None,None)
        if not window:
            initialized.put(c.WinError(c.get_last_error()))
            return
        initialized.put((window,kernel.GetCurrentThreadId()))
        message=w.MSG()
        try:
            while user.GetMessageW(c.byref(message),None,0,0)>0:
                user.TranslateMessage(c.byref(message))
                user.DispatchMessageW(c.byref(message))
        finally:user.DestroyWindow(window)
    worker=threading.Thread(target=window_loop,daemon=True)
    worker.start()
    initialization=initialized.get(timeout=5)
    if isinstance(initialization,Exception):raise initialization
    owner,thread_id=initialization
    def opened():
        deadline=time.monotonic()+3
        while not user.OpenClipboard(owner):
            if time.monotonic()>=deadline:
                error=c.get_last_error()
                user.GetOpenClipboardWindow.restype=w.HWND
                pid=w.DWORD()
                user.GetWindowThreadProcessId.argtypes=[w.HWND,c.POINTER(w.DWORD)]
                clipboard_owner=user.GetOpenClipboardWindow()
                user.GetWindowThreadProcessId(clipboard_owner,c.byref(pid))
                raise RuntimeError(f'native clipboard unavailable: error={error} owner_pid={pid.value}')
            time.sleep(.025)
    def allocated(data):
        handle=kernel.GlobalAlloc(2,len(data))
        if not handle:raise MemoryError('clipboard allocation')
        pointer=kernel.GlobalLock(handle)
        if not pointer:
            kernel.GlobalFree(handle)
            raise c.WinError(c.get_last_error())
        c.memmove(pointer,data,len(data));kernel.GlobalUnlock(handle)
        return handle
    def dispose(fmt,handle):
        if fmt in (2,0x82):gdi.DeleteObject(handle)
        elif fmt in (14,0x8e):gdi.DeleteEnhMetaFile(handle)
        else:kernel.GlobalFree(handle)
    saved=[];changed=False
    try:
        opened()
        try:
            fmt=0
            while True:
                c.set_last_error(0)
                fmt=user.EnumClipboardFormats(fmt)
                if not fmt:
                    if c.get_last_error():raise c.WinError(c.get_last_error())
                    break
                original=user.GetClipboardData(fmt)
                if not original:raise RuntimeError('clipboard format cannot be materialized: '+str(fmt))
                if fmt in (2,0x82):copy=user.CopyImage(original,0,0,0,0x2000)
                elif fmt in (14,0x8e):copy=gdi.CopyEnhMetaFileW(original,None)
                else:
                    if fmt in (3,9,0x80,0x83):raise RuntimeError('clipboard format cannot be safely cloned: '+str(fmt))
                    size=kernel.GlobalSize(original)
                    if not size:raise RuntimeError('clipboard format is not global memory: '+str(fmt))
                    pointer=kernel.GlobalLock(original)
                    if not pointer:raise c.WinError(c.get_last_error())
                    try:copy=allocated(c.string_at(pointer,size))
                    finally:kernel.GlobalUnlock(original)
                if not copy:raise RuntimeError('clipboard duplication failed: '+str(fmt))
                saved.append([fmt,copy])
            fixture=allocated((text+'\0').encode('utf-16le'))
            if not user.EmptyClipboard():
                kernel.GlobalFree(fixture)
                raise RuntimeError('EmptyClipboard failed')
            changed=True
            if not user.SetClipboardData(13,fixture):
                kernel.GlobalFree(fixture)
                raise RuntimeError('SetClipboardData failed')
        finally:user.CloseClipboard()
        yield {'status':'passed','preserved_formats':[fmt for fmt,_ in saved]}
    finally:
        try:
            if changed:
                opened()
                try:
                    if not user.EmptyClipboard():raise RuntimeError('clipboard restoration failed')
                    for item in saved:
                        if not user.SetClipboardData(*item):raise RuntimeError('clipboard format restore failed: '+str(item[0]))
                        item[1]=None
                finally:user.CloseClipboard()
        finally:
            for fmt,handle in saved:
                if handle:dispose(fmt,handle)
            user.PostThreadMessageW(thread_id,0x12,0,0)
            worker.join(timeout=3)
            if worker.is_alive():raise RuntimeError('clipboard owner window did not stop')


def read_text():
    """Read only the test's expected clipboard text; callers never log it."""
    user=c.WinDLL('user32',use_last_error=True)
    kernel=c.WinDLL('kernel32',use_last_error=True)
    user.OpenClipboard.argtypes=[w.HWND]
    user.GetClipboardData.argtypes=[w.UINT];user.GetClipboardData.restype=w.HANDLE
    kernel.GlobalLock.argtypes=[w.HANDLE];kernel.GlobalLock.restype=c.c_void_p
    kernel.GlobalUnlock.argtypes=[w.HANDLE]
    if not user.OpenClipboard(None):raise c.WinError(c.get_last_error())
    try:
        handle=user.GetClipboardData(13)
        if not handle:return ''
        pointer=kernel.GlobalLock(handle)
        if not pointer:raise c.WinError(c.get_last_error())
        try:return c.wstring_at(pointer)
        finally:kernel.GlobalUnlock(handle)
    finally:user.CloseClipboard()
