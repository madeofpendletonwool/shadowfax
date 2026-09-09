package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Encryption model
//
// Every uploaded byte is encrypted at rest with AES-256-GCM. Keys are layered:
//
//	master key (KEK)  - from ENCRYPTION_KEY, else generated and kept in $DATA_DIR/.encryption-key
//	  wraps
//	data key (DEK)    - fresh 32 random bytes per file (and per text note),
//	                    stored only in its wrapped form in the database
//	  encrypts
//	file body         - chunked AES-256-GCM (the STREAM construction), so files
//	                    of any size are encrypted and decrypted incrementally
//	                    without ever being held in memory.
//
// Because the wrapped DEK lives in the database row, deleting that row destroys
// the only copy of the key: an expired transfer is cryptographically shredded
// even if its bytes linger on the underlying disk.
const (
	masterKeyLen = 32
	dekLen       = 32

	streamMagic     = "SFX1"
	streamVersion   = byte(1)
	noncePrefixLen  = 7
	streamHeaderLen = len(streamMagic) + 1 + noncePrefixLen

	// chunkSize is the plaintext size of each authenticated chunk. Each chunk
	// costs a 16-byte tag, so 64 KiB keeps the overhead at ~0.02%.
	chunkSize = 64 * 1024

	keyFileName = ".encryption-key"
)

var (
	errCorruptStream = errors.New("ciphertext is corrupt, truncated, or was encrypted with a different key")
	errNotEncrypted  = errors.New("not an encrypted stream")
)

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != dekLen {
		return nil, fmt.Errorf("key must be %d bytes, got %d", dekLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func newDEK() ([]byte, error) {
	key := make([]byte, dekLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}
	return key, nil
}

// makeNonce builds the 12-byte GCM nonce for one chunk: a per-stream random
// prefix, the chunk counter, and a final-chunk flag. Authenticating the flag is
// what makes truncating a stream detectable rather than silent.
func makeNonce(prefix [noncePrefixLen]byte, counter uint32, last bool) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix[:])
	binary.BigEndian.PutUint32(nonce[noncePrefixLen:], counter)
	if last {
		nonce[11] = 1
	}
	return nonce
}

// encryptStream encrypts src into dst and reports how many plaintext bytes it
// consumed. That count is the size shown to users and sent as Content-Length,
// so it must be the plaintext size, not the on-disk size.
func encryptStream(dst io.Writer, src io.Reader, key []byte) (int64, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return 0, err
	}

	var prefix [noncePrefixLen]byte
	if _, err := rand.Read(prefix[:]); err != nil {
		return 0, fmt.Errorf("generate nonce prefix: %w", err)
	}

	header := make([]byte, 0, streamHeaderLen)
	header = append(header, streamMagic...)
	header = append(header, streamVersion)
	header = append(header, prefix[:]...)
	if _, err := dst.Write(header); err != nil {
		return 0, err
	}

	plain := make([]byte, chunkSize)
	ct := make([]byte, 0, chunkSize+aead.Overhead())

	var total int64
	var counter uint32
	for {
		n, readErr := io.ReadFull(src, plain)
		last := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
		if readErr != nil && !last {
			return total, fmt.Errorf("read plaintext: %w", readErr)
		}

		ct = aead.Seal(ct[:0], makeNonce(prefix, counter, last), plain[:n], nil)
		if _, err := dst.Write(ct); err != nil {
			return total, err
		}
		total += int64(n)

		if last {
			return total, nil
		}
		counter++
		if counter == 0 {
			return total, errors.New("file too large: chunk counter overflowed")
		}
	}
}

// decryptStream reverses encryptStream. A full-size chunk is never the last one
// (encryptStream always emits a final, possibly empty, chunk), so a short read
// unambiguously marks the end of the stream.
func decryptStream(dst io.Writer, src io.Reader, key []byte) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}

	header := make([]byte, streamHeaderLen)
	if _, err := io.ReadFull(src, header); err != nil {
		return fmt.Errorf("read stream header: %w", err)
	}
	if string(header[:len(streamMagic)]) != streamMagic {
		return errNotEncrypted
	}
	if v := header[len(streamMagic)]; v != streamVersion {
		return fmt.Errorf("unsupported stream version %d", v)
	}
	var prefix [noncePrefixLen]byte
	copy(prefix[:], header[len(streamMagic)+1:])

	ct := make([]byte, chunkSize+aead.Overhead())
	plain := make([]byte, 0, chunkSize)

	var counter uint32
	for {
		n, readErr := io.ReadFull(src, ct)
		last := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
		if readErr != nil && !last {
			return fmt.Errorf("read ciphertext: %w", readErr)
		}
		if n < aead.Overhead() {
			return errCorruptStream
		}

		plain, err = aead.Open(plain[:0], makeNonce(prefix, counter, last), ct[:n], nil)
		if err != nil {
			return errCorruptStream
		}
		if _, err := dst.Write(plain); err != nil {
			return err
		}

		if last {
			return nil
		}
		counter++
		if counter == 0 {
			return errCorruptStream
		}
	}
}

// sealBytes encrypts a small value (a text note) with a one-shot nonce prefix.
func sealBytes(key, plaintext []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

func openBytes(key, sealed []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errCorruptStream
	}
	nonce, body := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	out, err := aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, errCorruptStream
	}
	return out, nil
}

// wrapDEK / unwrapDEK protect a data key with the master key. The wrapped form
// is what lives in the database.
func wrapDEK(master, dek []byte) ([]byte, error) { return sealBytes(master, dek) }
func unwrapDEK(master, wrapped []byte) ([]byte, error) {
	dek, err := openBytes(master, wrapped)
	if err != nil {
		return nil, fmt.Errorf("unwrap data key: %w", err)
	}
	if len(dek) != dekLen {
		return nil, errCorruptStream
	}
	return dek, nil
}

// parseKeyMaterial accepts a 32-byte key as base64 (standard or URL, padded or
// not) or as hex, so operators can paste whatever their tooling produced.
func parseKeyMaterial(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		if key, err := decode(s); err == nil && len(key) == masterKeyLen {
			return key, nil
		}
	}
	return nil, fmt.Errorf(
		"encryption key must decode to %d bytes from base64 or hex; generate one with: openssl rand -base64 %d",
		masterKeyLen, masterKeyLen)
}

// loadMasterKey resolves the master key: ENCRYPTION_KEY if set, otherwise a
// key persisted in the data directory, generating it on first boot.
func loadMasterKey(dataDir string) ([]byte, error) {
	if env := os.Getenv("ENCRYPTION_KEY"); strings.TrimSpace(env) != "" {
		key, err := parseKeyMaterial(env)
		if err != nil {
			return nil, fmt.Errorf("ENCRYPTION_KEY: %w", err)
		}
		log.Printf("encryption: using master key from ENCRYPTION_KEY")
		return key, nil
	}

	path := filepath.Join(dataDir, keyFileName)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, parseErr := parseKeyMaterial(string(data))
		if parseErr != nil {
			return nil, fmt.Errorf(
				"%s is unreadable (%w); restore it or existing transfers cannot be decrypted", path, parseErr)
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o077 != 0 {
			log.Printf("WARNING: %s is readable by other users (mode %#o); run: chmod 600 %s",
				path, info.Mode().Perm(), path)
		}
		log.Printf("encryption: using master key from %s", path)
		return key, nil

	case !os.IsNotExist(err):
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	key := make([]byte, masterKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := writeKeyFile(path, key); err != nil {
		return nil, err
	}
	log.Printf("encryption: generated a new master key at %s", path)
	log.Printf("WARNING: the master key is stored beside the data it protects, so a copy of the " +
		"data directory is enough to decrypt it. Set ENCRYPTION_KEY to keep them apart. " +
		"Back this key up: losing it makes every stored transfer unrecoverable.")
	return key, nil
}

// writeKeyFile writes the key 0600 via a temp file and rename, so a crash can
// never leave a half-written key that would strand the data.
func writeKeyFile(path string, key []byte) error {
	encoded := base64.StdEncoding.EncodeToString(key)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := f.WriteString(encoded + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}
