package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	trayCallbackMessage = 0x8000 + 0x51
	wmDestroy           = 0x0002
	wmClose             = 0x0010
	wmLButtonDblClk     = 0x0203
	wmRButtonUp         = 0x0205

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nimAdd     = 0x00000000
	nimDelete  = 0x00000002

	mfString     = 0x00000000
	mfSeparator  = 0x00000800
	tpmRightBtn  = 0x0002
	tpmReturnCmd = 0x0100

	idiApplication = 32512

	trayOpen   = 1001
	trayToggle = 1002
	trayUpdate = 1003
	trayQuit   = 1004
)

type trayPoint struct{ X, Y int32 }
type trayMessage struct {
	HWnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       trayPoint
	LPrivate uint32
}
type trayWndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}
type trayNotifyIconData struct {
	CbSize           uint32
	HWnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

type windowsTray struct {
	app     *App
	hwnd    windows.Handle
	wndProc uintptr
	ready   chan error
	closed  chan struct{}
}

var (
	trayUser32   = windows.NewLazySystemDLL("user32.dll")
	trayShell32  = windows.NewLazySystemDLL("shell32.dll")
	trayKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW    = trayUser32.NewProc("RegisterClassExW")
	procCreateWindowExW     = trayUser32.NewProc("CreateWindowExW")
	procDefWindowProcW      = trayUser32.NewProc("DefWindowProcW")
	procGetMessageW         = trayUser32.NewProc("GetMessageW")
	procTranslateMessage    = trayUser32.NewProc("TranslateMessage")
	procDispatchMessageW    = trayUser32.NewProc("DispatchMessageW")
	procPostQuitMessage     = trayUser32.NewProc("PostQuitMessage")
	procPostMessageW        = trayUser32.NewProc("PostMessageW")
	procDestroyWindow       = trayUser32.NewProc("DestroyWindow")
	procLoadIconW           = trayUser32.NewProc("LoadIconW")
	procCreatePopupMenu     = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenuW         = trayUser32.NewProc("AppendMenuW")
	procTrackPopupMenu      = trayUser32.NewProc("TrackPopupMenu")
	procDestroyMenu         = trayUser32.NewProc("DestroyMenu")
	procGetCursorPos        = trayUser32.NewProc("GetCursorPos")
	procSetForegroundWindow = trayUser32.NewProc("SetForegroundWindow")
	procShellNotifyIconW    = trayShell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW    = trayKernel32.NewProc("GetModuleHandleW")
)

func (a *App) startTray() {
	a.mu.Lock()
	if a.tray != nil {
		a.mu.Unlock()
		return
	}
	tray := &windowsTray{app: a, ready: make(chan error, 1), closed: make(chan struct{})}
	a.tray = tray
	a.mu.Unlock()
	go tray.run()
	if err := <-tray.ready; err != nil {
		a.mu.Lock()
		if a.tray == tray {
			a.tray = nil
		}
		a.appendLogLocked("系统托盘初始化失败：" + err.Error())
		a.mu.Unlock()
		a.emitState()
	}
}

func (t *windowsTray) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.closed)

	className, _ := windows.UTF16PtrFromString("MPTCPDeskTrayWindow")
	module, _, errCall := procGetModuleHandleW.Call(0)
	if module == 0 {
		t.ready <- fmt.Errorf("GetModuleHandleW: %v", errCall)
		return
	}
	t.wndProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
		switch msg {
		case trayCallbackMessage:
			switch uint32(lparam) {
			case wmLButtonDblClk:
				go t.app.ShowWindow()
				return 0
			case wmRButtonUp:
				t.showMenu()
				return 0
			}
		case wmClose:
			t.removeIcon()
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}
		result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
		return result
	})
	wc := trayWndClassEx{CbSize: uint32(unsafe.Sizeof(trayWndClassEx{})), LpfnWndProc: t.wndProc, HInstance: windows.Handle(module), LpszClassName: className}
	atom, _, registerErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		t.ready <- fmt.Errorf("RegisterClassExW: %v", registerErr)
		return
	}
	hwnd, _, createErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, module, 0)
	if hwnd == 0 {
		t.ready <- fmt.Errorf("CreateWindowExW: %v", createErr)
		return
	}
	t.hwnd = windows.Handle(hwnd)
	if err := t.addIcon(); err != nil {
		t.ready <- err
		procDestroyWindow.Call(hwnd)
		return
	}
	t.ready <- nil
	var msg trayMessage
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (t *windowsTray) addIcon() error {
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	if icon == 0 {
		return fmt.Errorf("LoadIconW failed")
	}
	data := trayNotifyIconData{CbSize: uint32(unsafe.Sizeof(trayNotifyIconData{})), HWnd: t.hwnd, UID: 1, UFlags: nifMessage | nifIcon | nifTip, UCallbackMessage: trayCallbackMessage, HIcon: windows.Handle(icon)}
	tip, _ := windows.UTF16FromString("MPTCP Desk · MPX/4 Userspace")
	copy(data.SzTip[:], tip)
	ok, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	if ok == 0 {
		return fmt.Errorf("Shell_NotifyIconW: %v", e)
	}
	return nil
}

func (t *windowsTray) removeIcon() {
	if t.hwnd == 0 {
		return
	}
	data := trayNotifyIconData{CbSize: uint32(unsafe.Sizeof(trayNotifyIconData{})), HWnd: t.hwnd, UID: 1}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func (t *windowsTray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendItem := func(flags, id uintptr, label string) {
		var ptr *uint16
		if label != "" {
			ptr, _ = windows.UTF16PtrFromString(label)
		}
		procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(ptr)))
	}
	appendItem(mfString, trayOpen, "打开 MPTCP Desk")
	t.app.mu.Lock()
	running := t.app.state.Running || t.app.state.Busy
	t.app.mu.Unlock()
	if running {
		appendItem(mfString, trayToggle, "停止转发")
	} else {
		appendItem(mfString, trayToggle, "启动转发")
	}
	appendItem(mfString, trayUpdate, "检查更新")
	appendItem(mfSeparator, 0, "")
	appendItem(mfString, trayQuit, "退出")
	var pt trayPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(t.hwnd))
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightBtn|tpmReturnCmd, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(t.hwnd), 0)
	switch command {
	case trayOpen:
		go t.app.ShowWindow()
	case trayToggle:
		go func() {
			if running {
				_ = t.app.Stop()
			} else {
				_ = t.app.Start()
			}
		}()
	case trayUpdate:
		go func() { _, _ = t.app.CheckForUpdates(true) }()
	case trayQuit:
		go t.app.Quit()
	}
}

func (t *windowsTray) Close() {
	if t == nil || t.hwnd == 0 {
		return
	}
	procPostMessageW.Call(uintptr(t.hwnd), wmClose, 0, 0)
	select {
	case <-t.closed:
	default:
	}
}
