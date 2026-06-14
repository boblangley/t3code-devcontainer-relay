package t3relay

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Environment represents a discovered devcontainer environment.
type Environment struct {
	ID          string // devcontainer.id label value
	ContainerID string
	Name        string // sanitized name
	Hostname    string // <name>.<domain_suffix>
	IP          string
	Port        int
	Status      string // running | unreachable | stopped
	ProbeJSON   string // raw JSON from probe
	FirstSeen   int64  // unix seconds
	LastSeen    int64  // unix seconds
}

type Exposure struct {
	EnvironmentID string
	Name          string
	HostLabel     string
	Scheme        string
	Port          int
	CreatedAt     int64
	LastSeen      int64
	ExpiresAt     int64
}

// Store manages the SQLite-backed environments table.
type Store struct {
	db *sql.DB
}

const schema = `CREATE TABLE IF NOT EXISTS environments (
  id            TEXT PRIMARY KEY,
  container_id  TEXT NOT NULL,
  name          TEXT NOT NULL,
  hostname      TEXT NOT NULL,
  ip            TEXT NOT NULL,
  port          INTEGER NOT NULL DEFAULT 3773,
  status        TEXT NOT NULL,
  probe_json    TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS exposures (
  environment_id TEXT NOT NULL,
  name           TEXT NOT NULL,
  host_label     TEXT NOT NULL UNIQUE,
  scheme         TEXT NOT NULL DEFAULT 'http',
  port           INTEGER NOT NULL,
  created_at     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  expires_at     INTEGER NOT NULL,
  PRIMARY KEY (environment_id, name),
  FOREIGN KEY (environment_id) REFERENCES environments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_exposures_host_label ON exposures(host_label);
CREATE INDEX IF NOT EXISTS idx_exposures_expires_at ON exposures(expires_at);`

// OpenStore opens (creating if necessary) the SQLite DB at path.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) UpsertExposure(exposure Exposure) error {
	now := time.Now().Unix()
	if exposure.CreatedAt == 0 {
		exposure.CreatedAt = now
	}
	if exposure.LastSeen == 0 {
		exposure.LastSeen = now
	}
	_, err := s.db.Exec(`
		INSERT INTO exposures
			(environment_id, name, host_label, scheme, port, created_at, last_seen, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(environment_id, name) DO UPDATE SET
			host_label = excluded.host_label,
			scheme     = excluded.scheme,
			port       = excluded.port,
			last_seen  = excluded.last_seen,
			expires_at = excluded.expires_at
	`,
		exposure.EnvironmentID, exposure.Name, exposure.HostLabel, exposure.Scheme, exposure.Port,
		exposure.CreatedAt, exposure.LastSeen, exposure.ExpiresAt,
	)
	return err
}

func (s *Store) GetExposureByHostLabel(hostLabel string) (Exposure, bool) {
	now := time.Now().Unix()
	var e Exposure
	err := s.db.QueryRow(`
		SELECT environment_id, name, host_label, scheme, port, created_at, last_seen, expires_at
		FROM exposures
		WHERE host_label = ? AND expires_at > ?
	`, hostLabel, now).Scan(&e.EnvironmentID, &e.Name, &e.HostLabel, &e.Scheme, &e.Port, &e.CreatedAt, &e.LastSeen, &e.ExpiresAt)
	if err != nil {
		return Exposure{}, false
	}
	return e, true
}

func (s *Store) ListExposures(environmentID string) []Exposure {
	now := time.Now().Unix()
	rows, err := s.db.Query(`
		SELECT environment_id, name, host_label, scheme, port, created_at, last_seen, expires_at
		FROM exposures
		WHERE environment_id = ? AND expires_at > ?
		ORDER BY name
	`, environmentID, now)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var exposures []Exposure
	for rows.Next() {
		var e Exposure
		if err := rows.Scan(&e.EnvironmentID, &e.Name, &e.HostLabel, &e.Scheme, &e.Port, &e.CreatedAt, &e.LastSeen, &e.ExpiresAt); err != nil {
			continue
		}
		exposures = append(exposures, e)
	}
	return exposures
}

func (s *Store) DeleteExposure(environmentID, name string) (bool, error) {
	result, err := s.db.Exec(`DELETE FROM exposures WHERE environment_id = ? AND name = ?`, environmentID, name)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func (s *Store) DeleteExpiredExposures() error {
	_, err := s.db.Exec(`DELETE FROM exposures WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Upsert inserts or updates an environment row.
func (s *Store) Upsert(env Environment) error {
	now := time.Now().Unix()
	if env.LastSeen == 0 {
		env.LastSeen = now
	}
	_, err := s.db.Exec(`
		INSERT INTO environments
			(id, container_id, name, hostname, ip, port, status, probe_json, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			container_id = excluded.container_id,
			name         = excluded.name,
			hostname     = excluded.hostname,
			ip           = excluded.ip,
			port         = excluded.port,
			status       = excluded.status,
			probe_json   = excluded.probe_json,
			last_seen    = excluded.last_seen
	`,
		env.ID, env.ContainerID, env.Name, env.Hostname, env.IP, env.Port,
		env.Status, env.ProbeJSON, env.FirstSeen, env.LastSeen,
	)
	return err
}

// MarkStopped sets status='stopped' for the given container_id.
func (s *Store) MarkStopped(containerID string) error {
	_, err := s.db.Exec(`UPDATE environments SET status='stopped' WHERE container_id = ?`, containerID)
	return err
}

// DeleteByID removes an environment row by its internal store ID.
func (s *Store) DeleteByID(id string) (bool, error) {
	if _, err := s.db.Exec(`DELETE FROM exposures WHERE environment_id = ?`, id); err != nil {
		return false, err
	}
	result, err := s.db.Exec(`DELETE FROM environments WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

// List returns all environments ordered by last_seen desc.
func (s *Store) List() []Environment {
	rows, err := s.db.Query(`SELECT id, container_id, name, hostname, ip, port, status, COALESCE(probe_json,''), first_seen, last_seen FROM environments ORDER BY last_seen DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var envs []Environment
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.ContainerID, &e.Name, &e.Hostname, &e.IP, &e.Port, &e.Status, &e.ProbeJSON, &e.FirstSeen, &e.LastSeen); err != nil {
			continue
		}
		envs = append(envs, e)
	}
	return envs
}

// GetByHost returns an environment by its full hostname.
func (s *Store) GetByHost(hostname string) (Environment, bool) {
	var e Environment
	err := s.db.QueryRow(`SELECT id, container_id, name, hostname, ip, port, status, COALESCE(probe_json,''), first_seen, last_seen FROM environments WHERE hostname = ?`, hostname).
		Scan(&e.ID, &e.ContainerID, &e.Name, &e.Hostname, &e.IP, &e.Port, &e.Status, &e.ProbeJSON, &e.FirstSeen, &e.LastSeen)
	if err != nil {
		return Environment{}, false
	}
	return e, true
}

// GetByName returns an environment by its canonical published name.
func (s *Store) GetByName(name string) (Environment, bool) {
	var e Environment
	err := s.db.QueryRow(`SELECT id, container_id, name, hostname, ip, port, status, COALESCE(probe_json,''), first_seen, last_seen FROM environments WHERE name = ?`, name).
		Scan(&e.ID, &e.ContainerID, &e.Name, &e.Hostname, &e.IP, &e.Port, &e.Status, &e.ProbeJSON, &e.FirstSeen, &e.LastSeen)
	if err != nil {
		return Environment{}, false
	}
	return e, true
}

// GetByID returns an environment by its devcontainer ID.
func (s *Store) GetByID(id string) (Environment, bool) {
	var e Environment
	err := s.db.QueryRow(`SELECT id, container_id, name, hostname, ip, port, status, COALESCE(probe_json,''), first_seen, last_seen FROM environments WHERE id = ?`, id).
		Scan(&e.ID, &e.ContainerID, &e.Name, &e.Hostname, &e.IP, &e.Port, &e.Status, &e.ProbeJSON, &e.FirstSeen, &e.LastSeen)
	if err != nil {
		return Environment{}, false
	}
	return e, true
}
