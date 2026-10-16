//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32 = windows.NewLazySystemDLL("user32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procGetLastError = kernel32.NewProc("GetLastError")
	procCloseHandle = kernel32.NewProc("CloseHandle")
	procFindWindowW = user32.NewProc("FindWindowW")
	procShowWindow = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const errorAlreadyExists = 183
const showNormal = 9

func acquireSingleInstance() (release func(), alreadyRunning bool) {
	name, _ := windows.UTF16PtrFromString("MeccyRadar.SingleInstance")
	handle, _, _ := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return func() {}, false
	}
	lastError, _, _ := procGetLastError.Call()
	if lastError == errorAlreadyExists {
		return func() { procCloseHandle.Call(handle) }, true
	}
	return func() { procCloseHandle.Call(handle) }, false
}

func activateExistingWindow() {
	title, _ := windows.UTF16PtrFromString("MeccyRadar")
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd != 0 {
		procShowWindow.Call(hwnd, showNormal)
		procSetForegroundWindow.Call(hwnd)
	}
}
