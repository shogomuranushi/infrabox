package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

func infraboxKeyPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ib", "id_infrabox")
}

func infraboxPubKeyPath() string {
	return infraboxKeyPath() + ".pub"
}

// ensureSSHKey generates ~/.ib/id_infrabox if it does not exist.
func ensureSSHKey() error {
	keyPath := infraboxKeyPath()
	if _, err := os.Stat(keyPath); err == nil {
		return nil // already exists
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	// write private key in OpenSSH format
	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(privPEM), 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	// write public key in authorized_keys format
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	if err := os.WriteFile(infraboxPubKeyPath(), pubBytes, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	fmt.Printf("✓ SSH key generated: %s\n", keyPath)
	return nil
}

// loadInfraboxPubKey returns the contents of ~/.ib/id_infrabox.pub.
func loadInfraboxPubKey() (string, error) {
	data, err := os.ReadFile(infraboxPubKeyPath())
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", infraboxPubKeyPath(), err)
	}
	return string(data), nil
}
