package main

import (
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := newDEK()
	if err != nil {
		t.Fatalf("newDEK: %v", err)
	}
	return key
}

func TestStreamRoundTrip(t *testing.T) {
	key := testKey(t)

	// Sizes chosen around the chunk boundary, where an off-by-one in the
	// last-chunk logic would show up.
	sizes := []int{0, 1, 15, 16, 17, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 7}
	for _, size := range sizes {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}

		var ct bytes.Buffer
		n, err := encryptStream(&ct, bytes.NewReader(plain), key)
		if err != nil {
			t.Fatalf("size %d: encrypt: %v", size, err)
		}
		if n != int64(size) {
			t.Fatalf("size %d: reported %d plaintext bytes", size, n)
		}
		if ct.Len() <= size {
			t.Fatalf("size %d: ciphertext %d is not larger than plaintext", size, ct.Len())
		}
		// Only meaningful for a sample long enough that a chance match is
		// impossible; a lone random byte turns up in any buffer by accident.
		if size >= 32 && bytes.Contains(ct.Bytes(), plain[:32]) {
			t.Fatalf("size %d: plaintext is visible in the ciphertext", size)
		}

		var out bytes.Buffer
		if err := decryptStream(&out, bytes.NewReader(ct.Bytes()), key); err != nil {
			t.Fatalf("size %d: decrypt: %v", size, err)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Fatalf("size %d: round trip mismatch", size)
		}
	}
}

func TestStreamRejectsWrongKey(t *testing.T) {
	var ct bytes.Buffer
	if _, err := encryptStream(&ct, strings.NewReader("secret"), testKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := decryptStream(io.Discard, bytes.NewReader(ct.Bytes()), testKey(t)); err == nil {
		t.Fatal("decrypting with the wrong key succeeded")
	}
}

func TestStreamDetectsTampering(t *testing.T) {
	key := testKey(t)
	var ct bytes.Buffer
	if _, err := encryptStream(&ct, bytes.NewReader(make([]byte, 5000)), key); err != nil {
		t.Fatal(err)
	}
	b := ct.Bytes()
	b[len(b)-20] ^= 0x01 // flip a bit inside the ciphertext body

	if err := decryptStream(io.Discard, bytes.NewReader(b), key); err == nil {
		t.Fatal("tampered ciphertext decrypted cleanly")
	}
}

func TestStreamDetectsTruncation(t *testing.T) {
	key := testKey(t)
	var ct bytes.Buffer
	// Two full chunks plus a tail, so cutting at a chunk boundary is possible.
	if _, err := encryptStream(&ct, bytes.NewReader(make([]byte, 2*chunkSize+100)), key); err != nil {
		t.Fatal(err)
	}
	full := ct.Bytes()

	// Cut exactly at the first chunk boundary: the remaining bytes are a valid
	// chunk, so only the authenticated last-chunk flag catches this.
	cut := streamHeaderLen + chunkSize + 16
	if err := decryptStream(io.Discard, bytes.NewReader(full[:cut]), key); err == nil {
		t.Fatal("truncation at a chunk boundary went undetected")
	}
	if err := decryptStream(io.Discard, bytes.NewReader(full[:len(full)-5]), key); err == nil {
		t.Fatal("truncated tail went undetected")
	}
}

func TestWrapUnwrapDEK(t *testing.T) {
	master := testKey(t)
	dek := testKey(t)

	wrapped, err := wrapDEK(master, dek)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, dek) {
		t.Fatal("wrapped key contains the raw data key")
	}

	got, err := unwrapDEK(master, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrap returned a different key")
	}
	if _, err := unwrapDEK(testKey(t), wrapped); err == nil {
		t.Fatal("unwrapped with the wrong master key")
	}
}

func TestWrappingUsesFreshNonces(t *testing.T) {
	master := testKey(t)
	dek := testKey(t)
	a, _ := wrapDEK(master, dek)
	b, _ := wrapDEK(master, dek)
	if bytes.Equal(a, b) {
		t.Fatal("wrapping the same key twice produced identical output (nonce reuse)")
	}
}

func TestParseKeyMaterial(t *testing.T) {
	good := []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // base64, 32 bytes
		strings.Repeat("ab", 32),                       // hex
		"  " + strings.Repeat("cd", 32) + "\n",         // whitespace tolerated
	}
	for _, s := range good {
		if _, err := parseKeyMaterial(s); err != nil {
			t.Fatalf("parseKeyMaterial(%q): %v", s, err)
		}
	}
	for _, s := range []string{"", "short", strings.Repeat("ab", 16), "not base64 or hex!!"} {
		if _, err := parseKeyMaterial(s); err == nil {
			t.Fatalf("parseKeyMaterial(%q) accepted a bad key", s)
		}
	}
}
