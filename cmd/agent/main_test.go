package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTermInputQueueIsBoundedAndStopIsIdempotent(t *testing.T) {
	released := 0
	term := &termSession{
		input:   make(chan []byte, 1),
		done:    make(chan struct{}),
		release: func() { released++ },
	}
	first := []byte("first")
	if !term.enqueueInput(first) {
		t.Fatal("first terminal input was unexpectedly rejected")
	}
	if term.enqueueInput([]byte("second")) {
		t.Fatal("terminal input queue accepted data beyond its capacity")
	}
	if got := <-term.input; !bytes.Equal(got, first) {
		t.Fatalf("queued terminal input = %q, want %q", got, first)
	}
	term.stop()
	term.stop()
	if released != 1 {
		t.Fatalf("slot release count = %d, want 1", released)
	}
	if term.enqueueInput([]byte("after stop")) {
		t.Fatal("stopped terminal accepted input")
	}
}

func TestOperationSlotAdmissionIsBounded(t *testing.T) {
	slots := make(chan struct{}, 1)
	if !acquireSlot(slots) {
		t.Fatal("first slot acquisition failed")
	}
	if acquireSlot(slots) {
		t.Fatal("slot acquisition exceeded capacity")
	}
	release := releaseSlot(slots)
	release()
	release()
	if !acquireSlot(slots) {
		t.Fatal("released slot was not reusable")
	}
}

func TestJitteredReconnectDelayStaysBounded(t *testing.T) {
	base := 10 * time.Second
	maximum := 30 * time.Second
	for range 100 {
		got := jitteredReconnectDelay(base, maximum)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jittered delay=%v outside expected range", got)
		}
	}
	for range 100 {
		got := jitteredReconnectDelay(maximum, maximum)
		if got < 24*time.Second || got > maximum {
			t.Fatalf("maximum jittered delay=%v outside expected range", got)
		}
	}
}

func TestActionCommandCandidatesSupportDirectSystemCommands(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"reboot", "poweroff"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	for _, action := range []string{"reboot", "poweroff"} {
		commands := actionCommandCandidates(action)
		if len(commands) != 1 || len(commands[0]) != 1 || filepath.Base(commands[0][0]) != action {
			t.Fatalf("%s candidates=%#v", action, commands)
		}
	}
}

func TestFinishTermDoesNotRemoveReplacement(t *testing.T) {
	stale := &termSession{}
	current := &termSession{}
	a := &agent{terms: map[string]*termSession{"session": current}}
	if a.finishTerm("session", stale) {
		t.Fatal("superseded terminal retained ownership")
	}
	if a.terms["session"] != current {
		t.Fatal("superseded terminal removed its replacement")
	}
}
