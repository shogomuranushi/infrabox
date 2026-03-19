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

// ensureSSHKey は ~/.ib/id_infrabox がなければ生成する。
func ensureSSHKey() error {
	keyPath := infraboxKeyPath()
	if _, err := os.Stat(keyPath); err == nil {
		return nil // already exists
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	// 秘密鍵を OpenSSH 形式で書き込む
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

	// 公開鍵を authorized_keys 形式で書き込む
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

// loadInfraboxPubKey は ~/.ib/id_infrabox.pub の内容を返す。
func loadInfraboxPubKey() (string, error) {
	data, err := os.ReadFile(infraboxPubKeyPath())
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", infraboxPubKeyPath(), err)
	}
	return string(data), nil
}
