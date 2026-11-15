//go:build !linux

package store

func networkFilesystemWarning(string) string { return "" }
