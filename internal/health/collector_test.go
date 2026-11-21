package health

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type unavailableSource struct{}

func (unavailableSource) CPUPercent(context.Context) (float64, error) {
	return 0, errors.New("unavailable")
}
func (unavailableSource) Memory(context.Context) (Usage, error) {
	return Usage{}, errors.New("unavailable")
}
func (unavailableSource) Disk(context.Context, string) (Usage, error) {
	return Usage{}, errors.New("unavailable")
}
func (unavailableSource) Uptime(context.Context) (uint64, error) { return 0, errors.New("unavailable") }

func TestHealthCollectorReportsUnavailableMetrics(t *testing.T) {
	report := newCollector("/data", unavailableSource{}).Collect(context.Background())
	if report.CPUPercent != nil || report.Memory != nil || report.Disk != nil || report.UptimeSeconds != nil {
		t.Fatalf("unavailable metrics were reported as values: %+v", report)
	}
	if !reflect.DeepEqual(report.Unavailable, []string{"cpu", "memory", "disk", "uptime"}) {
		t.Fatalf("unavailable = %v", report.Unavailable)
	}
}
