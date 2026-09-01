package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homectl/internal/protocol"

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
	Username          string
	PasswordHash      string
	TOTPSecret        string
	LastTOTPLoginStep int64
}

type Store struct {
	db *sql.DB
}

func prepareDatabaseFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}

	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("database %s must be a regular file, not a symlink or special file", path)
		}
		if err := os.Chmod(path, 0600); err != nil {
			return fmt.Errorf("secure database %s: %w", path, err)
		}
	case os.IsNotExist(err):
		f, openErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return fmt.Errorf("create database %s: %w", path, openErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			return fmt.Errorf("close database %s: %w", path, closeErr)
		}
	default:
		return fmt.Errorf("inspect database %s: %w", path, err)
	}
	return nil
}

func OpenStore(path string) (*Store, error) {
	if err := prepareDatabaseFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	// These connection-local settings help startup without changing the
	// database file before its schema version has been accepted.
	for _, pragma := range []string{
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
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	_ = os.Chmod(path, 0600)
	return s, nil
}

func (s *Store) initSchema() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	if err := s.ensureSchemaObjects(); err != nil {
		return err
	}
	if version == currentSchemaVersion {
		return nil
	}
	if err := s.migrateAdminSchema(); err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, currentSchemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (s *Store) ensureSchemaObjects() error {
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
  password_hash TEXT NOT NULL,
  totp_secret TEXT NOT NULL DEFAULT '',
  totp_last_login_step INTEGER NOT NULL DEFAULT -1,
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
		`CREATE TABLE IF NOT EXISTS sessions (
  token_hash BLOB PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  persistent INTEGER NOT NULL DEFAULT 0
)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init sqlite schema: %w", err)
		}
	}
	return nil
}

const currentSchemaVersion = 1

func (s *Store) migrateAdminSchema() error {
	var declaredType string
	hasLastStep := false
	rows, err := s.db.Query(`PRAGMA table_info(admins)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "password_hash" {
			declaredType = strings.ToUpper(strings.TrimSpace(typ))
		}
		if name == "totp_last_login_step" {
			hasLastStep = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if declaredType != "" && declaredType != "TEXT" {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DROP TABLE IF EXISTS admins_new`); err != nil {
			return fmt.Errorf("drop temporary admins table: %w", err)
		}
		if _, err := tx.Exec(`CREATE TABLE admins_new (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  totp_secret TEXT NOT NULL DEFAULT '',
  totp_last_login_step INTEGER NOT NULL DEFAULT -1,
  updated_at INTEGER NOT NULL
)`); err != nil {
			return fmt.Errorf("create temporary admins table: %w", err)
		}
		copySQL := `INSERT INTO admins_new(id,username,password_hash,totp_secret,totp_last_login_step,updated_at)
SELECT id,username,CAST(password_hash AS TEXT),totp_secret,-1,updated_at FROM admins`
		if hasLastStep {
			copySQL = `INSERT INTO admins_new(id,username,password_hash,totp_secret,totp_last_login_step,updated_at)
SELECT id,username,CAST(password_hash AS TEXT),totp_secret,totp_last_login_step,updated_at FROM admins`
		}
		if _, err := tx.Exec(copySQL); err != nil {
			return fmt.Errorf("copy admins data: %w", err)
		}
		if _, err := tx.Exec(`DROP TABLE admins`); err != nil {
			return fmt.Errorf("drop admins table: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE admins_new RENAME TO admins`); err != nil {
			return fmt.Errorf("rename admins table: %w", err)
		}
		return tx.Commit()
	}
	if declaredType == "TEXT" && !hasLastStep {
		if _, err := s.db.Exec(`ALTER TABLE admins ADD COLUMN totp_last_login_step INTEGER NOT NULL DEFAULT -1`); err != nil {
			return fmt.Errorf("add totp replay column: %w", err)
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
	username, err = normalizeUsername(username)
	if err != nil {
		return fmt.Errorf("admin_username: %w", err)
	}
	if password == "" {
		return errors.New("admin_password is required for first startup")
	}
	if password == "CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD" {
		return errors.New("admin_password still uses the example placeholder")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return fmt.Errorf("admin_password: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO admins(id,username,password_hash,totp_secret,totp_last_login_step,updated_at) VALUES(1,?,?,?,-1,?)`, username, hash, "", time.Now().Unix())
	return err
}

func (s *Store) GetAdmin() (*AdminRecord, error) {
	var a AdminRecord
	err := s.db.QueryRow(`SELECT username,password_hash,totp_secret,totp_last_login_step FROM admins WHERE id=1`).Scan(&a.Username, &a.PasswordHash, &a.TOTPSecret, &a.LastTOTPLoginStep)
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
	ok, needsRehash := verifyPasswordHash(a.PasswordHash, password)
	if !ok {
		return a, false, nil
	}
	if needsRehash {
		if next, err := rehashVerifiedPassword(password); err == nil {
			if _, err := s.db.Exec(`UPDATE admins SET password_hash=?, updated_at=? WHERE id=1`, next, time.Now().Unix()); err == nil {
				a.PasswordHash = next
			}
		}
	}
	return a, true, nil
}

func (s *Store) UpdateAdminUsername(username string) error {
	_, err := s.db.Exec(`UPDATE admins SET username=?, updated_at=? WHERE id=1`, username, time.Now().Unix())
	return err
}

func (s *Store) UpdateAdminUsernameAndRevokeSessions(username string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE admins SET username=?, updated_at=? WHERE id=1`, username, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateAdminPassword(password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE admins SET password_hash=?, updated_at=? WHERE id=1`, hash, time.Now().Unix())
	return err
}

func (s *Store) UpdateAdminPasswordAndRevokeSessions(password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE admins SET password_hash=?, updated_at=? WHERE id=1`, hash, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetAdminTOTP(secret string) error {
	_, err := s.db.Exec(`UPDATE admins SET totp_secret=?, totp_last_login_step=-1, updated_at=? WHERE id=1`, secret, time.Now().Unix())
	return err
}

func (s *Store) EnableAdminTOTPAndRevokeSessions(secret string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE admins SET totp_secret=?, totp_last_login_step=-1, updated_at=? WHERE id=1`, secret, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DisableAdminTOTPAndRevokeSessions() (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE admins SET totp_secret='', totp_last_login_step=-1, updated_at=? WHERE id=1 AND totp_secret<>''`, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func sessionPersistentValue(persistent bool) int {
	if persistent {
		return 1
	}
	return 0
}

func (s *Store) CreateSession(token string, expiresAt int64, persistent bool) error {
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,created_at,expires_at,persistent) VALUES(?,?,?,?)`,
		hashToken(token), time.Now().Unix(), expiresAt, sessionPersistentValue(persistent))
	return err
}

func (s *Store) ConsumeTOTPLoginStepAndCreateSession(step int64, token string, expiresAt int64, persistent bool) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE admins SET totp_last_login_step=? WHERE id=1 AND totp_last_login_step<?`, step, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO sessions(token_hash,created_at,expires_at,persistent) VALUES(?,?,?,?)`,
		hashToken(token), time.Now().Unix(), expiresAt, sessionPersistentValue(persistent)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) SessionValid(token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var expires int64
	err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, hashToken(token)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Now().Unix() >= expires {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
		return false, nil
	}
	return true, nil
}

func (s *Store) SessionExpiry(token string) (int64, bool, error) {
	if token == "" {
		return 0, false, nil
	}
	hash := hashToken(token)
	var expires int64
	err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, hash).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if time.Now().Unix() >= expires {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hash)
		return 0, false, nil
	}
	return expires, true, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
	return err
}

func (s *Store) DeleteAllSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions`)
	return err
}

func (s *Store) CleanupSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at<=?`, time.Now().Unix())
	return err
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

func (s *Store) EnrollDevice(enrollmentToken string, d *DeviceRecord) (bool, error) {
	if d == nil || d.ID == "" || len(d.TokenHash) == 0 {
		return false, errors.New("invalid device enrollment")
	}
	infoJSON := ""
	if d.Info != nil {
		b, err := protocol.MarshalSystemInfo(d.Info)
		if err != nil {
			return false, err
		}
		infoJSON = string(b)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, err := tx.Exec(`UPDATE enrollment_tokens SET used_at=? WHERE token_hash=? AND used_at=0 AND expires_at>=?`, now, hashToken(enrollmentToken), now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO devices(id,name,token_hash,last_seen,info_json) VALUES(?,?,?,?,?)`, d.ID, d.Name, d.TokenHash, d.LastSeen, infoJSON); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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

// ImportLegacyDeviceJSON imports device records from a configured JSON source.
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
