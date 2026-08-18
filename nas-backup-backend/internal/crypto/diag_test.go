package crypto

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestDiagCheckObject 临时诊断：验证 /tmp/obj53.enc 能否用当前主密钥+文件内嵌首个 nonce 解密。
// 通过表示对象自洽有效，问题在于 DB 记录的 encrypted_iv 与对象实际 nonce 不一致。
func TestDiagCheckObject(t *testing.T) {
	enc, err := NewEncryptor(filepath.Join("..", "..", "data", "master.key"))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	inPath := "/tmp/obj53.enc"
	data, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) < keySize+nonceSize {
		t.Fatalf("too small: %d", len(data))
	}
	// 提取文件内嵌的第一个 nonce（salt 之后 12 字节）
	firstNonce := data[keySize : keySize+nonceSize]
	iv := base64.StdEncoding.EncodeToString(firstNonce)
	fmt.Printf("first nonce base64 = %s\n", iv)

	outPath := "/tmp/obj53.decrypted"
	if err := enc.DecryptFile(inPath, outPath, iv); err != nil {
		fmt.Printf("RESULT=FAIL object NOT decryptable with current key+embedded nonce: %v\n", err)
		return
	}
	plain, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	preview := string(plain)
	if len(preview) > 300 {
		preview = preview[:300]
	}
	fmt.Printf("RESULT=OK decrypted %d bytes, preview:\n%s\n", len(plain), preview)
}
