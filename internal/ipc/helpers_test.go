package ipc_test

import (
	"testing"
	"time"
)

func timeAfter() <-chan time.Time {
	return time.After(2 * time.Second)
}

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}
