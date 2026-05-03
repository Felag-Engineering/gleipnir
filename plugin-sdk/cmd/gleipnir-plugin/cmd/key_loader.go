package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// loadSecretKey resolves and decrypts a secret key. Resolution order:
//  1. --key-stdin: read the .key file content from stdin
//  2. env GLEIPNIR_PLUGIN_SIGNING_KEY: path to .key file (or inline base64 .key content)
//  3. --key flag (flagKey)
//  4. default: ~/.config/gleipnir-plugin/keys/signing.key
//
// Passphrase resolution order:
//  1. env GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE
//  2. interactive terminal prompt (if stdin is a TTY)
//
// Returns the raw Ed25519 private key, the key ID, the default path to the
// sibling .pub file (used when --pubkey is not given), and any error.
func loadSecretKey(flagKey string, flagKeyStdin bool, stdin io.Reader) (
	raw [64]byte, keyID [8]byte, defaultPubPath string, err error,
) {
	var keyData []byte

	switch {
	case flagKeyStdin:
		keyData, err = io.ReadAll(stdin)
		if err != nil {
			return [64]byte{}, [8]byte{}, "", fmt.Errorf("read key from stdin: %w", err)
		}

	case os.Getenv("GLEIPNIR_PLUGIN_SIGNING_KEY") != "":
		envVal := os.Getenv("GLEIPNIR_PLUGIN_SIGNING_KEY")
		if strings.HasPrefix(envVal, "untrusted comment:") {
			// Inline .key content.
			keyData = []byte(envVal)
		} else {
			// Path to .key file.
			keyData, err = os.ReadFile(envVal)
			if err != nil {
				return [64]byte{}, [8]byte{}, "", fmt.Errorf("read key from GLEIPNIR_PLUGIN_SIGNING_KEY=%s: %w", envVal, err)
			}
			defaultPubPath = strings.TrimSuffix(envVal, ".key") + ".pub"
		}

	default:
		keyPath := flagKey
		if keyPath == "" {
			keyPath = defaultKeyPath()
		}
		keyData, err = os.ReadFile(keyPath)
		if err != nil {
			return [64]byte{}, [8]byte{}, "", fmt.Errorf("read key %s: %w", keyPath, err)
		}
		defaultPubPath = strings.TrimSuffix(keyPath, ".key") + ".pub"
	}

	sk, _, err := signing.ParseSecretKey(keyData)
	if err != nil {
		return [64]byte{}, [8]byte{}, "", fmt.Errorf("parse secret key: %w", err)
	}

	// Unencrypted key: skip passphrase.
	if sk.KDFAlg == ([2]byte{}) {
		slog.Warn("using unencrypted signing key — not recommended outside testing")
		var decryptedKeyID [8]byte
		raw, decryptedKeyID, err = signing.DecryptSecretKey(sk, nil)
		if err != nil {
			return [64]byte{}, [8]byte{}, "", fmt.Errorf("decrypt key: %w", err)
		}
		return raw, decryptedKeyID, defaultPubPath, nil
	}

	passphrase, err := resolvePassphrase(stdin)
	if err != nil {
		return [64]byte{}, [8]byte{}, "", err
	}

	var decryptedKeyID [8]byte
	raw, decryptedKeyID, err = signing.DecryptSecretKey(sk, passphrase)
	if err != nil {
		return [64]byte{}, [8]byte{}, "", fmt.Errorf("decrypt key (wrong passphrase?): %w", err)
	}
	return raw, decryptedKeyID, defaultPubPath, nil
}

// resolvePassphrase returns the passphrase from env or terminal prompt.
func resolvePassphrase(stdin io.Reader) ([]byte, error) {
	if v := os.Getenv("GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE"); v != "" {
		return []byte(v), nil
	}

	// Try terminal prompt.
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Enter passphrase for signing key: ")
		pass, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		return pass, nil
	}

	return nil, fmt.Errorf("no passphrase available (set GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE or use --passphrase-stdin/--unencrypted)")
}

// promptPassphraseWithConfirm prompts for a passphrase twice and returns it.
// Used during key generation.
func promptPassphraseWithConfirm() ([]byte, error) {
	fd := int(os.Stdin.Fd())
	fmt.Fprint(os.Stderr, "Enter passphrase for new signing key: ")
	pass, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read passphrase: %w", err)
	}

	fmt.Fprint(os.Stderr, "Confirm passphrase: ")
	confirm, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read passphrase confirm: %w", err)
	}

	if string(pass) != string(confirm) {
		return nil, fmt.Errorf("passphrases do not match")
	}
	return pass, nil
}

// defaultKeyPath returns the default signing key path.
func defaultKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "signing.key"
	}
	return filepath.Join(home, ".config", "gleipnir-plugin", "keys", "signing.key")
}
