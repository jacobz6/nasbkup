// Package crypto implements AES-256-GCM encryption for backup files. It
// manages a master key file, derives per-file Data Encryption Keys (DEKs)
// using HKDF, and provides streaming-safe encrypt/decrypt operations.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/hkdf"
)

const (
	// nonceSize is the length of the AES-GCM nonce in bytes (12 bytes as per NIST).
	nonceSize = 12

	// keySize is the length of the AES-256 key in bytes.
	keySize = 32

	// hkdfInfo is the fixed info string used for HKDF key derivation.
	hkdfInfo = "nas-backup-dek-v1"
)

// defaultChunkSize is the default chunk size for streaming encryption (256KB).
const defaultChunkSize = 256 * 1024

// Encryptor manages AES-256-GCM encryption for backup files.
type Encryptor struct {
	masterKeyPath string
	masterKey     []byte
	chunkSize     int // Size of each encryption chunk in bytes.
}

// NewEncryptor creates an Encryptor that loads or generates a master key
// from the given key file path.
func NewEncryptor(keyFilePath string) (*Encryptor, error) {
	e := &Encryptor{
		masterKeyPath: keyFilePath,
		chunkSize:     defaultChunkSize,
	}
	if err := e.loadOrGenerateMasterKey(); err != nil {
		return nil, fmt.Errorf("initialize encryptor: %w", err)
	}
	return e, nil
}

// SetChunkSize sets the chunk size for streaming encryption/decryption.
// A larger chunk size reduces per-chunk overhead but increases memory usage.
// Must be at least 1 KB. The default is 256 KB.
func (e *Encryptor) SetChunkSize(size int) {
	if size >= 1024 {
		e.chunkSize = size
	}
}

// loadOrGenerateMasterKey loads the master key from the configured file, or
// generates a new 32-byte key if the file does not exist. The key file is
// created with 0600 permissions to prevent unauthorized access.
func (e *Encryptor) loadOrGenerateMasterKey() error {
	// Try to load an existing key first.
	key, err := LoadMasterKey(e.masterKeyPath)
	if err == nil && len(key) == keySize {
		e.masterKey = key
		return nil
	}

	// File doesn't exist or is invalid — generate a new key.
	key, err = GenerateMasterKey()
	if err != nil {
		return fmt.Errorf("generate master key: %w", err)
	}

	if err := SaveMasterKey(e.masterKeyPath, key); err != nil {
		return fmt.Errorf("save master key: %w", err)
	}

	e.masterKey = key
	return nil
}

// deriveDEK derives a Data Encryption Key from the master key and a random
// salt using HKDF-SHA256. The salt ensures that each encryption operation
// uses a unique DEK even if the master key is the same.
func (e *Encryptor) deriveDEK(salt []byte) ([]byte, error) {
	h := hkdf.New(sha256.New, e.masterKey, salt, []byte(hkdfInfo))
	dek := make([]byte, keySize)
	if _, err := io.ReadFull(h, dek); err != nil {
		return nil, fmt.Errorf("derive DEK via HKDF: %w", err)
	}
	return dek, nil
}

// EncryptFile encrypts the file at inputPath using AES-256-GCM and writes the
// ciphertext to outputPath using a streaming approach to handle large files
// without loading them entirely into memory. The output format is:
// salt (32 bytes) || chunk1: nonce(12) || ciphertext+tag || chunk2: ... || ...
// Each chunk is 256KB to balance memory usage and overhead.
//
// The base64-encoded nonce of the first chunk is returned so it can be stored
// in the backup metadata (used for integrity verification during decryption).
//
// Encryption steps:
//  1. Generate a random 32-byte salt for HKDF.
//  2. Derive a unique DEK from the master key using HKDF(salt).
//  3. For each chunk: generate a random 12-byte nonce, encrypt with AES-GCM.
//  4. Write salt || chunk1(nonce+ciphertext+tag) || chunk2(...) || ...
func (e *Encryptor) EncryptFile(inputPath, outputPath string) (iv string, err error) {
	// Generate a random salt for HKDF.
	salt := make([]byte, keySize)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	// Derive a unique DEK.
	dek, err := e.deriveDEK(salt)
	if err != nil {
		return "", err
	}

	// Create AES-GCM cipher.
	block, err := aes.NewCipher(dek)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	// Open input and output files.
	in, err := os.Open(inputPath)
	if err != nil {
		return "", fmt.Errorf("open plaintext file %q: %w", inputPath, err)
	}
	defer in.Close()

	if err := os.MkdirAll(dirOf(outputPath), 0700); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("open output file %q: %w", outputPath, err)
	}
	defer out.Close()

	// Write salt first.
	if _, err := out.Write(salt); err != nil {
		return "", fmt.Errorf("write salt: %w", err)
	}

	// Read and encrypt in chunks.
	buf := make([]byte, e.chunkSize)
	var firstNonce []byte
	chunkIdx := 0

	for {
		n, readErr := in.Read(buf)
		if n == 0 {
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return "", fmt.Errorf("read chunk %d: %w", chunkIdx, readErr)
			}
		}

		chunk := buf[:n]

		// Generate a random nonce for this chunk.
		nonce := make([]byte, nonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return "", fmt.Errorf("generate nonce for chunk %d: %w", chunkIdx, err)
		}

		// Save first chunk's nonce as the "IV" for metadata storage.
		if chunkIdx == 0 {
			firstNonce = nonce
		}

		// Encrypt: Seal appends the auth tag to the ciphertext.
		ciphertext := aesgcm.Seal(nil, nonce, chunk, nil)

		// Write nonce || ciphertext+tag for this chunk.
		if _, err := out.Write(nonce); err != nil {
			return "", fmt.Errorf("write nonce for chunk %d: %w", chunkIdx, err)
		}
		if _, err := out.Write(ciphertext); err != nil {
			return "", fmt.Errorf("write ciphertext for chunk %d: %w", chunkIdx, err)
		}

		chunkIdx++

		if readErr == io.EOF {
			break
		}
	}

	if firstNonce == nil {
		// Empty file: write a single nonce+empty ciphertext so decryption works.
		nonce := make([]byte, nonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return "", fmt.Errorf("generate nonce for empty file: %w", err)
		}
		firstNonce = nonce
		ciphertext := aesgcm.Seal(nil, nonce, []byte{}, nil)
		if _, err := out.Write(nonce); err != nil {
			return "", fmt.Errorf("write nonce for empty file: %w", err)
		}
		if _, err := out.Write(ciphertext); err != nil {
			return "", fmt.Errorf("write ciphertext for empty file: %w", err)
		}
	}

	return base64.StdEncoding.EncodeToString(firstNonce), nil
}

// DecryptFile decrypts the file at inputPath (produced by EncryptFile) and
// writes the plaintext to outputPath using a streaming approach.
// The ivBase64 parameter is the base64-encoded nonce of the first chunk,
// returned by EncryptFile (used for consistency/integrity verification).
//
// Decryption steps:
//  1. Read the encrypted file in streaming mode.
//  2. Extract salt (first 32 bytes).
//  3. Derive the same DEK from the master key using HKDF(salt).
//  4. For each chunk: read nonce, then ciphertext+tag, decrypt with AES-GCM.
//  5. Write plaintext chunks to the output file.
func (e *Encryptor) DecryptFile(inputPath, outputPath, ivBase64 string) error {
	// Chunk buffer: plaintext chunk size + GCM tag overhead (16 bytes).
	chunkSize := e.chunkSize + 16

	// Read salt (first 32 bytes).
	salt := make([]byte, keySize)
	{
		f, err := os.Open(inputPath)
		if err != nil {
			return fmt.Errorf("open encrypted file %q: %w", inputPath, err)
		}
		if _, err := io.ReadFull(f, salt); err != nil {
			f.Close()
			return fmt.Errorf("read salt from encrypted file %q: %w", inputPath, err)
		}
		f.Close()
	}

	// Derive the same DEK.
	dek, err := e.deriveDEK(salt)
	if err != nil {
		return err
	}

	// Create AES-GCM cipher.
	block, err := aes.NewCipher(dek)
	if err != nil {
		return fmt.Errorf("create AES cipher: %w", err)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create GCM: %w", err)
	}

	// Open encrypted input file.
	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open encrypted file %q: %w", inputPath, err)
	}
	defer in.Close()

	// Skip past the salt (already read).
	if _, err := in.Seek(int64(keySize), io.SeekStart); err != nil {
		return fmt.Errorf("seek past salt: %w", err)
	}

	if err := os.MkdirAll(dirOf(outputPath), 0700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open output file %q: %w", outputPath, err)
	}
	defer out.Close()

	// Decode expected first-chunk nonce for consistency check.
	expectedFirstNonce, err := base64.StdEncoding.DecodeString(ivBase64)
	if err != nil {
		return fmt.Errorf("decode IV from base64: %w", err)
	}

	// Read and decrypt chunks.
	nonceBuf := make([]byte, nonceSize)
	chunkBuf := make([]byte, chunkSize)
	chunkIdx := 0

	for {
		// Read nonce for this chunk.
		_, readErr := io.ReadFull(in, nonceBuf)
		if readErr != nil {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				// End of file — no more chunks.
				break
			}
			return fmt.Errorf("read nonce for chunk %d: %w", chunkIdx, readErr)
		}

		// Verify first chunk's nonce matches the stored IV.
		if chunkIdx == 0 && len(expectedFirstNonce) == nonceSize {
			for i := range nonceBuf {
				if nonceBuf[i] != expectedFirstNonce[i] {
					return fmt.Errorf("nonce mismatch on first chunk: file may be corrupted or tampered with")
				}
			}
		}

		// Read ciphertext for this chunk. Use io.ReadFull instead of a single
		// in.Read: Read is allowed to return fewer bytes than requested (a
		// "short read"), which would truncate the GCM auth tag from the tail
		// of the chunk and cause spurious "message authentication failed"
		// errors. This is especially likely on NFS/SMB mounts or when a read
		// is interrupted (EINTR). ReadFull loops until the buffer is full or
		// EOF, so every full chunk reads exactly chunkSize bytes. The final
		// chunk is smaller and yields io.ErrUnexpectedEOF with the partial
		// bytes, which we decrypt as the last chunk.
		n, readErr := io.ReadFull(in, chunkBuf)
		if n == 0 {
			// No ciphertext after a nonce — malformed file (nonce without
			// ciphertext). Treat as end of stream.
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
			return fmt.Errorf("read ciphertext for chunk %d: %w", chunkIdx, readErr)
		}
		ciphertext := chunkBuf[:n]

		// Decrypt this chunk.
		plaintext, err := aesgcm.Open(nil, nonceBuf, ciphertext, nil)
		if err != nil {
			return fmt.Errorf("decrypt chunk %d: %w", chunkIdx, err)
		}

		// Write plaintext to output.
		if _, err := out.Write(plaintext); err != nil {
			return fmt.Errorf("write plaintext for chunk %d: %w", chunkIdx, err)
		}

		chunkIdx++

		// io.ErrUnexpectedEOF means we read a partial (final) chunk; the file
		// ends here. io.EOF with n>0 cannot happen for ReadFull, but guard
		// anyway.
		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read ciphertext for chunk %d: %w", chunkIdx, readErr)
		}
	}

	return nil
}

// GenerateMasterKey generates a cryptographically secure 32-byte random key.
func GenerateMasterKey() ([]byte, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}
	return key, nil
}

// SaveMasterKey writes a master key to the given file path with 0600
// permissions, ensuring only the owner can read it.
func SaveMasterKey(path string, key []byte) error {
	if err := os.MkdirAll(dirOf(path), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return fmt.Errorf("write master key to %q: %w", path, err)
	}
	return nil
}

// LoadMasterKey reads a master key from the given file path.
func LoadMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key from %q: %w", path, err)
	}
	if len(data) != keySize {
		return nil, fmt.Errorf("master key at %q has wrong size: got %d, want %d", path, len(data), keySize)
	}
	return data, nil
}

// dirOf returns the directory component of a file path, or "." if the path
// has no directory component.
func dirOf(path string) string {
	d := len(path) - 1
	for d >= 0 && path[d] != '/' && path[d] != os.PathSeparator {
		d--
	}
	if d < 0 {
		return "."
	}
	return path[:d]
}
