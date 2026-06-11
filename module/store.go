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
);`

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
