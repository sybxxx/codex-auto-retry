//go:build !windows

package main

func readStartupApprovalStatus() string { return "unknown" }
