//go:build !windows

package network

func rejectGeoLiteReparsePath(string) error { return nil }
