package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"homectl/internal/protocol"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type DeviceRecord struct {
	ID        string
	Name      string
	TokenHash []byte
	LastSeen  int64
	Info      *protocol.SystemInfo
}

type AdminRecord struct {
	Username     string
	PasswordHash []byte
	TOTPSecret   string
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0600)
	return s, nil
}

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash BLOB NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0,
  info_json TEXT NOT NULL DEFAULT ''
)`,
		`CREATE TABLE IF NOT EXISTS admins (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  username TEXT NOT NULL UNIQUE,
  password_hash BLOB NOT NULL,
  totp_secret TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS enrollment_tokens (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL DEFAULT '',
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER NOT NULL DEFAULT 0
)`,
		`CREATE INDEX IF NOT EXISTS idx_enrollment_tokens_expires ON enrollment_tokens(expires_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init sqlite schema: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Get(id string) (*DeviceRecord, error) {
	var d DeviceRecord
	var infoJSON string
	err := s.db.QueryRow(`SELECT id,name,token_hash,last_seen,info_json FROM devices WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.TokenHash, &d.LastSeen, &infoJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if infoJSON != "" {
		var info protocol.SystemInfo
		if err := protocol.UnmarshalSystemInfo([]byte(infoJSON), &info); err == nil {
			d.Info = &info
		}
	}
	return &d, nil
}

func (s *Store) Put(r *DeviceRecord) error {
	if r == nil || r.ID == "" || len(r.TokenHash) == 0 {
		return errors.New("invalid device")
	}
	infoJSON := ""
	if r.Info != nil {
		b, err := protocol.MarshalSystemInfo(r.Info)
		if err != nil {
			return err
		}
		infoJSON = string(b)
	}
	_, err := s.db.Exec(`
INSERT INTO devices(id,name,token_hash,last_seen,info_json)
VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,
 token_hash=excluded.token_hash,
 last_seen=excluded.last_seen,
 info_json=excluded.info_json`, r.ID, r.Name, r.TokenHash, r.LastSeen, infoJSON)
	return err
}

func (s *Store) UpdateHeartbeat(id string, lastSeen int64, info *protocol.SystemInfo) error {
	infoJSON := ""
	if info != nil {
		b, err := protocol.MarshalSystemInfo(info)
		if err != nil {
			return err
		}
		infoJSON = string(b)
	}
	_, err := s.db.Exec(`UPDATE devices SET last_seen=?, info_json=? WHERE id=?`, lastSeen, infoJSON, id)
	return err
}

func (s *Store) UpdateDeviceName(id, name string) error {
	_, err := s.db.Exec(`UPDATE devices SET name=? WHERE id=?`, name, id)
	return err
}

func (s *Store) DeleteDevice(id string) error {
	_, err := s.db.Exec(`DELETE FROM devices WHERE id=?`, id)
	return err
}

func (s *Store) List() ([]DeviceRecord, error) {
	rows, err := s.db.Query(`SELECT id,name,token_hash,last_seen,info_json FROM devices ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRecord
	for rows.Next() {
		var d DeviceRecord
		var infoJSON string
		if err := rows.Scan(&d.ID, &d.Name, &d.TokenHash, &d.LastSeen, &infoJSON); err != nil {
			return nil, err
		}
		if infoJSON != "" {
			var info protocol.SystemInfo
			if err := protocol.UnmarshalSystemInfo([]byte(infoJSON), &info); err == nil {
				d.Info = &info
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) EnsureAdmin(username, password string) error {
	admin, err := s.GetAdmin()
	if err != nil {
		return err
	}
	if admin != nil {
		return nil
	}
	if username == "" {
		return errors.New("admin_username is required for first startup")
	}
	if len(password) < 12 {
		return errors.New("admin_password must be at least 12 characters for first startup")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO admins(id,username,password_hash,totp_secret,updated_at) VALUES(1,?,?,?,?)`, username, hash, "", time.Now().Unix())
	return err
}

func (s *Store) GetAdmin() (*AdminRecord, error) {
	var a AdminRecord
	err := s.db.QueryRow(`SELECT username,password_hash,totp_secret FROM admins WHERE id=1`).Scan(&a.Username, &a.PasswordHash, &a.TOTPSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) VerifyAdminPassword(password string) (*AdminRecord, bool, error) {
	a, err := s.GetAdmin()
	if err != nil || a == nil {
		return a, false, err
	}
	if bcrypt.CompareHashAndPassword(a.PasswordHash, []byte(password)) != nil {
		return a, false, nil
	}
	return a, true, nil
}

func (s *Store) UpdateAdminUsername(username string) error {
	_, err := s.db.Exec(`UPDATE admins SET username=?, updated_at=? WHERE id=1`, username, time.Now().Unix())
	return err
}

func (s *Store) UpdateAdminPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE admins SET password_hash=?, updated_at=? WHERE id=1`, hash, time.Now().Unix())
	return err
}

func (s *Store) SetAdminTOTP(secret string) error {
	_, err := s.db.Exec(`UPDATE admins SET totp_secret=?, updated_at=? WHERE id=1`, secret, time.Now().Unix())
	return err
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Store) CreateEnrollmentToken(id, label, token string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO enrollment_tokens(id,label,token_hash,created_at,expires_at,used_at) VALUES(?,?,?,?,?,0)`,
		id, label, hashToken(token), time.Now().Unix(), expiresAt)
	return err
}

func (s *Store) ConsumeEnrollmentToken(token string) (bool, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(`UPDATE enrollment_tokens SET used_at=? WHERE token_hash=? AND used_at=0 AND expires_at>=?`, now, hashToken(token), now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) CleanupEnrollmentTokens() error {
	_, err := s.db.Exec(`DELETE FROM enrollment_tokens WHERE (used_at>0 AND used_at<?) OR expires_at<?`, time.Now().Add(-24*time.Hour).Unix(), time.Now().Add(-24*time.Hour).Unix())
	return err
}

type legacyStoreFile struct {
	Devices []struct {
		ID       string               `json:"id"`
		Name     string               `json:"name"`
		Token    string               `json:"token"`
		LastSeen int64                `json:"last_seen"`
		Info     *protocol.SystemInfo `json:"info,omitempty"`
	} `json:"devices"`
}

// ImportLegacyDeviceJSON performs a one-time migration from the pre-SQLite JSON device store.
// Existing SQLite device rows are left untouched.
func (s *Store) ImportLegacyDeviceJSON(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var legacy legacyStoreFile
	if err := json.Unmarshal(b, &legacy); err != nil {
		return 0, fmt.Errorf("decode legacy device store: %w", err)
	}
	count := 0
	for _, d := range legacy.Devices {
		if d.ID == "" || d.Token == "" {
			continue
		}
		existing, err := s.Get(d.ID)
		if err != nil {
			return count, err
		}
		if existing != nil {
			continue
		}
		if err := s.Put(&DeviceRecord{ID: d.ID, Name: d.Name, TokenHash: hashToken(d.Token), LastSeen: d.LastSeen, Info: d.Info}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
