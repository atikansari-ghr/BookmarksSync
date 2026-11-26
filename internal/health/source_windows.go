//go:build windows

package health

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformSource struct{}

var kernel32 = windows.NewLazySystemDLL("kernel32.dll")

func cpuTimes() (idle, total uint64, err error) {
	var idleFT, kernelFT, userFT windows.Filetime
	proc := kernel32.NewProc("GetSystemTimes")
	if e := proc.Find(); e != nil {
		return 0, 0, e
	}
	r, _, e := proc.Call(uintptr(unsafe.Pointer(&idleFT)), uintptr(unsafe.Pointer(&kernelFT)), uintptr(unsafe.Pointer(&userFT)))
	if r == 0 {
		return 0, 0, e
	}
	toUint := func(v windows.Filetime) uint64 { return uint64(v.HighDateTime)<<32 | uint64(v.LowDateTime) }
	idle = toUint(idleFT)
	total = toUint(kernelFT) + toUint(userFT)
	return idle, total, nil
}

func (platformSource) CPUPercent(ctx context.Context) (float64, error) {
	i1, t1, err := cpuTimes()
	if err != nil {
		return 0, err
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
	}
	i2, t2, err := cpuTimes()
	if err != nil {
		return 0, err
	}
	if t2 <= t1 || i2 < i1 {
		return 0, errors.New("cpu counters unavailable")
	}
	dt, di := t2-t1, i2-i1
	return float64(dt-di) * 100 / float64(dt), nil
}

type memoryStatusEx struct {
	Length, MemoryLoad                                                                                   uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, AvailExtendedVirtual uint64
}

func (platformSource) Memory(context.Context) (Usage, error) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	proc := kernel32.NewProc("GlobalMemoryStatusEx")
	if err := proc.Find(); err != nil {
		return Usage{}, err
	}
	r, _, err := proc.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return Usage{}, err
	}
	return usage(m.TotalPhys, m.AvailPhys)
}

func (platformSource) Disk(_ context.Context, path string) (Usage, error) {
	dir, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(dir, &free, &total, &totalFree); err != nil {
		return Usage{}, err
	}
	return usage(total, totalFree)
}

func (platformSource) Uptime(context.Context) (uint64, error) {
	proc := kernel32.NewProc("GetTickCount64")
	if err := proc.Find(); err != nil {
		return 0, err
	}
	ms, _, _ := proc.Call()
	return uint64(ms / 1000), nil
}
