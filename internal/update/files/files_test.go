package files

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInterruptedReplacementRetainsPreviousRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.json")
	if err := Write(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	err := Replace(path, 0600, func(w io.Writer) error {
		_, err := w.Write([]byte("partial"))
		if err != nil {
			return err
		}
		return errors.New("injected write failure")
	})
	if err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "old" {
		t.Fatal("partial record replaced previous state", string(b), err)
	}
	if err := Write(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil || string(b) != "new" {
		t.Fatal("replacement did not publish", err)
	}
}

func TestUpdatePathsRefuseSymlinksAndSharedWritableState(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := Directory(dir, 0700); err != nil {
		t.Fatal("invalid directory fixture:", err)
	}
	target := filepath.Join(dir, "target")
	if err := Write(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if Write(link, []byte("overwrite"), 0600) == nil {
		t.Fatal("followed a destination symlink")
	}
	if err := os.Chmod(target, 0666); err != nil {
		t.Fatal(err)
	}
	if Write(target, []byte("overwrite"), 0600) == nil {
		t.Fatal("adopted writable state")
	}
	parent := filepath.Join(dir, "parent")
	if err := os.Symlink(dir, parent); err != nil {
		t.Fatal(err)
	}
	if Directory(filepath.Join(parent, "child"), 0700) == nil {
		t.Fatal("created state through a symlink ancestor")
	}
}

func TestStableLockExcludesAnotherProcessHandle(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operation.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	inode, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := LockWithin(path, 0); err == nil {
		release()
		unlock()
		t.Fatal("concurrent operation acquired lock")
	}
	unlock()
	unlock, err = Lock(path)
	if err != nil {
		t.Fatal("lock did not release", err)
	}
	again, err := os.Stat(path)
	if err != nil || !os.SameFile(inode, again) {
		t.Fatal("lock inode changed; an existing waiter could lock a different file", err)
	}
	unlock()
}

// W1: vpnd takes the operations lock briefly for every change it accepts, so
// the update worker and vpnd wait a few seconds for it instead of refusing at
// once, and refuse only when it stays held.
func TestLockWaitsForABriefHolderAndRefusesALongOne(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operations.lock")
	unlock, err := LockWithin(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		unlock()
	}()
	began := time.Now()
	second, err := LockWithin(path, 3*time.Second)
	if err != nil {
		t.Fatalf("a brief holder made the second wait fail: %v", err)
	}
	if waited := time.Since(began); waited < 150*time.Millisecond {
		t.Fatalf("got the lock after %v while it was held", waited)
	}
	if third, err := LockWithin(path, 300*time.Millisecond); err == nil {
		third()
		t.Fatal("acquired a lock that stayed held")
	} else if !strings.Contains(err.Error(), "another node operation is running") {
		t.Fatalf("refusal = %v", err)
	}
	second()
}
