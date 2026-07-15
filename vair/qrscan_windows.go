//go:build windows

package main

// "Scan QR from screen / from file" — the GDI screen capture is ported
// verbatim from the 1.10 qr_windows.go; the image-file picker now uses the
// Wails v3 dialog instead of the raw OFN plumbing. Decoding lives in core.

import (
	"fmt"
	"image"
	"os"
	"syscall"
	"unsafe"

	"vair/core"
)

var (
	qrGdi32                 = syscall.NewLazyDLL("gdi32.dll")
	qrUser32                = syscall.NewLazyDLL("user32.dll")
	procQRGetDC             = qrUser32.NewProc("GetDC")
	procQRReleaseDC         = qrUser32.NewProc("ReleaseDC")
	procQRGetSystemMetrics  = qrUser32.NewProc("GetSystemMetrics")
	procQRCreateCompatDC    = qrGdi32.NewProc("CreateCompatibleDC")
	procQRCreateDIBSection  = qrGdi32.NewProc("CreateDIBSection")
	procQRSelectObject      = qrGdi32.NewProc("SelectObject")
	procQRBitBlt            = qrGdi32.NewProc("BitBlt")
	procQRDeleteObject      = qrGdi32.NewProc("DeleteObject")
	procQRDeleteDC          = qrGdi32.NewProc("DeleteDC")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
	srcCopy           = 0x00CC0020
)

type bitmapInfoHeaderQ struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfoQ struct {
	Header bitmapInfoHeaderQ
	Colors [1]uint32
}

func qrSysMetric(i int) int32 {
	r, _, _ := procQRGetSystemMetrics.Call(uintptr(i))
	return int32(r)
}

// captureVirtualScreen grabs the entire virtual desktop (all monitors) as an
// RGBA image. Falls back to the primary monitor if the virtual metrics are
// unavailable.
func captureVirtualScreen() (image.Image, error) {
	x, y := qrSysMetric(smXVirtualScreen), qrSysMetric(smYVirtualScreen)
	w, h := qrSysMetric(smCXVirtualScreen), qrSysMetric(smCYVirtualScreen)
	if w <= 0 || h <= 0 {
		w, h, x, y = qrSysMetric(0), qrSysMetric(1), 0, 0 // SM_CXSCREEN / SM_CYSCREEN
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("bad screen metrics %dx%d", w, h)
	}
	hScreen, _, _ := procQRGetDC.Call(0)
	if hScreen == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procQRReleaseDC.Call(0, hScreen)
	hMemDC, _, _ := procQRCreateCompatDC.Call(hScreen)
	if hMemDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procQRDeleteDC.Call(hMemDC)

	// CreateDIBSection hands us a bitmap backed by a pixel buffer we can read
	// directly. 32bpp BI_RGB, top-down (negative height) so row 0 is the top.
	var bi bitmapInfoQ
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = w
	bi.Header.Height = -h
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = 0 // BI_RGB

	var bits unsafe.Pointer
	hBmp, _, _ := procQRCreateDIBSection.Call(hMemDC, uintptr(unsafe.Pointer(&bi)),
		0 /*DIB_RGB_COLORS*/, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBmp == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection failed")
	}
	defer procQRDeleteObject.Call(hBmp)
	old, _, _ := procQRSelectObject.Call(hMemDC, hBmp)
	defer procQRSelectObject.Call(hMemDC, old)

	ret, _, _ := procQRBitBlt.Call(hMemDC, 0, 0, uintptr(w), uintptr(h), hScreen,
		uintptr(int(x)), uintptr(int(y)), srcCopy)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	// The DIB memory is BGRA, top-down; copy into a Go-owned RGBA image while
	// the bitmap is still alive (its buffer is freed by DeleteObject).
	n := int(w) * int(h)
	buf := unsafe.Slice((*byte)(bits), n*4)
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < n; i++ {
		img.Pix[i*4+0] = buf[i*4+2] // R
		img.Pix[i*4+1] = buf[i*4+1] // G
		img.Pix[i*4+2] = buf[i*4+0] // B
		img.Pix[i*4+3] = 255
	}
	return img, nil
}

// scanQRFromFile opens the image picker, reads the chosen file and decodes a
// QR. Returns ("", nil) when the user cancels.
func scanQRFromFile() (string, error) {
	paths, err := theApp.Dialog.OpenFile().
		SetTitle("Select a QR image").
		CanChooseFiles(true).
		AddFilter("Images (*.png;*.jpg;*.jpeg;*.gif;*.bmp)", "*.png;*.jpg;*.jpeg;*.gif;*.bmp").
		AddFilter("All files (*.*)", "*.*").
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return "", nil
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return "", err
	}
	return core.DecodeQRBytes(data)
}

// scanQRFromScreen captures the virtual desktop and decodes a QR found on it.
func scanQRFromScreen() (string, error) {
	img, err := captureVirtualScreen()
	if err != nil {
		return "", err
	}
	return core.DecodeQRImage(img)
}
