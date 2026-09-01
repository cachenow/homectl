package main

import "sync"

const truncatedOutputMarker = "\n[output truncated]\n"

type cappedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func newCappedOutput(limit int) *cappedOutput {
	return &cappedOutput{limit: limit, data: make([]byte, 0, min(limit, 32<<10))}
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()

	remaining := w.limit - len(w.data)
	if remaining > 0 {
		keep := min(remaining, len(p))
		w.data = append(w.data, p[:keep]...)
		if keep < len(p) {
			w.truncated = true
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return n, nil
}

func (w *cappedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := string(w.data)
	if w.truncated {
		out += truncatedOutputMarker
	}
	return out
}
