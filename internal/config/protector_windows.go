//go:build windows

package config

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProtector struct{}

func NewPlatformProtector(serviceName string) (Protector, error) {
	if serviceName == "" {
		return nil, errors.New("缺少服务名称")
	}
	return windowsProtector{}, nil
}

func (windowsProtector) Protect(plain []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(plain))}
	if len(plain) > 0 {
		in.Data = &plain[0]
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return copyDataBlob(out), nil
}

func (windowsProtector) Unprotect(protected []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(protected))}
	if len(protected) > 0 {
		in.Data = &protected[0]
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return copyDataBlob(out), nil
}

func copyDataBlob(blob windows.DataBlob) []byte {
	if blob.Data == nil || blob.Size == 0 {
		return nil
	}
	return unsafe.Slice(blob.Data, blob.Size)
}
