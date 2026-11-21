// Package health collects host metrics without exposing application records or secrets.
package health

import (
	"context"
	"fmt"
)

type Usage struct {
	TotalBytes uint64  `json:"totalBytes"`
	UsedBytes  uint64  `json:"usedBytes"`
	Percent    float64 `json:"percent"`
}

type Report struct {
	CPUPercent    *float64 `json:"cpuPercent"`
	Memory        *Usage   `json:"memory"`
	Disk          *Usage   `json:"disk"`
	UptimeSeconds *uint64  `json:"uptimeSeconds"`
	Unavailable   []string `json:"unavailable"`
}

type source interface {
	CPUPercent(context.Context) (float64, error)
	Memory(context.Context) (Usage, error)
	Disk(context.Context, string) (Usage, error)
	Uptime(context.Context) (uint64, error)
}

type Collector struct {
	dataDir string
	source  source
}

func New(dataDir string) *Collector { return newCollector(dataDir, platformSource{}) }

func newCollector(dataDir string, src source) *Collector {
	return &Collector{dataDir: dataDir, source: src}
}

func (c *Collector) Collect(ctx context.Context) Report {
	r := Report{Unavailable: []string{}}
	if value, err := c.source.CPUPercent(ctx); err == nil && value >= 0 && value <= 100 {
		r.CPUPercent = &value
	} else {
		r.Unavailable = append(r.Unavailable, "cpu")
	}
	if value, err := c.source.Memory(ctx); err == nil {
		r.Memory = &value
	} else {
		r.Unavailable = append(r.Unavailable, "memory")
	}
	if value, err := c.source.Disk(ctx, c.dataDir); err == nil {
		r.Disk = &value
	} else {
		r.Unavailable = append(r.Unavailable, "disk")
	}
	if value, err := c.source.Uptime(ctx); err == nil {
		r.UptimeSeconds = &value
	} else {
		r.Unavailable = append(r.Unavailable, "uptime")
	}
	return r
}

func usage(total, free uint64) (Usage, error) {
	if total == 0 || free > total {
		return Usage{}, fmt.Errorf("invalid usage counters")
	}
	used := total - free
	return Usage{TotalBytes: total, UsedBytes: used, Percent: float64(used) * 100 / float64(total)}, nil
}
