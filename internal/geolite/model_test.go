package geolite

import (
	"strings"
	"testing"
)

func TestDefaultConfigUsesTwiceWeeklyLocalScheduleAndStartsDisabled(t *testing.T) {
	got := DefaultConfig()
	if got.Enabled {
		t.Fatal("default schedule must remain disabled until credentials are configured")
	}
	if got.StartTime != "04:00" || len(got.Days) != 2 || got.Days[0] != "MON" || got.Days[1] != "THU" {
		t.Fatalf("default schedule = %#v; want MON,THU at 04:00", got)
	}
}

func TestValidateConfigAcceptsValidTwoOrMoreWeekdaysAndLocalTime(t *testing.T) {
	for _, config := range []Config{
		{Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"},
		{Enabled: true, Days: []string{"SUN", "TUE", "SAT"}, StartTime: "23:59"},
		{Enabled: false, Days: []string{"MON", "THU"}, StartTime: "00:00"},
		{AccountID: "123456", LicenseKey: "a1B2_c3D4", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"},
	} {
		if err := ValidateConfig(config); err != nil {
			t.Errorf("ValidateConfig(%#v) = %v", config, err)
		}
	}
}

func TestValidateConfigRejectsUnsafeOrInvalidSchedule(t *testing.T) {
	for _, test := range []struct {
		name   string
		config Config
	}{
		{name: "one day", config: Config{Enabled: true, Days: []string{"MON"}, StartTime: "04:00"}},
		{name: "duplicate days", config: Config{Enabled: true, Days: []string{"MON", "MON"}, StartTime: "04:00"}},
		{name: "unknown day", config: Config{Enabled: true, Days: []string{"MON", "FUNDAY"}, StartTime: "04:00"}},
		{name: "empty time", config: Config{Enabled: true, Days: []string{"MON", "THU"}, StartTime: ""}},
		{name: "non padded time", config: Config{Enabled: true, Days: []string{"MON", "THU"}, StartTime: "4:00"}},
		{name: "hour overflow", config: Config{Enabled: true, Days: []string{"MON", "THU"}, StartTime: "24:00"}},
		{name: "minute overflow", config: Config{Enabled: true, Days: []string{"MON", "THU"}, StartTime: "12:60"}},
		{name: "seven day cap", config: Config{Enabled: true, Days: []string{"MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN", "MON2"}, StartTime: "04:00"}},
		{name: "account id not numeric", config: Config{AccountID: "123\nEditionIDs GeoIP2-City", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}},
		{name: "license key has line break", config: Config{LicenseKey: "private-key\nEditionIDs GeoIP2-City", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}},
		{name: "account id without license key", config: Config{AccountID: "123456", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}},
		{name: "license key without account id", config: Config{LicenseKey: "a1B2_c3D4", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}},
		{name: "license key punctuation", config: Config{AccountID: "123456", LicenseKey: "bad-key!", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}},
		{name: "license key too long", config: Config{AccountID: "123456", LicenseKey: strings.Repeat("A", 65), Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateConfig(test.config); err == nil {
				t.Fatalf("ValidateConfig(%#v) succeeded", test.config)
			}
		})
	}
}
