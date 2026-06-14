package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateMasterKey 测试主密钥生成的长度和随机性
func TestGenerateMasterKey(t *testing.T) {
	key1, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey failed: %v", err)
	}
	if len(key1) != keySize {
		t.Fatalf("expected key size %d, got %d", keySize, len(key1))
	}

	// 第二次生成的密钥应该与第一次不同（随机性）
	key2, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey failed: %v", err)
	}
	if bytes.Equal(key1, key2) {
		t.Fatal("two consecutive master keys are identical (randomness issue)")
	}
}

// TestSaveAndLoadMasterKey 测试主密钥的保存和加载
func TestSaveAndLoadMasterKey(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")

	key, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey failed: %v", err)
	}

	if err := SaveMasterKey(keyPath, key); err != nil {
		t.Fatalf("SaveMasterKey failed: %v", err)
	}

	// 验证文件权限
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected file permission 0600, got %o", info.Mode().Perm())
	}

	// 验证加载
	loaded, err := LoadMasterKey(keyPath)
	if err != nil {
		t.Fatalf("LoadMasterKey failed: %v", err)
	}
	if !bytes.Equal(key, loaded) {
		t.Fatal("loaded key does not match saved key")
	}
}

// TestLoadMasterKeyWrongSize 测试加载错误大小的密钥文件
func TestLoadMasterKeyWrongSize(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "wrong.key")

	// 写入一个长度错误的密钥
	wrongKey := []byte("short-key")
	if err := os.WriteFile(keyPath, wrongKey, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := LoadMasterKey(keyPath)
	if err == nil {
		t.Fatal("expected error for wrong-size key file, got nil")
	}
}

// TestLoadMasterKeyNotFound 测试加载不存在的密钥文件
func TestLoadMasterKeyNotFound(t *testing.T) {
	_, err := LoadMasterKey("/nonexistent/path/key.bin")
	if err == nil {
		t.Fatal("expected error for nonexistent key file, got nil")
	}
}

// TestNewEncryptorCreatesKey 测试 NewEncryptor 在密钥文件不存在时自动创建
func TestNewEncryptorCreatesKey(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "new.key")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}
	if e.masterKey == nil {
		t.Fatal("encryptor master key is nil")
	}
	if len(e.masterKey) != keySize {
		t.Fatalf("expected master key size %d, got %d", keySize, len(e.masterKey))
	}

	// 验证密钥文件已创建
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Fatal("key file was not created")
	}
}

// TestNewEncryptorLoadsExisting 测试 NewEncryptor 加载已存在的密钥
func TestNewEncryptorLoadsExisting(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "existing.key")

	// 预先生成一个密钥
	originalKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey failed: %v", err)
	}
	if err := SaveMasterKey(keyPath, originalKey); err != nil {
		t.Fatalf("SaveMasterKey failed: %v", err)
	}

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}
	if !bytes.Equal(e.masterKey, originalKey) {
		t.Fatal("encryptor did not load existing key")
	}
}

// TestEncryptDecryptSmallFile 测试小文件的加密解密（单块）
func TestEncryptDecryptSmallFile(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// 创建测试文件（100 字节）
	originalContent := []byte("Hello, this is a small test file for encryption!")
	plainPath := filepath.Join(tmpDir, "plain.txt")
	if err := os.WriteFile(plainPath, originalContent, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 加密
	encryptedPath := filepath.Join(tmpDir, "encrypted.enc")
	iv, err := e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}
	if iv == "" {
		t.Fatal("iv is empty after encryption")
	}

	// 解密
	decryptedPath := filepath.Join(tmpDir, "decrypted.txt")
	if err := e.DecryptFile(encryptedPath, decryptedPath, iv); err != nil {
		t.Fatalf("DecryptFile failed: %v", err)
	}

	// 验证内容一致
	decrypted, err := os.ReadFile(decryptedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(originalContent, decrypted) {
		t.Fatalf("decrypted content does not match original\ngot: %q\nwant: %q", decrypted, originalContent)
	}
}

// TestEncryptDecryptMediumFile 测试中等大小文件的加密解密（多块）
func TestEncryptDecryptMediumFile(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// 创建 1MB 的测试文件（跨越多个 chunk）
	originalContent := make([]byte, 1024*1024)
	for i := range originalContent {
		originalContent[i] = byte(i % 256)
	}
	plainPath := filepath.Join(tmpDir, "plain_1mb.dat")
	if err := os.WriteFile(plainPath, originalContent, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	encryptedPath := filepath.Join(tmpDir, "encrypted.enc")
	iv, err := e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}

	decryptedPath := filepath.Join(tmpDir, "decrypted.dat")
	if err := e.DecryptFile(encryptedPath, decryptedPath, iv); err != nil {
		t.Fatalf("DecryptFile failed: %v", err)
	}

	decrypted, err := os.ReadFile(decryptedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(originalContent, decrypted) {
		t.Fatal("decrypted content does not match original (1MB file)")
	}
}

// TestEncryptDecryptEmptyFile 测试空文件的加密解密
func TestEncryptDecryptEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	plainPath := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(plainPath, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	encryptedPath := filepath.Join(tmpDir, "empty.enc")
	iv, err := e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}

	decryptedPath := filepath.Join(tmpDir, "decrypted_empty.txt")
	if err := e.DecryptFile(encryptedPath, decryptedPath, iv); err != nil {
		t.Fatalf("DecryptFile failed: %v", err)
	}

	decrypted, err := os.ReadFile(decryptedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("expected empty file, got %d bytes", len(decrypted))
	}
}

// TestEncryptDecryptBinaryData 测试二进制数据的加密解密
func TestEncryptDecryptBinaryData(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// 创建包含各种字节值的二进制数据
	originalContent := make([]byte, 1024)
	for i := range originalContent {
		originalContent[i] = byte(i)
	}
	// 在末尾添加全零和全 FF
	originalContent = append(originalContent, bytes.Repeat([]byte{0x00}, 128)...)
	originalContent = append(originalContent, bytes.Repeat([]byte{0xFF}, 128)...)

	plainPath := filepath.Join(tmpDir, "binary.bin")
	if err := os.WriteFile(plainPath, originalContent, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	encryptedPath := filepath.Join(tmpDir, "binary.enc")
	iv, err := e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}

	decryptedPath := filepath.Join(tmpDir, "decrypted.bin")
	if err := e.DecryptFile(encryptedPath, decryptedPath, iv); err != nil {
		t.Fatalf("DecryptFile failed: %v", err)
	}

	decrypted, err := os.ReadFile(decryptedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(originalContent, decrypted) {
		t.Fatal("decrypted binary content does not match original")
	}
}

// TestEncryptDecryptWrongIV 测试使用错误的 IV 解密应失败
func TestEncryptDecryptWrongIV(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	plainPath := filepath.Join(tmpDir, "plain.txt")
	if err := os.WriteFile(plainPath, []byte("test content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	encryptedPath := filepath.Join(tmpDir, "encrypted.enc")
	iv, err := e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}

	// 使用错误的 IV（翻转第一个字节）
	wrongIV := []byte(iv)
	if len(wrongIV) > 0 {
		if wrongIV[0] == 'A' {
			wrongIV[0] = 'B'
		} else {
			wrongIV[0] = 'A'
		}
	}

	decryptedPath := filepath.Join(tmpDir, "decrypted_wrong.txt")
	err = e.DecryptFile(encryptedPath, decryptedPath, string(wrongIV))
	if err == nil {
		t.Fatal("expected error when decrypting with wrong IV, got nil")
	}
}

// TestEncryptFileNotFound 测试加密不存在的文件
func TestEncryptFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	_, err = e.EncryptFile("/nonexistent/file.txt", filepath.Join(tmpDir, "out.enc"))
	if err == nil {
		t.Fatal("expected error when encrypting nonexistent file, got nil")
	}
}

// TestDecryptFileNotFound 测试解密不存在的文件
func TestDecryptFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	err = e.DecryptFile("/nonexistent/file.enc", filepath.Join(tmpDir, "out.txt"), "dummy-iv")
	if err == nil {
		t.Fatal("expected error when decrypting nonexistent file, got nil")
	}
}

// TestEncryptedFileStructure 验证加密文件的二进制结构
func TestEncryptedFileStructure(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// 创建刚好一个 chunk 大小的文件
	content := make([]byte, 256*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}
	plainPath := filepath.Join(tmpDir, "chunk_sized.dat")
	if err := os.WriteFile(plainPath, content, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	encryptedPath := filepath.Join(tmpDir, "chunk_sized.enc")
	_, err = e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}

	data, err := os.ReadFile(encryptedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// 验证前 32 字节是 salt
	if len(data) <= keySize {
		t.Fatal("encrypted file too short")
	}

	// 验证 salt 后面紧跟着 nonce(12) + ciphertext+tag(256K+16)
	remaining := data[keySize:]
	if len(remaining) < nonceSize {
		t.Fatal("missing nonce after salt")
	}

	// 每个 chunk 的结构应该是: nonce(12) + ciphertext+tag(chunkSize+16)
	// 验证至少有一个完整的 chunk
	chunk1Nonce := remaining[:nonceSize]
	if len(chunk1Nonce) != nonceSize {
		t.Fatalf("invalid nonce size: %d", len(chunk1Nonce))
	}
}

// TestMultipleEncryptsProduceDifferentOutput 测试相同内容多次加密产生不同输出
func TestMultipleEncryptsProduceDifferentOutput(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	plainPath := filepath.Join(tmpDir, "plain.txt")
	if err := os.WriteFile(plainPath, []byte("identical content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 第一次加密
	enc1 := filepath.Join(tmpDir, "enc1.enc")
	iv1, err := e.EncryptFile(plainPath, enc1)
	if err != nil {
		t.Fatalf("EncryptFile 1 failed: %v", err)
	}

	// 第二次加密
	enc2 := filepath.Join(tmpDir, "enc2.enc")
	iv2, err := e.EncryptFile(plainPath, enc2)
	if err != nil {
		t.Fatalf("EncryptFile 2 failed: %v", err)
	}

	// 两次加密的输出应该不同（因为 salt 和 nonce 是随机的）
	data1, _ := os.ReadFile(enc1)
	data2, _ := os.ReadFile(enc2)
	if bytes.Equal(data1, data2) {
		t.Fatal("two encryptions of the same content produced identical output")
	}
	if iv1 == iv2 {
		t.Fatal("two encryptions of the same content produced identical IVs")
	}

	// 但两次解密都应该得到相同的内容
	dec1 := filepath.Join(tmpDir, "dec1.txt")
	dec2 := filepath.Join(tmpDir, "dec2.txt")
	if err := e.DecryptFile(enc1, dec1, iv1); err != nil {
		t.Fatalf("DecryptFile 1 failed: %v", err)
	}
	if err := e.DecryptFile(enc2, dec2, iv2); err != nil {
		t.Fatalf("DecryptFile 2 failed: %v", err)
	}

	d1, _ := os.ReadFile(dec1)
	d2, _ := os.ReadFile(dec2)
	if !bytes.Equal(d1, d2) {
		t.Fatal("decrypted outputs from two encryptions differ")
	}
}

// TestDeriveDEKUniqueness 测试每次派生的 DEK 都不同
func TestDeriveDEKUniqueness(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	// 使用不同的 salt 派生 DEK
	salt1 := []byte("salt1-salt1-salt1-salt1-salt1-salt1") // 32 bytes
	salt2 := []byte("salt2-salt2-salt2-salt2-salt2-salt2")

	dek1, err := e.deriveDEK(salt1)
	if err != nil {
		t.Fatalf("deriveDEK 1 failed: %v", err)
	}
	dek2, err := e.deriveDEK(salt2)
	if err != nil {
		t.Fatalf("deriveDEK 2 failed: %v", err)
	}

	if bytes.Equal(dek1, dek2) {
		t.Fatal("different salts produced the same DEK")
	}
}

// TestTamperedCiphertext 测试篡改密文后解密应失败
func TestTamperedCiphertext(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.bin")

	e, err := NewEncryptor(keyPath)
	if err != nil {
		t.Fatalf("NewEncryptor failed: %v", err)
	}

	plainPath := filepath.Join(tmpDir, "plain.txt")
	if err := os.WriteFile(plainPath, []byte("secret content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	encryptedPath := filepath.Join(tmpDir, "encrypted.enc")
	iv, err := e.EncryptFile(plainPath, encryptedPath)
	if err != nil {
		t.Fatalf("EncryptFile failed: %v", err)
	}

	// 读取并篡改密文（修改 salt 后面的某个字节）
	data, err := os.ReadFile(encryptedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	// 修改 salt 之后的第一个字节（nonce 的第一个字节）
	data[keySize] ^= 0xFF

	// 写回篡改后的密文
	if err := os.WriteFile(encryptedPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	decryptedPath := filepath.Join(tmpDir, "decrypted_tampered.txt")
	err = e.DecryptFile(encryptedPath, decryptedPath, iv)
	if err == nil {
		t.Fatal("expected error when decrypting tampered ciphertext, got nil")
	}
}
