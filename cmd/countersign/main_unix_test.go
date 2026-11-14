//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe has no end until someone writes to it, so opening it for
// reading waits for ever. readWaybill must refuse it without opening it.
func TestReadWaybillRefusesAPipeWithoutWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot make a named pipe: %v", err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := readWaybill(path)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a named pipe was accepted")
		}
	case <-time.After(2 * time.Second):
		// Let the stuck open go, so the test does not leave it behind.
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("readWaybill waited on a named pipe")
	}
}
