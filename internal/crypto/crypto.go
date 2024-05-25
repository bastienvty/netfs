package crypto

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"io"
)

type Crypto interface {
	Encrypt(key, plaintext []byte) ([]byte, error)
	Decrypt(key, ciphertext []byte) ([]byte, error)
	EncryptRSA(pubKey *rsa.PublicKey, plaintext []byte) ([]byte, error)
	DecryptRSA(privKey *rsa.PrivateKey, ciphertext []byte) ([]byte, error)
	Sign(privKey *rsa.PrivateKey, data []byte) ([]byte, error)
	VerifySign(pubKey *rsa.PublicKey, data, signature []byte) error
}

type CryptoHelper struct {
}

func (c *CryptoHelper) Encrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) == 0 {
		return plaintext, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err.Error())
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Never use more than 2^32 random nonces with a given key because of the risk of a repeat.
	nonce := make([]byte, aesgcm.NonceSize()) // 12 bytes by default
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// encrypt an prepend the nonce to the ciphertext before returning it
	ciphertext := aesgcm.Seal(nonce, nonce, plaintext, nil)

	return ciphertext, nil
}

func (c *CryptoHelper) Decrypt(key, ciphertext []byte) ([]byte, error) {
	if len(key) == 0 {
		return ciphertext, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// the nonce is prepended to the cipher text so we need to make sure it is still there and length matches up
	nonceSize := aesgcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, err
	}

	// now we split the nonce from the ciptertext
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)

	return plaintext, err
}

func (c *CryptoHelper) EncryptRSA(pubKey *rsa.PublicKey, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	encrypted, err := rsa.EncryptOAEP(
		sha256.New(),
		rand.Reader,
		pubKey,
		plaintext,
		nil,
	)
	if err != nil {
		return nil, err
	}
	return encrypted, nil
}

func (c *CryptoHelper) DecryptRSA(privKey *rsa.PrivateKey, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	decrypted, err := rsa.DecryptOAEP(
		sha256.New(),
		rand.Reader,
		privKey,
		ciphertext,
		nil,
	)
	if err != nil {
		return nil, err
	}
	return decrypted, nil
}

func (c *CryptoHelper) Sign(privKey *rsa.PrivateKey, data []byte) ([]byte, error) {
	hash := sha256.New()
	hash.Write(data)
	hashSum := hash.Sum(nil)
	return rsa.SignPSS(rand.Reader, privKey, crypto.SHA256, hashSum, nil)
}

func (c *CryptoHelper) VerifySign(pubKey *rsa.PublicKey, data, signature []byte) error {
	hash := sha256.New()
	hash.Write(data)
	hashSum := hash.Sum(nil)
	return rsa.VerifyPSS(pubKey, crypto.SHA256, hashSum, signature, nil)
}
