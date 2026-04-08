package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type VM struct {
	ID          string
	Name        string
	Owner       string
	Namespace   string // K8s namespace where this VM's resources live
	State       string // creating, running, error, deleted
	AuthEnabled bool   // whether oauth2-proxy auth is enabled for this VM's ingress
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SetupScript struct {
	ID              string
	Owner           string
	EncryptedScript string // base64-encoded AES-256-GCM ciphertext
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type DB struct {
	conn          *sql.DB
	encryptionKey []byte // 32-byte key for AES-256-GCM
}

func Open(path string, encryptionKey []byte) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d := &DB{conn: conn, encryptionKey: encryptionKey}
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
	// Add auth_enabled column for existing databases (default 1 = enabled)
	d.conn.Exec(`ALTER TABLE vms ADD COLUMN auth_enabled INTEGER NOT NULL DEFAULT 1`)

	// Invitation codes table
	_, err = d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS invitation_codes (
			id         TEXT PRIMARY KEY,
			code_hash  TEXT NOT NULL UNIQUE,
			used       INTEGER NOT NULL DEFAULT 0,
			used_by    TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			used_at    DATETIME
		);
	`)
	if err != nil {
		return err
	}

	// Setup scripts table (one per user, encrypted)
	_, err = d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS setup_scripts (
			id               TEXT PRIMARY KEY,
			owner            TEXT NOT NULL UNIQUE,
			encrypted_script TEXT NOT NULL,
			created_at       DATETIME NOT NULL,
			updated_at       DATETIME NOT NULL
		);
	`)
	return err
}

type Key struct {
	ID        string
	Name      string
	APIKey    string
	CreatedAt time.Time
}

type InvitationCode struct {
	ID        string
	CodeHash  string // SHA-256 hash of the code
	Used      bool
	UsedBy    string // name of user who redeemed
	CreatedAt time.Time
	UsedAt    *time.Time
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (d *DB) InsertVM(vm *VM) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO vms (id, name, owner, namespace, state, auth_enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		vm.ID, vm.Name, vm.Owner, vm.Namespace, vm.State, boolToInt(vm.AuthEnabled), vm.CreatedAt, vm.UpdatedAt,
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
	query := `SELECT id, name, owner, namespace, state, auth_enabled, created_at, updated_at FROM vms WHERE name = ? AND state != 'deleted'`
	args := []interface{}{name}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	vm := &VM{}
	var authEnabled int
	err := d.conn.QueryRow(query, args...).Scan(&vm.ID, &vm.Name, &vm.Owner, &vm.Namespace, &vm.State, &authEnabled, &vm.CreatedAt, &vm.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	vm.AuthEnabled = authEnabled != 0
	return vm, err
}

// ListVMs returns VMs owned by the given owner (always filters by owner).
func (d *DB) ListVMs(owner string) ([]*VM, error) {
	query := `SELECT id, name, owner, namespace, state, auth_enabled, created_at, updated_at FROM vms WHERE state != 'deleted' AND owner = ?`
	rows, err := d.conn.Query(query, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		vm := &VM{}
		var authEnabled int
		if err := rows.Scan(&vm.ID, &vm.Name, &vm.Owner, &vm.Namespace, &vm.State, &authEnabled, &vm.CreatedAt, &vm.UpdatedAt); err != nil {
			return nil, err
		}
		vm.AuthEnabled = authEnabled != 0
		vms = append(vms, vm)
	}
	return vms, rows.Err()
}

// ListAllVMs returns all VMs regardless of owner (for admin use).
func (d *DB) ListAllVMs() ([]*VM, error) {
	rows, err := d.conn.Query(
		`SELECT id, name, owner, namespace, state, auth_enabled, created_at, updated_at FROM vms WHERE state != 'deleted' ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		vm := &VM{}
		var authEnabled int
		if err := rows.Scan(&vm.ID, &vm.Name, &vm.Owner, &vm.Namespace, &vm.State, &authEnabled, &vm.CreatedAt, &vm.UpdatedAt); err != nil {
			return nil, err
		}
		vm.AuthEnabled = authEnabled != 0
		vms = append(vms, vm)
	}
	return vms, rows.Err()
}

// UpdateVMAuth updates the auth_enabled flag for a VM.
func (d *DB) UpdateVMAuth(name, owner string, enabled bool) error {
	query := `UPDATE vms SET auth_enabled = ?, updated_at = ? WHERE name = ? AND state != 'deleted'`
	args := []interface{}{boolToInt(enabled), time.Now(), name}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	res, err := d.conn.Exec(query, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("VM not found")
	}
	return nil
}

// InsertInvitationCode stores a new invitation code (hashed).
func (d *DB) InsertInvitationCode(ic *InvitationCode) error {
	_, err := d.conn.Exec(
		`INSERT INTO invitation_codes (id, code_hash, used, used_by, created_at) VALUES (?, ?, 0, '', ?)`,
		ic.ID, ic.CodeHash, ic.CreatedAt,
	)
	return err
}

// RedeemInvitationCode atomically marks an invitation code as used.
// Returns an error if the code is invalid or already used.
func (d *DB) RedeemInvitationCode(rawCode string) error {
	h := hashString(rawCode)
	res, err := d.conn.Exec(
		`UPDATE invitation_codes SET used = 1, used_at = ? WHERE code_hash = ? AND used = 0`,
		time.Now(), h,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("invalid or already used invitation code")
	}
	return nil
}

// ListInvitationCodes returns all invitation codes (for admin).
func (d *DB) ListInvitationCodes() ([]*InvitationCode, error) {
	rows, err := d.conn.Query(
		`SELECT id, code_hash, used, used_by, created_at, used_at FROM invitation_codes ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codes []*InvitationCode
	for rows.Next() {
		ic := &InvitationCode{}
		if err := rows.Scan(&ic.ID, &ic.CodeHash, &ic.Used, &ic.UsedBy, &ic.CreatedAt, &ic.UsedAt); err != nil {
			return nil, err
		}
		codes = append(codes, ic)
	}
	return codes, rows.Err()
}

// SaveSetupScript creates or updates the setup script for the given owner.
// The script content is encrypted before storage.
func (d *DB) SaveSetupScript(id, owner string, script []byte) error {
	if len(d.encryptionKey) == 0 {
		return fmt.Errorf("encryption key not configured")
	}
	encrypted, err := Encrypt(script, d.encryptionKey)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	now := time.Now()
	_, err = d.conn.Exec(
		`INSERT INTO setup_scripts (id, owner, encrypted_script, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(owner) DO UPDATE SET encrypted_script = excluded.encrypted_script, updated_at = excluded.updated_at`,
		id, owner, encrypted, now, now,
	)
	return err
}

// GetSetupScript returns the decrypted setup script for the given owner.
func (d *DB) GetSetupScript(owner string) ([]byte, error) {
	if len(d.encryptionKey) == 0 {
		return nil, fmt.Errorf("encryption key not configured")
	}
	var encrypted string
	err := d.conn.QueryRow(
		`SELECT encrypted_script FROM setup_scripts WHERE owner = ?`, owner,
	).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Decrypt(encrypted, d.encryptionKey)
}

// DeleteSetupScript removes the setup script for the given owner.
func (d *DB) DeleteSetupScript(owner string) error {
	res, err := d.conn.Exec(`DELETE FROM setup_scripts WHERE owner = ?`, owner)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("setup script not found")
	}
	return nil
}

// RenameVM changes the name of an existing VM. If owner is non-empty, only the owner's VM is affected.
func (d *DB) RenameVM(oldName, newName, owner string) error {
	query := `UPDATE vms SET name = ?, updated_at = ? WHERE name = ? AND state != 'deleted'`
	args := []interface{}{newName, time.Now(), oldName}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	res, err := d.conn.Exec(query, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("VM not found")
	}
	return nil
}

// DeleteVM soft-deletes a VM. If owner is non-empty, only the owner's VM is affected.
func (d *DB) DeleteVM(name, owner string) error {
	query := `UPDATE vms SET state = 'deleted', updated_at = ? WHERE name = ? AND state != 'deleted'`
	args := []interface{}{time.Now(), name}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	_, err := d.conn.Exec(query, args...)
	return err
}
