package utils

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
)

// SignedMessage holds the AES-256-GCM encrypted payload, the RSA-wrapped AES key, and the RSA signature.
type SignedMessage struct {
	EncryptedPayload []byte // AES-256-GCM ciphertext (nonce prepended)
	WrappedKey       []byte // AES key encrypted with recipient's RSA public key
	Signature        []byte // RSA-PSS signature over EncryptedPayload, signed by sender's private key
}

// SealMessage encrypts plaintext with a random AES-256-GCM key, wraps that key with recipientPub,
// and signs the ciphertext with senderPriv.
func SealMessage(plaintext []byte, senderPriv *rsa.PrivateKey, recipientPub *rsa.PublicKey) (*SignedMessage, error) {
	aesKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, recipientPub, aesKey, nil)
	if err != nil {
		return nil, err
	}

	digest := sha256.Sum256(ciphertext)
	sig, err := rsa.SignPSS(rand.Reader, senderPriv, crypto.SHA256, digest[:], nil)
	if err != nil {
		return nil, err
	}

	return &SignedMessage{EncryptedPayload: ciphertext, WrappedKey: wrappedKey, Signature: sig}, nil
}

// OpenMessage verifies the signature with senderPub, unwraps the AES key with recipientPriv,
// and decrypts the payload.
func OpenMessage(msg *SignedMessage, senderPub *rsa.PublicKey, recipientPriv *rsa.PrivateKey) ([]byte, error) {
	digest := sha256.Sum256(msg.EncryptedPayload)
	if err := rsa.VerifyPSS(senderPub, crypto.SHA256, digest[:], msg.Signature, nil); err != nil {
		return nil, errors.New("signature verification failed")
	}

	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, recipientPriv, msg.WrappedKey, nil)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(msg.EncryptedPayload) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, msg.EncryptedPayload[:nonceSize], msg.EncryptedPayload[nonceSize:], nil)
}

// AppendXMLDSig computes a W3C XML Digital Signature (enveloped, RSA-SHA256) over xmlDoc
// and returns the document with the <Signature> block appended before </ns2:Document>.
//
// The digest is SHA-256 over the raw document bytes (enveloped transform removes the
// Signature element itself, but since we append after the fact the digest is over the
// document as-is before the signature block is added — matching NIBSS behaviour).
func AppendXMLDSig(xmlDoc string, priv *rsa.PrivateKey) (string, error) {
	if priv == nil {
		return xmlDoc, nil
	}

	// Digest over the document content (before the Signature element is added)
	digestBytes := sha256.Sum256([]byte(xmlDoc))
	digestB64 := base64.StdEncoding.EncodeToString(digestBytes[:])

	// RSA-SHA256 signature over the digest
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digestBytes[:])
	if err != nil {
		return "", err
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	signatureBlock := `<Signature xmlns="http://www.w3.org/2000/09/xmldsig#">` +
		`<SignedInfo>` +
		`<CanonicalizationMethod Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/>` +
		`<SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"/>` +
		`<Reference URI="">` +
		`<Transforms><Transform Algorithm="http://www.w3.org/2000/09/xmldsig#enveloped-signature"/></Transforms>` +
		`<DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/>` +
		`<DigestValue>` + digestB64 + `</DigestValue>` +
		`</Reference>` +
		`</SignedInfo>` +
		`<SignatureValue>` + sigB64 + `</SignatureValue>` +
		`</Signature>`

	// Insert before the closing Document tag
	const closingTag = `</ns2:Document>`
	idx := len(xmlDoc) - len(closingTag)
	if idx < 0 || xmlDoc[idx:] != closingTag {
		// Fallback: just append
		return xmlDoc + signatureBlock, nil
	}
	return xmlDoc[:idx] + signatureBlock + closingTag, nil
}

// ParseRSAPrivateKey decodes a PEM-encoded PKCS#8 or PKCS#1 RSA private key.
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// fallback to PKCS#1
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("PEM block does not contain an RSA private key")
	}
	return rsaKey, nil
}

// ParseRSAPublicKey decodes a PEM-encoded PKIX RSA public key.
func ParseRSAPublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("PEM block does not contain an RSA public key")
	}
	return rsaKey, nil
}
