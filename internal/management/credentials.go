package management

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DatabaseCredential carries sealed credentials only inside configuration transactions.
type DatabaseCredential struct {
	DatabaseID string `json:"-"`
	Ciphertext []byte `json:"-"`
}

func (s *Store) credentialKey(id string, create bool) ([]byte, error) {
	source, err := s.credentialSource(id, create)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(source.Path), "credentials.key")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		if err := source.withState(context.Background(), false, func(st *sourceState) error {
			if len(st.Credentials) > 0 {
				return errors.New("数据库凭据加密密钥缺失，请恢复密钥备份")
			}
			return nil
		}); err != nil {
			return nil, err
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".credential-key-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(file.Name())
		if _, err = file.Write(key); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if err = os.Link(file.Name(), path); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, errors.New("无法读取数据库凭据加密密钥，请检查服务端密钥文件")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("数据库凭据密钥必须是仅当前用户可读写的普通文件")
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		return nil, errors.New("数据库凭据加密密钥无效")
	}
	return key, nil
}
func (s *Store) credentialCipher(id string, create bool) (cipher.AEAD, error) {
	key, err := s.credentialKey(id, create)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Store) SealDatabaseCredential(id, password string) (DatabaseCredential, error) {
	if id == "" || len(password) == 0 || len(password) > 16384 {
		return DatabaseCredential{}, errors.New("需要数据库密码，最多 16384 字节")
	}
	aead, err := s.credentialCipher(id, true)
	if err != nil {
		return DatabaseCredential{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return DatabaseCredential{}, err
	}
	return DatabaseCredential{DatabaseID: id, Ciphertext: aead.Seal(nonce, nonce, []byte(password), []byte(id))}, nil
}
func (s *Store) DatabasePassword(ctx context.Context, id string) (string, error) {
	source, err := s.credentialSource(id, false)
	if err != nil {
		return "", err
	}
	purpose := "READ"
	if id != source.ID {
		purpose = "WRITE"
	}
	var sealed []byte
	if err := source.withState(ctx, false, func(st *sourceState) error {
		raw := st.Credentials[purpose]
		if raw == "" {
			return errors.New("数据库连接密码尚未保存")
		}
		var e error
		sealed, e = base64.StdEncoding.DecodeString(raw)
		return e
	}); err != nil {
		return "", err
	}
	aead, err := s.credentialCipher(id, false)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize() {
		return "", errors.New("数据库连接密码密文无效")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(id))
	if err != nil {
		return "", errors.New("无法解密数据库连接密码，请检查密钥备份")
	}
	return string(plain), nil
}

func WriteCredentialID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("write_%x", sum[:16])
}

func (s *Store) credentialSource(key string, create bool) (*Source, error) {
	ids, _ := s.sourceIDs()
	for _, id := range ids {
		if key == id || key == WriteCredentialID(id) {
			return s.source(context.Background(), id, create)
		}
	}
	if create && sourceID.MatchString(key) && !strings.HasPrefix(key, "write_") {
		return s.source(context.Background(), key, true)
	}
	return nil, errors.New("数据库凭据引用无效")
}
func (s *Store) openLegacyCredential(id string, sealed []byte) (string, error) {
	key, e := os.ReadFile(s.Path + ".credentials.key")
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("旧凭据格式无效")
	}
	plain, e := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(id))
	return string(plain), e
}

func verifyCiphertext(key []byte, id, encoded string) error {
	sealed, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		return errors.New("连接密码密文格式无效")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return e
	}
	if len(sealed) < gcm.NonceSize() {
		return errors.New("连接密码密文无效")
	}
	if _, e = gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(id)); e != nil {
		return errors.New("连接密码解密校验失败")
	}
	return nil
}
