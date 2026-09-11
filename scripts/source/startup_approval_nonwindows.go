//go:build !windows

package main

func readStartupApprovalStatus() string { return "unknown" }

func ensureStartupApproval(string) error { return nil }
