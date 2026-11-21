//go:build linux

package health

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type platformSource struct{}

func (platformSource) CPUPercent(ctx context.Context) (float64, error) {
	a, err := readCPUTicks()
	if err != nil {
		return 0, err
	}
	t := time.NewTimer(100 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-t.C:
	}
	b, err := readCPUTicks()
	if err != nil {
		return 0, err
	}
	delta := b.total - a.total
	if delta == 0 || b.idle < a.idle {
		return 0, errors.New("cpu counters unavailable")
	}
	idle := b.idle - a.idle
	return float64(delta-idle) * 100 / float64(delta), nil
}

func readCPUTicks() (struct{ total, idle uint64 }, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return struct{ total, idle uint64 }{}, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return struct{ total, idle uint64 }{}, errors.New("cpu counters unavailable")
	}
	fields := strings.Fields(s.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return struct{ total, idle uint64 }{}, errors.New("cpu counters unavailable")
	}
	var out struct{ total, idle uint64 }
	for i, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return out, err
		}
		out.total += v
		if i == 3 || i == 4 {
			out.idle += v
		}
	}
	return out, s.Err()
}

func (platformSource) Memory(context.Context) (Usage, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Usage{}, err
	}
	defer f.Close()
	values := map[string]uint64{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.Fields(s.Text())
		if len(parts) >= 2 && (parts[0] == "MemTotal:" || parts[0] == "MemAvailable:") {
			v, e := strconv.ParseUint(parts[1], 10, 64)
			if e != nil {
				return Usage{}, e
			}
			values[strings.TrimSuffix(parts[0], ":")] = v * 1024
		}
	}
	if err := s.Err(); err != nil {
		return Usage{}, err
	}
	total, ok1 := values["MemTotal"]
	free, ok2 := values["MemAvailable"]
	if !ok1 || !ok2 {
		return Usage{}, errors.New("memory counters unavailable")
	}
	return usage(total, free)
}

func (platformSource) Disk(_ context.Context, path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return usage(total, free)
}

func (platformSource) Uptime(context.Context) (uint64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(string(b))
	if len(parts) == 0 {
		return 0, errors.New("uptime unavailable")
	}
	v, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || v < 0 {
		return 0, errors.New("uptime unavailable")
	}
	return uint64(v), nil
}
