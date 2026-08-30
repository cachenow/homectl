package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"homectl/internal/protocol"
)

type DeviceRecord struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Token    string               `json:"token"`
	LastSeen int64                `json:"last_seen"`
	Info     *protocol.SystemInfo `json:"info,omitempty"`
}

type Store struct {
	path    string
	mu      sync.RWMutex
	devices map[string]DeviceRecord
}

type storeFile struct {
	Devices []DeviceRecord `json:"devices"`
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, devices: make(map[string]DeviceRecord)}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read store: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("decode store: %w", err)
	}
	for _, d := range f.Devices {
		if d.ID != "" {
			s.devices[d.ID] = d
		}
	}
	return s, nil
}

func (s *Store) Close() error { return nil }

func (s *Store) Get(id string) (*DeviceRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return nil, nil
	}
	copy := d
	return &copy, nil
}

func (s *Store) Put(r *DeviceRecord) error {
	if r == nil || r.ID == "" {
		return errors.New("invalid device")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[r.ID] = *r
	return s.persistLocked()
}

func (s *Store) List() ([]DeviceRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeviceRecord, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	return out, nil
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f := storeFile{Devices: make([]DeviceRecord, 0, len(s.devices))}
	for _, d := range s.devices {
		f.Devices = append(f.Devices, d)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(s.path, 0600)
}
