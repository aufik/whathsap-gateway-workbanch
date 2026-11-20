package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	FullName  string    `json:"fullName"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL belum diatur")
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(15)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			username VARCHAR(50) NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			full_name VARCHAR(120) NOT NULL,
			role VARCHAR(20) NOT NULL CHECK (role IN ('admin','tester')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id VARCHAR(128) PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS user_workbooks (
			user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			file_name TEXT NOT NULL,
			file_data BYTEA NOT NULL,
			metrics_data JSONB NOT NULL DEFAULT '{}'::jsonb,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS user_settings (
			user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			bot_phone VARCHAR(30) NOT NULL DEFAULT '',
			settings JSONB NOT NULL DEFAULT '{}'::jsonb,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS wa_devices (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			device_name VARCHAR(120) NOT NULL,
			phone_number VARCHAR(30) NOT NULL DEFAULT '',
			jid VARCHAR(120) NOT NULL DEFAULT '',
			is_connected BOOLEAN NOT NULL DEFAULT false,
			device_uuid VARCHAR(64) NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_seen TIMESTAMPTZ
		)`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS max_wa_devices INT NOT NULL DEFAULT 3`,
		`ALTER TABLE wa_devices ADD COLUMN IF NOT EXISTS device_uuid VARCHAR(64) NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_wa_devices_user_id ON wa_devices(user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wa_devices_uuid ON wa_devices(device_uuid) WHERE device_uuid <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wa_devices_phone ON wa_devices(phone_number) WHERE phone_number <> ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SeedAdmin(ctx context.Context, username, password, fullName string) (User, error) {
	var existing User
	err := s.db.QueryRowContext(ctx, `SELECT id, username, full_name, role, created_at FROM users WHERE username=$1`, username).
		Scan(&existing.ID, &existing.Username, &existing.FullName, &existing.Role, &existing.CreatedAt)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	var user User
	err = s.db.QueryRowContext(ctx, `INSERT INTO users (username,password_hash,full_name,role) VALUES ($1,$2,$3,'admin') RETURNING id,username,full_name,role,created_at`, username, string(hash), fullName).
		Scan(&user.ID, &user.Username, &user.FullName, &user.Role, &user.CreatedAt)
	return user, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, full_name, role, created_at FROM users ORDER BY id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID,&u.Username,&u.FullName,&u.Role,&u.CreatedAt); err != nil { return nil, err }
		users = append(users,u)
	}
	return users, rows.Err()
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,full_name,role,created_at FROM users WHERE username=$1`, username).
		Scan(&u.ID,&u.Username,&hash,&u.FullName,&u.Role,&u.CreatedAt)
	if err != nil { return User{}, err }
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil { return User{}, errors.New("username atau password salah") }
	return u, nil
}

func (s *Store) CreateUser(ctx context.Context, username,password,fullName,role string) (User,error) {
	if role != "admin" { role = "tester" }
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil { return User{},err }
	var u User
	err = s.db.QueryRowContext(ctx, `INSERT INTO users (username,password_hash,full_name,role) VALUES ($1,$2,$3,$4) RETURNING id,username,full_name,role,created_at`, username,string(hash),fullName,role).
		Scan(&u.ID,&u.Username,&u.FullName,&u.Role,&u.CreatedAt)
	return u,err
}

func (s *Store) SaveWorkbook(ctx context.Context, userID int64, fileName string, fileData, metricsData []byte) error {
	if len(metricsData)==0 { metricsData=[]byte(`{}`) }
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_workbooks (user_id,file_name,file_data,metrics_data) VALUES ($1,$2,$3,$4::jsonb)
		ON CONFLICT (user_id) DO UPDATE SET file_name=excluded.file_name,file_data=excluded.file_data,metrics_data=excluded.metrics_data,updated_at=now()`, userID,fileName,fileData,string(metricsData))
	return err
}

func (s *Store) LoadWorkbook(ctx context.Context, userID int64) (string,[]byte,[]byte,error) {
	var name string
	var data []byte
	var metrics json.RawMessage
	err := s.db.QueryRowContext(ctx, `SELECT file_name,file_data,metrics_data FROM user_workbooks WHERE user_id=$1`,userID).Scan(&name,&data,&metrics)
	return name,data,metrics,err
}

func (s *Store) EnsureUserDirectory(userID int64) (string,error) {
	path := fmt.Sprintf("data/users/%d",userID)
	return path, os.MkdirAll(path,0755)
}

func (s *Store) LoadWorkbookForUser(ctx context.Context, userID int64) (string, []byte, []byte, error) {
	if userID <= 0 {
		return "", nil, nil, errors.New("user id tidak valid")
	}
	var name string
	var data []byte
	var metrics []byte
	err := s.db.QueryRowContext(ctx, `SELECT file_name, file_data, metrics_data FROM user_workbooks WHERE user_id=$1`, userID).
		Scan(&name, &data, &metrics)
	return name, data, metrics, err
}

// LoadSettings returns the stored human-behaviour settings for a user, or
// ok=false when the user never saved any.
func (s *Store) LoadSettings(ctx context.Context, userID int64) (raw []byte, botPhone string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT settings::text, bot_phone FROM user_settings WHERE user_id=$1`, userID).
		Scan(&raw, &botPhone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return raw, botPhone, true, nil
}

// SaveSettings upserts the user's behaviour settings.
func (s *Store) SaveSettings(ctx context.Context, userID int64, settingsRaw []byte, botPhone string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_settings (user_id, bot_phone, settings) VALUES ($1,$2,$3::jsonb)
		ON CONFLICT (user_id) DO UPDATE SET bot_phone=excluded.bot_phone, settings=excluded.settings, updated_at=now()`,
		userID, botPhone, string(settingsRaw))
	return err
}

type WADevice struct {
	ID          int64  `json:"id"`
	DeviceUUID  string `json:"deviceUuid"`
	DeviceName  string `json:"deviceName"`
	PhoneNumber string `json:"phoneNumber"`
	JID         string `json:"jid"`
	IsConnected bool   `json:"isConnected"`
}

// AddWADevice registers a tester device. A phone number may only belong to one
// account, and each account is capped by MaxWADevices.
func (s *Store) AddWADevice(ctx context.Context, userID int64, name, phone, uuid string) (WADevice, error) {
	var limit int
	if err := s.db.QueryRowContext(ctx, `SELECT max_wa_devices FROM users WHERE id=$1`, userID).Scan(&limit); err != nil {
		return WADevice{}, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wa_devices WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return WADevice{}, err
	}
	if limit <= 0 {
		limit = 3
	}
	if count >= limit {
		return WADevice{}, fmt.Errorf("batas maksimal %d nomor WA untuk akun ini sudah tercapai", limit)
	}

	var d WADevice
	err := s.db.QueryRowContext(ctx, `INSERT INTO wa_devices (user_id, device_name, phone_number, jid, device_uuid)
		VALUES ($1,$2,$3,'',$4)
		RETURNING id, device_uuid, device_name, phone_number, jid, is_connected`,
		userID, name, phone, uuid).
		Scan(&d.ID, &d.DeviceUUID, &d.DeviceName, &d.PhoneNumber, &d.JID, &d.IsConnected)
	return d, err
}

func (s *Store) ListWADevices(ctx context.Context, userID int64) ([]WADevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, device_uuid, device_name, phone_number, jid, is_connected
		FROM wa_devices WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WADevice
	for rows.Next() {
		var d WADevice
		if err := rows.Scan(&d.ID, &d.DeviceUUID, &d.DeviceName, &d.PhoneNumber, &d.JID, &d.IsConnected); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWADevice(ctx context.Context, userID, deviceID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM wa_devices WHERE id=$1 AND user_id=$2`, deviceID, userID)
	return err
}

func (s *Store) SetDeviceConnected(ctx context.Context, userID, deviceID int64, connected bool, jid string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wa_devices SET is_connected=$3, jid=$4, last_seen=now() WHERE id=$1 AND user_id=$2`,
		deviceID, userID, connected, jid)
	return err
}

func (s *Store) SetDeviceConnectedByUUID(ctx context.Context, userID int64, deviceUUID string, connected bool, jid string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wa_devices SET is_connected=$3, jid=$4, last_seen=now() WHERE device_uuid=$1 AND user_id=$2`,
		deviceUUID, userID, connected, jid)
	return err
}

func (s *Store) SetMaxWADevices(ctx context.Context, userID int64, limit int) error {
	if limit < 1 || limit > 20 {
		return errors.New("batas perangkat harus antara 1 dan 20")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET max_wa_devices=$2 WHERE id=$1`, userID, limit)
	return err
}
