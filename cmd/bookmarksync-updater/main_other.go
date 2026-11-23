//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("BookmarksSync's native updater is available on Windows only")
}
