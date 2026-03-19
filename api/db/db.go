package db

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type VM struct {
	ID        string
	Name      string
	Owner     string
	Namespace string // K8s namespace where this VM's resources live
	State     string // creating, running, error, deleted
	CreatedAt time.Time
	UpdatedAt time.Time
}

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DB) migrate() error {
	_, err := d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS vms (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL UNIQUE,
			owner      TEXT NOT NULL DEFAULT '',
			namespace  TEXT NOT NULL DEFAULT '',
			state      TEXT NOT NULL DEFAULT 'creating',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE TABLE IF NOT EXISTS keys (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			api_key    TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	// Add namespace column for existing databases
	d.conn.Exec(`ALTER TABLE vms ADD COLUMN namespace TEXT NOT NULL DEFAULT ''`)
	return nil
}

type Key struct {
	ID        string
	Name      string
	APIKey    string
	CreatedAt time.Time
}

func (d *DB) InsertKey(k *Key) error {
	_, err := d.conn.Exec(
		`INSERT INTO keys (id, name, api_key, created_at) VALUES (?, ?, ?, ?)`,
		k.ID, k.Name, k.APIKey, k.CreatedAt,
	)
	return err
}

func (d *DB) FindKeyByValue(apiKey string) (*Key, error) {
	k := &Key{}
	err := d.conn.QueryRow(
		`SELECT id, name, api_key, created_at FROM keys WHERE api_key = ?`, apiKey,
	).Scan(&k.ID, &k.Name, &k.APIKey, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (d *DB) FindKeyByName(name string) (*Key, error) {
	k := &Key{}
	err := d.conn.QueryRow(
		`SELECT id, name, api_key, created_at FROM keys WHERE name = ?`, name,
	).Scan(&k.ID, &k.Name, &k.APIKey, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (d *DB) InsertVM(vm *VM) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO vms (id, name, owner, namespace, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		vm.ID, vm.Name, vm.Owner, vm.Namespace, vm.State, vm.CreatedAt, vm.UpdatedAt,
	)
	return err
}

func (d *DB) UpdateVMState(name, state string) error {
	_, err := d.conn.Exec(
		`UPDATE vms SET state = ?, updated_at = ? WHERE name = ?`,
		state, time.Now(), name,
	)
	return err
}

// GetVM returns the VM if it exists and belongs to owner (empty owner = admin, no restriction).
func (d *DB) GetVM(name, owner string) (*VM, error) {
	query := `SELECT id, name, owner, namespace, state, created_at, updated_at FROM vms WHERE name = ? AND state != 'deleted'`
	args := []interface{}{name}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	vm := &VM{}
	err := d.conn.QueryRow(query, args...).Scan(&vm.ID, &vm.Name, &vm.Owner, &vm.Namespace, &vm.State, &vm.CreatedAt, &vm.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return vm, err
}

// ListVMs returns VMs for the given owner (empty owner = admin, returns all).
func (d *DB) ListVMs(owner string) ([]*VM, error) {
	query := `SELECT id, name, owner, namespace, state, created_at, updated_at FROM vms WHERE state != 'deleted'`
	args := []interface{}{}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		vm := &VM{}
		if err := rows.Scan(&vm.ID, &vm.Name, &vm.Owner, &vm.Namespace, &vm.State, &vm.CreatedAt, &vm.UpdatedAt); err != nil {
			return nil, err
		}
		vms = append(vms, vm)
	}
	return vms, rows.Err()
}

func (d *DB) DeleteVM(name string) error {
	_, err := d.conn.Exec(
		`UPDATE vms SET state = 'deleted', updated_at = ? WHERE name = ?`,
		time.Now(), name,
	)
	return err
}
