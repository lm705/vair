//go:build windows

package main

import (
	"fmt"
	"image"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── GDI screen-capture for "scan QR from screen" ───────────────────────────
// Reuses user32 (declared in standalone_windows.go) for GetDC/ReleaseDC and the
// SM_* metrics; adds the gdi32 BitBlt/GetDIBits chain to grab the whole virtual
// desktop into an image we hand to the QR decoder.
var (
	gdi32q                  = windows.NewLazySystemDLL("gdi32.dll")
	procGetDCq              = user32.NewProc("GetDC")
	procReleaseDCq          = user32.NewProc("ReleaseDC")
	procCreateCompatibleDCq = gdi32q.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = gdi32q.NewProc("CreateDIBSection")
	procSelectObjectq       = gdi32q.NewProc("SelectObject")
	procBitBltq             = gdi32q.NewProc("BitBlt")
	procDeleteObjectq       = gdi32q.NewProc("DeleteObject")
	procDeleteDCq           = gdi32q.NewProc("DeleteDC")
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

func sysMetric(i int) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int32(r)
}

// captureVirtualScreen grabs the entire virtual desktop (all monitors) as an
// RGBA image. Falls back to the primary monitor if the virtual metrics are
// unavailable.
func captureVirtualScreen() (image.Image, error) {
	x, y := sysMetric(smXVirtualScreen), sysMetric(smYVirtualScreen)
	w, h := sysMetric(smCXVirtualScreen), sysMetric(smCYVirtualScreen)
	if w <= 0 || h <= 0 {
		w, h, x, y = sysMetric(0), sysMetric(1), 0, 0 // SM_CXSCREEN / SM_CYSCREEN
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("bad screen metrics %dx%d", w, h)
	}
	hScreen, _, _ := procGetDCq.Call(0)
	if hScreen == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDCq.Call(0, hScreen)
	hMemDC, _, _ := procCreateCompatibleDCq.Call(hScreen)
	if hMemDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDCq.Call(hMemDC)

	// CreateDIBSection hands us a bitmap backed by a pixel buffer we can read
	// directly — no GetDIBits (which is finicky about the bitmap being selected).
	// 32bpp BI_RGB, top-down (negative height) so row 0 is the top.
	var bi bitmapInfoQ
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = w
	bi.Header.Height = -h
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = 0 // BI_RGB

	var bits unsafe.Pointer
	hBmp, _, _ := procCreateDIBSection.Call(hMemDC, uintptr(unsafe.Pointer(&bi)),
		0 /*DIB_RGB_COLORS*/, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBmp == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection failed")
	}
	defer procDeleteObjectq.Call(hBmp)
	old, _, _ := procSelectObjectq.Call(hMemDC, hBmp)
	defer procSelectObjectq.Call(hMemDC, old)

	ret, _, _ := procBitBltq.Call(hMemDC, 0, 0, uintptr(w), uintptr(h), hScreen,
		uintptr(int(x)), uintptr(int(y)), srcCopy)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	// The DIB memory is BGRA, top-down; copy it into a Go-owned RGBA image while
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

// pickImageFile shows the native open dialog filtered to images and returns the
// chosen path ("" on cancel). Reuses the OFN plumbing from standalone_windows.go.
func pickImageFile(ownerHwnd uintptr) string {
	const bufLen = 4096
	buf := make([]uint16, bufLen)
	filter := utf16Z("Images\x00*.png;*.jpg;*.jpeg;*.gif;*.bmp\x00All files\x00*.*\x00")
	title := utf16Z("Select a QR image")
	ofn := openFileNameW{
		Owner: ownerHwnd, Filter: &filter[0], File: &buf[0], MaxFile: bufLen,
		Title: &title[0],
		Flags: ofnExplorer | ofnPathMustExist | ofnFileMustExist | ofnHideReadOnly | ofnNoChangeDir,
	}
	ofn.StructSize = uint32(unsafe.Sizeof(ofn))
	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// scanQRFromFile opens the picker, reads the chosen image and decodes a QR.
// Returns ("", nil) when the user cancels.
func scanQRFromFile(ownerHwnd uintptr) (string, error) {
	path := pickImageFile(ownerHwnd)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return decodeQRBytes(data)
}

// scanQRFromScreen captures the virtual desktop and decodes a QR found on it.
func scanQRFromScreen() (string, error) {
	img, err := captureVirtualScreen()
	if err != nil {
		return "", err
	}
	return decodeQRImage(img)
}
