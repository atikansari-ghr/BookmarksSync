//go:build !windows

package updates

func InstalledGeoLiteRunner() GeoLiteRunner { return nil }
