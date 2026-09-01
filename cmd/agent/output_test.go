package main

import (
	"strings"
	"testing"
)

func TestCappedOutputKeepsPrefixAndMarksTruncation(t *testing.T) {
	w := newCappedOutput(5)
	if n, err := w.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if got, want := w.String(), "abcde"+truncatedOutputMarker; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCappedOutputDoesNotMarkExactLimit(t *testing.T) {
	w := newCappedOutput(5)
	_, _ = w.Write([]byte("abc"))
	_, _ = w.Write([]byte("de"))
	if got := w.String(); got != "abcde" {
		t.Fatalf("String() = %q", got)
	}
}

func TestCappedOutputNeverBuffersBeyondLimit(t *testing.T) {
	w := newCappedOutput(4096)
	payload := strings.Repeat("x", 1<<20)
	_, _ = w.Write([]byte(payload))
	if len(w.data) != 4096 {
		t.Fatalf("buffered %d bytes", len(w.data))
	}
}
