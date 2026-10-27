package wfp

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows Filtering Platform, spoken to directly.
//
// The kill-switch used to be built out of Windows Firewall: flip the default
// outbound action to Block and add allow rules for what must keep moving. It
// has a hole the size of the firewall's own policy. Windows Firewall lets any
// matching allow rule win over the default action, and a stock Windows 10
// arrives with close to a hundred enabled outbound allow rules that reach the
// internet — the DNS client service, telemetry, backup, Store apps with any
// port and any protocol. Measured in the test VM: with the tunnel gone and the
// block standing, curl was refused and a lookup through the system resolver
// still reached 8.8.8.8. Every name the machine asked for left in the clear.
//
// WFP is the engine Windows Firewall itself is built on, and it does not have
// that problem. Filters live in sublayers; a Block in any sublayer beats a
// Permit in another, so allow rules written by Windows Firewall — its own or
// anyone's — cannot open a door our sublayer has shut. And every change is made
// inside a transaction: the whole set appears at once or not at all, and one
// that is never committed is rolled back by the system, even if this process
// dies halfway. Mullvad, WireGuard for Windows and Tailscale all do it this way.
//
// Only the calls the kill-switch needs are bound here. The structures follow
// fwpmtypes.h exactly — their sizes are checked by a test, because a wrong
// offset here is not an error message, it is a filter that means something
// else.

var (
	modFwpuclnt = windows.NewLazySystemDLL("fwpuclnt.dll")

	procFwpmEngineOpen0           = modFwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmEngineClose0          = modFwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmTransactionBegin0     = modFwpuclnt.NewProc("FwpmTransactionBegin0")
	procFwpmTransactionCommit0    = modFwpuclnt.NewProc("FwpmTransactionCommit0")
	procFwpmTransactionAbort0     = modFwpuclnt.NewProc("FwpmTransactionAbort0")
	procFwpmProviderAdd0          = modFwpuclnt.NewProc("FwpmProviderAdd0")
	procFwpmProviderDeleteByKey0  = modFwpuclnt.NewProc("FwpmProviderDeleteByKey0")
	procFwpmSubLayerAdd0          = modFwpuclnt.NewProc("FwpmSubLayerAdd0")
	procFwpmSubLayerDeleteByKey0  = modFwpuclnt.NewProc("FwpmSubLayerDeleteByKey0")
	procFwpmFilterAdd0            = modFwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmFilterDeleteByKey0    = modFwpuclnt.NewProc("FwpmFilterDeleteByKey0")
	procFwpmFilterGetByKey0       = modFwpuclnt.NewProc("FwpmFilterGetByKey0")
	procFwpmGetAppIdFromFileName0 = modFwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
	procFwpmFreeMemory0           = modFwpuclnt.NewProc("FwpmFreeMemory0")
)

// ── structures (fwpmtypes.h / fwptypes.h) ───────────────────────────────────

type fwpmDisplayData0 struct {
	name        *uint16
	description *uint16
}

type fwpByteBlob struct {
	size uint32
	data *uint8
}

type fwpmSession0 struct {
	sessionKey           windows.GUID
	displayData          fwpmDisplayData0
	flags                uint32
	txnWaitTimeoutInMSec uint32
	processID            uint32
	sid                  *windows.SID
	username             *uint16
	kernelMode           int32
}

type fwpmProvider0 struct {
	providerKey  windows.GUID
	displayData  fwpmDisplayData0
	flags        uint32
	providerData fwpByteBlob
	serviceName  *uint16
}

type fwpmSublayer0 struct {
	subLayerKey  windows.GUID
	displayData  fwpmDisplayData0
	flags        uint32
	providerKey  *windows.GUID
	providerData fwpByteBlob
	weight       uint16
}

// fwpValue0 and fwpConditionValue0 are a type tag and a pointer-sized union.
// A 64-bit value is carried by POINTER, like every value wider than 32 bits.
type fwpValue0 struct {
	kind  uint32
	value uintptr
}

type fwpConditionValue0 struct {
	kind  uint32
	value uintptr
}

type fwpmFilterCondition0 struct {
	fieldKey       windows.GUID
	matchType      uint32
	conditionValue fwpConditionValue0
}

type fwpmAction0 struct {
	kind       uint32
	filterType windows.GUID
}

type fwpmFilter0 struct {
	filterKey           windows.GUID
	displayData         fwpmDisplayData0
	flags               uint32
	providerKey         *windows.GUID
	providerData        fwpByteBlob
	layerKey            windows.GUID
	subLayerKey         windows.GUID
	weight              fwpValue0
	numFilterConditions uint32
	filterCondition     *fwpmFilterCondition0
	action              fwpmAction0
	_                   [4]byte // the next union holds a UINT64: 8-aligned
	providerContextKey  windows.GUID
	reserved            *windows.GUID
	filterID            uint64
	effectiveWeight     fwpValue0
}

type fwpV4AddrAndMask struct {
	addr uint32 // host byte order, as WFP wants it
	mask uint32
}

type fwpV6AddrAndMask struct {
	addr         [16]byte
	prefixLength uint8
}

// ── constants ────────────────────────────────────────────────────────────────

const (
	rpcCAuthnWinNT = 10

	fwpEmpty            = 0
	fwpUint8            = 1
	fwpUint16           = 2
	fwpUint32           = 3
	fwpUint64           = 4
	fwpByteBlobType     = 12
	fwpV4AddrMask       = 0x100
	fwpV6AddrMask       = 0x101
	fwpMatchEqual       = 0
	fwpMatchFlagsAllSet = 6

	fwpActionBlock  = 0x1001 // FWP_ACTION_BLOCK  | FWP_ACTION_FLAG_TERMINATING
	fwpActionPermit = 0x1002 // FWP_ACTION_PERMIT | FWP_ACTION_FLAG_TERMINATING

	fwpmFilterFlagPersistent   = 0x1
	fwpmProviderFlagPersistent = 0x1
	fwpmSublayerFlagPersistent = 0x1
	fwpmSessionFlagDynamic     = 0x1

	fwpConditionFlagIsLoopback = 0x1

	fwpEAlreadyExists     = 0x80320009
	fwpEFilterNotFound    = 0x80320003
	fwpEProviderNotFound  = 0x80320005
	fwpESublayerNotFound  = 0x80320007
	fwpEInUse             = 0x8032000A
	fwpENotFound          = 0x80320008
	wfpTxnTimeoutMillisec = 10000
)

var (
	fwpmLayerALEAuthConnectV4 = windows.GUID{Data1: 0xc38d57d1, Data2: 0x05a7, Data3: 0x4c33, Data4: [8]byte{0x90, 0x4f, 0x7f, 0xbc, 0xee, 0xe6, 0x0e, 0x82}}
	fwpmLayerALEAuthConnectV6 = windows.GUID{Data1: 0x4a72393b, Data2: 0x319f, Data3: 0x44bc, Data4: [8]byte{0x84, 0xc3, 0xba, 0x54, 0xdc, 0xb3, 0xb6, 0xb4}}
	// FWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4/V6: a connection accepted from outside.
	fwpmLayerALEAuthRecvAcceptV4 = windows.GUID{Data1: 0xe1cd9fe7, Data2: 0xf4b5, Data3: 0x4273, Data4: [8]byte{0x96, 0xc0, 0x59, 0x2e, 0x48, 0x7b, 0x86, 0x50}}
	fwpmLayerALEAuthRecvAcceptV6 = windows.GUID{Data1: 0xa3b42c97, Data2: 0x9f04, Data3: 0x4672, Data4: [8]byte{0xb8, 0x7e, 0xce, 0xe9, 0xc4, 0x83, 0x25, 0x7f}}

	fwpmConditionIPProtocol       = windows.GUID{Data1: 0x3971ef2b, Data2: 0x623e, Data3: 0x4f9a, Data4: [8]byte{0x8c, 0xb1, 0x6e, 0x79, 0xb8, 0x06, 0xb9, 0xa7}}
	fwpmConditionIPRemoteAddress  = windows.GUID{Data1: 0xb235ae9a, Data2: 0x1d64, Data3: 0x49b8, Data4: [8]byte{0xa4, 0x4c, 0x5f, 0xf3, 0xd9, 0x09, 0x50, 0x45}}
	fwpmConditionIPLocalPort      = windows.GUID{Data1: 0x0c1ba1af, Data2: 0x5765, Data3: 0x453f, Data4: [8]byte{0xaf, 0x22, 0xa8, 0xf7, 0x91, 0xac, 0x77, 0x5b}}
	fwpmConditionIPRemotePort     = windows.GUID{Data1: 0xc35a604d, Data2: 0xd22b, Data3: 0x4e1a, Data4: [8]byte{0x91, 0xb4, 0x68, 0xf6, 0x74, 0xee, 0x67, 0x4b}}
	fwpmConditionIPLocalInterface = windows.GUID{Data1: 0x4cd62a49, Data2: 0x59c3, Data3: 0x4969, Data4: [8]byte{0xb7, 0xf3, 0xbd, 0xa5, 0xd3, 0x28, 0x90, 0xa4}}
	fwpmConditionALEAppID         = windows.GUID{Data1: 0xd78e1e87, Data2: 0x8644, Data3: 0x4ea5, Data4: [8]byte{0x94, 0x37, 0xd8, 0x09, 0xec, 0xef, 0xc9, 0x71}}
	fwpmConditionFlags            = windows.GUID{Data1: 0x632ce23b, Data2: 0x5167, Data3: 0x435c, Data4: [8]byte{0x86, 0xd7, 0xe9, 0x03, 0x68, 0x4a, 0xa8, 0x0c}}
)

// wfpError is a failed WFP call, with the HRESULT it returned.
type wfpError struct {
	op   string
	code uint32
}

func (e *wfpError) Error() string {
	return fmt.Sprintf("%s: 0x%08X (%v)", e.op, e.code, windows.Errno(e.code))
}

func wfpErrCode(err error) uint32 {
	var we *wfpError
	if errors.As(err, &we) {
		return we.code
	}
	return 0
}

func wfpCall(op string, p *windows.LazyProc, args ...uintptr) error {
	if err := p.Find(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	r, _, _ := p.Call(args...)
	if r != 0 {
		return &wfpError{op: op, code: uint32(r)}
	}
	return nil
}

// ── engine and transactions ─────────────────────────────────────────────────

type wfpEngine uintptr

func wfpOpen() (wfpEngine, error) { return wfpOpenSession(false) }

// wfpOpenSession opens the filtering engine. A dynamic session owns what it
// adds: the engine deletes all of it when the session is closed, or when the
// process holding it dies — whichever comes first.
func wfpOpenSession(dynamic bool) (wfpEngine, error) {
	name, _ := windows.UTF16PtrFromString("Vair kill-switch")
	session := fwpmSession0{
		displayData: fwpmDisplayData0{name: name},
		// Not INFINITE: a filtering engine that has stopped answering must
		// come back as an error, not hang the program that asked.
		txnWaitTimeoutInMSec: wfpTxnTimeoutMillisec,
	}
	if dynamic {
		session.flags = fwpmSessionFlagDynamic
	}
	var h uintptr
	err := wfpCall("FwpmEngineOpen0", procFwpmEngineOpen0,
		0, rpcCAuthnWinNT, 0, uintptr(unsafe.Pointer(&session)), uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(name)
	if err != nil {
		return 0, err
	}
	return wfpEngine(h), nil
}

func (e wfpEngine) close() {
	procFwpmEngineClose0.Call(uintptr(e)) //nolint:errcheck
}

// inTransaction runs fn inside one WFP transaction: committed when fn returns
// nil, rolled back otherwise — so whatever fn managed to add before failing
// never takes effect.
func (e wfpEngine) inTransaction(fn func() error) error {
	if err := wfpCall("FwpmTransactionBegin0", procFwpmTransactionBegin0, uintptr(e), 0); err != nil {
		return err
	}
	if err := fn(); err != nil {
		procFwpmTransactionAbort0.Call(uintptr(e)) //nolint:errcheck
		return err
	}
	return wfpCall("FwpmTransactionCommit0", procFwpmTransactionCommit0, uintptr(e))
}

// ── objects ──────────────────────────────────────────────────────────────────

func (e wfpEngine) addProvider(key windows.GUID, name string) error {
	n, _ := windows.UTF16PtrFromString(name)
	p := fwpmProvider0{providerKey: key, displayData: fwpmDisplayData0{name: n}, flags: fwpmProviderFlagPersistent}
	err := wfpCall("FwpmProviderAdd0", procFwpmProviderAdd0, uintptr(e), uintptr(unsafe.Pointer(&p)), 0)
	runtime.KeepAlive(n)
	if wfpErrCode(err) == fwpEAlreadyExists {
		return nil
	}
	return err
}

// addSublayer adds a sublayer of the highest weight. provider may be nil, and
// flags is 0 for an object that lives only as long as a dynamic session.
func (e wfpEngine) addSublayer(key windows.GUID, provider *windows.GUID, name string, flags uint32) error {
	n, _ := windows.UTF16PtrFromString(name)
	s := fwpmSublayer0{
		subLayerKey: key,
		displayData: fwpmDisplayData0{name: n},
		flags:       flags,
		providerKey: provider,
		weight:      0xffff,
	}
	err := wfpCall("FwpmSubLayerAdd0", procFwpmSubLayerAdd0, uintptr(e), uintptr(unsafe.Pointer(&s)), 0)
	runtime.KeepAlive(n)
	runtime.KeepAlive(provider)
	if wfpErrCode(err) == fwpEAlreadyExists {
		return nil
	}
	return err
}

func (e wfpEngine) deleteSublayer(key windows.GUID) error {
	err := wfpCall("FwpmSubLayerDeleteByKey0", procFwpmSubLayerDeleteByKey0, uintptr(e), uintptr(unsafe.Pointer(&key)))
	switch wfpErrCode(err) {
	case fwpESublayerNotFound, fwpENotFound:
		return nil
	}
	return err
}

func (e wfpEngine) deleteProvider(key windows.GUID) error {
	err := wfpCall("FwpmProviderDeleteByKey0", procFwpmProviderDeleteByKey0, uintptr(e), uintptr(unsafe.Pointer(&key)))
	switch wfpErrCode(err) {
	case fwpEProviderNotFound, fwpENotFound:
		return nil
	}
	return err
}

func (e wfpEngine) deleteFilter(key windows.GUID) error {
	err := wfpCall("FwpmFilterDeleteByKey0", procFwpmFilterDeleteByKey0, uintptr(e), uintptr(unsafe.Pointer(&key)))
	switch wfpErrCode(err) {
	case fwpEFilterNotFound, fwpENotFound:
		return nil
	}
	return err
}

func (e wfpEngine) filterExists(key windows.GUID) (bool, error) {
	var f *fwpmFilter0
	err := wfpCall("FwpmFilterGetByKey0", procFwpmFilterGetByKey0,
		uintptr(e), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&f)))
	if err == nil {
		procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&f))) //nolint:errcheck
		return true, nil
	}
	switch wfpErrCode(err) {
	case fwpEFilterNotFound, fwpENotFound:
		return false, nil
	}
	return false, err
}

// wfpAppID asks WFP for the identifier it matches a program by — the file's
// device path, which is what a filter on FWPM_CONDITION_ALE_APP_ID compares.
// The result is WFP's memory: release it with the returned function once the
// filter that uses it has been added.
func wfpAppID(path string) (*fwpByteBlob, func(), error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	var blob *fwpByteBlob
	if err := wfpCall("FwpmGetAppIdFromFileName0", procFwpmGetAppIdFromFileName0,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&blob))); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return blob, func() { procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&blob))) }, nil //nolint:errcheck
}

// ── filters ──────────────────────────────────────────────────────────────────

// wfpCond is one condition of a filter, in a form a test can read.
type wfpCond struct {
	field  windows.GUID
	match  uint32
	kind   uint32
	u8     uint8
	u16    uint16
	u32    uint32
	u64    uint64
	app    string       // for an application condition: the program's path
	prefix netip.Prefix // for an address condition
}

// wfpFilterSpec is one filter as the policy decides it.
type wfpFilterSpec struct {
	key    windows.GUID
	name   string
	layer  windows.GUID
	action uint32
	weight uint8
	conds  []wfpCond
}

func condProtocol(proto uint8) wfpCond {
	return wfpCond{field: fwpmConditionIPProtocol, match: fwpMatchEqual, kind: fwpUint8, u8: proto}
}

func condRemotePort(port uint16) wfpCond {
	return wfpCond{field: fwpmConditionIPRemotePort, match: fwpMatchEqual, kind: fwpUint16, u16: port}
}

func condLocalPort(port uint16) wfpCond {
	return wfpCond{field: fwpmConditionIPLocalPort, match: fwpMatchEqual, kind: fwpUint16, u16: port}
}

func condInterface(luid uint64) wfpCond {
	return wfpCond{field: fwpmConditionIPLocalInterface, match: fwpMatchEqual, kind: fwpUint64, u64: luid}
}

func condApp(path string) wfpCond {
	return wfpCond{field: fwpmConditionALEAppID, match: fwpMatchEqual, kind: fwpByteBlobType, app: path}
}

func condLoopback() wfpCond {
	return wfpCond{field: fwpmConditionFlags, match: fwpMatchFlagsAllSet, kind: fwpUint32, u32: fwpConditionFlagIsLoopback}
}

func condRemotePrefix(p netip.Prefix) wfpCond {
	kind := uint32(fwpV4AddrMask)
	if p.Addr().Is6() {
		kind = fwpV6AddrMask
	}
	return wfpCond{field: fwpmConditionIPRemoteAddress, match: fwpMatchEqual, kind: kind, prefix: p}
}

// addFilter turns a spec into an FWPM_FILTER0 and adds it. Every pointer the
// structure carries is kept alive until WFP has copied it. provider may be nil;
// flags is fwpmFilterFlagPersistent for a filter that outlives this program,
// 0 for one that belongs to a dynamic session. A zero key in the spec lets the
// engine choose one.
func (e wfpEngine) addFilter(spec wfpFilterSpec, provider *windows.GUID, sublayer windows.GUID, flags uint32) error {
	name, _ := windows.UTF16PtrFromString(spec.name)
	conds := make([]fwpmFilterCondition0, len(spec.conds))
	var keep []any
	var frees []func()
	defer func() {
		for _, f := range frees {
			f()
		}
	}()
	for i, c := range spec.conds {
		conds[i].fieldKey = c.field
		conds[i].matchType = c.match
		conds[i].conditionValue.kind = c.kind
		switch c.kind {
		case fwpUint8:
			conds[i].conditionValue.value = uintptr(c.u8)
		case fwpUint16:
			conds[i].conditionValue.value = uintptr(c.u16)
		case fwpUint32:
			conds[i].conditionValue.value = uintptr(c.u32)
		case fwpUint64:
			v := new(uint64)
			*v = c.u64
			keep = append(keep, v)
			conds[i].conditionValue.value = uintptr(unsafe.Pointer(v))
		case fwpByteBlobType:
			blob, free, err := wfpAppID(c.app)
			if err != nil {
				return err
			}
			frees = append(frees, free)
			conds[i].conditionValue.value = uintptr(unsafe.Pointer(blob))
		case fwpV4AddrMask:
			a := c.prefix.Addr().As4()
			m := &fwpV4AddrAndMask{
				addr: uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3]),
				mask: prefixMask4(c.prefix.Bits()),
			}
			keep = append(keep, m)
			conds[i].conditionValue.value = uintptr(unsafe.Pointer(m))
		case fwpV6AddrMask:
			m := &fwpV6AddrAndMask{addr: c.prefix.Addr().As16(), prefixLength: uint8(c.prefix.Bits())}
			keep = append(keep, m)
			conds[i].conditionValue.value = uintptr(unsafe.Pointer(m))
		default:
			return fmt.Errorf("filter %q: condition type %d is not supported", spec.name, c.kind)
		}
	}
	f := fwpmFilter0{
		filterKey:           spec.key,
		displayData:         fwpmDisplayData0{name: name},
		flags:               flags,
		providerKey:         provider,
		layerKey:            spec.layer,
		subLayerKey:         sublayer,
		weight:              fwpValue0{kind: fwpUint8, value: uintptr(spec.weight)},
		numFilterConditions: uint32(len(conds)),
		action:              fwpmAction0{kind: spec.action},
	}
	if len(conds) > 0 {
		f.filterCondition = &conds[0]
	}
	var id uint64
	err := wfpCall("FwpmFilterAdd0", procFwpmFilterAdd0,
		uintptr(e), uintptr(unsafe.Pointer(&f)), 0, uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(name)
	runtime.KeepAlive(conds)
	runtime.KeepAlive(keep)
	runtime.KeepAlive(provider)
	if err != nil {
		return fmt.Errorf("filter %q: %w", spec.name, err)
	}
	return nil
}

func prefixMask4(bits int) uint32 {
	if bits <= 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}
