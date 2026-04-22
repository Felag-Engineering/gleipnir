# Troubleshooting

Common issues will be added here as they come up. If you hit something not listed below, please open an issue.

## Known issues

### Lost encryption key

If `GLEIPNIR_ENCRYPTION_KEY` is lost, there is no recovery path. The AES-256-GCM ciphertext stored in the database cannot be decrypted without the original key — every provider API key and webhook secret stored before the key was lost is permanently gone.

To get the instance working again, follow the key rotation workaround in [Operations — Rotating the encryption key](operations.md#rotating-the-encryption-key-v10-known-limitation). This requires re-entering all provider API keys and rotating all webhook secrets from scratch.
