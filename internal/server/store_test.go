package server

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homectl/internal/protocol"

	"golang.org/x/crypto/bcrypt"
)

func TestSQLiteStoreDevicesAndAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnsureAdmin("admin", "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	admin, ok, err := s.VerifyAdminPassword("0123456789abcdef")
	if err != nil || !ok || admin.Username != "admin" {
		t.Fatalf("admin verification failed: %#v %v %v", admin, ok, err)
	}

	want := &DeviceRecord{ID: "device-1", Name: "lab", TokenHash: hashToken("secret"), LastSeen: 123}
	if err := s.Put(want); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDeviceName("device-1", "custom lab"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHeartbeat("device-1", 456, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("device-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != want.ID || got.Name != "custom lab" || got.LastSeen != 456 || !secureEqualBytes(got.TokenHash, want.TokenHash) {
		t.Fatalf("unexpected record after rename/heartbeat: %#v", got)
	}
	if err := s.DeleteDevice("device-1"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get("device-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("device still exists after delete: %#v", got)
	}
}

func TestEnrollmentTokenIsOneTime(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateEnrollmentToken("id1", "test", "one-time-secret", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ConsumeEnrollmentToken("one-time-secret")
	if err != nil || !ok {
		t.Fatalf("first consume failed: %v %v", ok, err)
	}
	ok, err = s.ConsumeEnrollmentToken("one-time-secret")
	if err != nil || ok {
		t.Fatalf("token reused: %v %v", ok, err)
	}
}

func TestEnrollmentLabelBecomesInitialDeviceName(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateEnrollmentToken("id1", "  HomeServer  ", "named-secret", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err := s.EnrollDevice("named-secret", &DeviceRecord{ID: "device-1", Name: "homeserver", TokenHash: hashToken("device-secret")})
	if err != nil || !ok {
		t.Fatalf("enrollment failed: ok=%v err=%v", ok, err)
	}
	record, err := s.Get("device-1")
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.Name != "HomeServer" {
		t.Fatalf("initial device name=%q, want HomeServer", record.Name)
	}
}

func TestBlankEnrollmentLabelFallsBackToAgentName(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateEnrollmentToken("id1", "  ", "unnamed-secret", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err := s.EnrollDevice("unnamed-secret", &DeviceRecord{ID: "device-1", Name: "AgentConfiguredName", TokenHash: hashToken("device-secret")})
	if err != nil || !ok {
		t.Fatalf("enrollment failed: ok=%v err=%v", ok, err)
	}
	record, err := s.Get("device-1")
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.Name != "AgentConfiguredName" {
		t.Fatalf("initial device name=%q, want AgentConfiguredName", record.Name)
	}
}

func TestLegacyAdminBlobSchemaMigratesToTextAndArgon2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE admins (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  username TEXT NOT NULL UNIQUE,
  password_hash BLOB NOT NULL,
  totp_secret TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	legacyPassword := "old-pass-123"
	legacyHash, err := bcrypt.GenerateFromPassword([]byte(legacyPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admins(id,username,password_hash,totp_secret,updated_at) VALUES(1,'admin',?,'',?)`, legacyHash, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var declaredType string
	rows, err := s.db.Query(`PRAGMA table_info(admins)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "password_hash" {
			declaredType = typ
		}
	}
	rows.Close()
	if declaredType != "TEXT" {
		t.Fatalf("password_hash type not migrated: %q", declaredType)
	}
	admin, ok, err := s.VerifyAdminPassword(legacyPassword)
	if err != nil || !ok {
		t.Fatalf("legacy login failed: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(admin.PasswordHash, "$argon2id$") {
		t.Fatalf("legacy hash not upgraded after successful login: %q", admin.PasswordHash)
	}
}

func TestEnsureAdminIgnoresBootstrapCredentialsAfterCreation(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnsureAdmin("owner", "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAdmin("", ""); err != nil {
		t.Fatalf("bootstrap credentials were revalidated after the administrator already existed: %v", err)
	}
	admin, err := s.GetAdmin()
	if err != nil || admin == nil || admin.Username != "owner" {
		t.Fatalf("administrator changed unexpectedly: %#v %v", admin, err)
	}
}

func TestPersistentSessionStoredAsTokenHash(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	token := "raw-session-secret-value"
	if err := s.CreateSession(token, time.Now().Add(time.Hour).Unix(), true); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == token {
		t.Fatal("raw session token was stored in SQLite")
	}
	ok, err := s.SessionValid(token)
	if err != nil || !ok {
		t.Fatalf("stored session not valid: %v %v", ok, err)
	}
}

func TestSchemaVersionIsCurrent(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version=%d want=%d", version, currentSchemaVersion)
	}
}

func TestFutureSchemaIsRejectedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sentinel(value TEXT NOT NULL); INSERT INTO sentinel(value) VALUES('keep'); PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(beforeBytes)
	if s, err := OpenStore(path); err == nil {
		_ = s.Close()
		t.Fatal("future schema version was accepted")
	} else if !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("unexpected future schema error: %v", err)
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if after := sha256.Sum256(afterBytes); after != before {
		t.Fatal("future-version database contents changed during rejection")
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM sentinel`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("sentinel=%q err=%v", value, err)
	}
	var created int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='devices'`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("unexpected schema object count=%d err=%v", created, err)
	}
}

func TestDeviceOrderIsExplicitAndUnaffectedByRename(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, device := range []*DeviceRecord{
		{ID: "device-b", Name: "Beta", TokenHash: hashToken("token-b")},
		{ID: "device-a", Name: "Alpha", TokenHash: hashToken("token-a")},
	} {
		if err := s.Put(device); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].ID != "device-b" || list[1].ID != "device-a" {
		t.Fatalf("initial insertion order=%#v err=%v", list, err)
	}
	if err := s.UpdateDeviceName("device-b", "Zulu"); err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil || list[0].ID != "device-b" {
		t.Fatalf("rename changed order=%#v err=%v", list, err)
	}
	if err := s.ReorderDevices([]string{"device-a", "device-b"}); err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil || list[0].ID != "device-a" || list[1].ID != "device-b" {
		t.Fatalf("manual order=%#v err=%v", list, err)
	}
	if err := s.ReorderDevices([]string{"device-a", "device-a"}); err == nil {
		t.Fatal("duplicate device order was accepted")
	}
}

func TestDeviceMetricCardsPersistAndRequireThree(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(&DeviceRecord{ID: "device", Name: "Device", TokenHash: hashToken("token")}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMetricCards("device", []string{protocol.MetricNetwork, protocol.MetricCPU, protocol.MetricMemory}); err != nil {
		t.Fatal(err)
	}
	record, err := s.Get("device")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protocol.MetricCPU, protocol.MetricMemory, protocol.MetricNetwork}
	if record == nil || len(record.MetricCards) != len(want) {
		t.Fatalf("metric cards=%#v", record)
	}
	for index := range want {
		if record.MetricCards[index] != want[index] {
			t.Fatalf("metric cards=%v want=%v", record.MetricCards, want)
		}
	}
	if err := s.UpdateMetricCards("device", []string{protocol.MetricCPU, protocol.MetricMemory}); err == nil {
		t.Fatal("fewer than three metric cards were accepted")
	}
}

func TestVersionOneDeviceOrderMigrationPreservesPreviousDisplayOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash BLOB NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0,
  info_json TEXT NOT NULL DEFAULT ''
);
INSERT INTO devices(id,name,token_hash) VALUES('z','Zulu',x'01'),('a','Alpha',x'02');
PRAGMA user_version=1`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].ID != "a" || list[1].ID != "z" {
		t.Fatalf("migrated order=%#v err=%v", list, err)
	}
	if len(list[0].MetricCards) != len(protocol.DefaultMetricCards()) {
		t.Fatalf("migrated default metric cards=%v", list[0].MetricCards)
	}
}

func TestCompleteExistingDatabaseMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE devices (id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash BLOB NOT NULL, last_seen INTEGER NOT NULL DEFAULT 0, info_json TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE admins (id INTEGER PRIMARY KEY CHECK (id = 1), username TEXT NOT NULL UNIQUE, password_hash BLOB NOT NULL, totp_secret TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL)`,
		`CREATE TABLE enrollment_tokens (id TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', token_hash BLOB NOT NULL UNIQUE, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX idx_enrollment_tokens_expires ON enrollment_tokens(expires_at)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	legacyHash, err := bcrypt.GenerateFromPassword([]byte("short-pass"), bcrypt.DefaultCost)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	deviceToken := strings.Repeat("d", 64)
	enrollmentToken := strings.Repeat("e", 48)
	if _, err := db.Exec(`INSERT INTO devices(id,name,token_hash,last_seen,info_json) VALUES('device-1','custom name',?,123,'')`, hashToken(deviceToken)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admins(id,username,password_hash,totp_secret,updated_at) VALUES(1,'owner',?,'',123)`, legacyHash); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO enrollment_tokens(id,label,token_hash,created_at,expires_at,used_at) VALUES('token-1','pending',?,123,?,0)`, hashToken(enrollmentToken), time.Now().Add(time.Hour).Unix()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	device, err := s.Get("device-1")
	if err != nil || device == nil || device.Name != "custom name" || !secureEqualBytes(device.TokenHash, hashToken(deviceToken)) {
		t.Fatalf("migrated device=%#v err=%v", device, err)
	}
	admin, err := s.GetAdmin()
	if err != nil || admin == nil || admin.Username != "owner" || !strings.HasPrefix(admin.PasswordHash, "$2") || admin.LastTOTPLoginStep != -1 {
		t.Fatalf("migrated admin=%#v err=%v", admin, err)
	}
	if _, ok, err := s.VerifyAdminPassword("short-pass"); err != nil || !ok {
		t.Fatalf("existing bcrypt password failed: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ConsumeEnrollmentToken(enrollmentToken); err != nil || !ok {
		t.Fatalf("existing enrollment token failed: ok=%v err=%v", ok, err)
	}
	if err := s.CreateSession("session", time.Now().Add(time.Hour).Unix(), false); err != nil {
		t.Fatalf("sessions table unavailable after migration: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}

func TestEnrollDeviceRollsBackTokenOnInsertFailure(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(&DeviceRecord{ID: "device-1", Name: "existing", TokenHash: hashToken("existing-token")}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEnrollmentToken("enroll-1", "test", "enroll-secret", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err := s.EnrollDevice("enroll-secret", &DeviceRecord{ID: "device-1", Name: "duplicate", TokenHash: hashToken("new-device-token")})
	if err == nil || ok {
		t.Fatalf("duplicate enrollment unexpectedly succeeded: ok=%v err=%v", ok, err)
	}
	ok, err = s.ConsumeEnrollmentToken("enroll-secret")
	if err != nil || !ok {
		t.Fatalf("enrollment token was consumed despite rolled-back device insert: ok=%v err=%v", ok, err)
	}
}

func TestTOTPLoginStepRollsBackWhenSessionInsertFails(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnsureAdmin("admin", "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	const token = "duplicate-session-token"
	if err := s.CreateSession(token, time.Now().Add(time.Hour).Unix(), false); err != nil {
		t.Fatal(err)
	}
	consumed, err := s.ConsumeTOTPLoginStepAndCreateSession(100, token, time.Now().Add(time.Hour).Unix(), false)
	if err == nil || consumed {
		t.Fatalf("duplicate session insert unexpectedly succeeded: consumed=%v err=%v", consumed, err)
	}
	admin, err := s.GetAdmin()
	if err != nil {
		t.Fatal(err)
	}
	if admin.LastTOTPLoginStep != -1 {
		t.Fatalf("TOTP step advanced despite rolled-back session insert: %d", admin.LastTOTPLoginStep)
	}
}

func TestOpenStoreSecuresDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("database mode=%#o want=0600", got)
	}
}

func TestOpenStoreRejectsDatabaseSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "homectl.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(link); err == nil {
		t.Fatal("database symlink was accepted")
	}
}
