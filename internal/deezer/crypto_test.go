package deezer

import (
	"bytes"
	"crypto/cipher"
	"math/rand"
	"testing"

	"golang.org/x/crypto/blowfish" //nolint:staticcheck // Deezer protocol requires Blowfish
)

// encryptStripe is the inverse of decryptStripe, used only by the test to build
// a synthetic encrypted stream.
func encryptStripe(bf cipher.Block, stripe []byte) []byte {
	out := make([]byte, len(stripe))
	cipher.NewCBCEncrypter(bf, blowfishIV).CryptBlocks(out, stripe)
	return out
}

// buildEncrypted produces a BF_CBC_STRIPE stream from plaintext: every 6144
// block has its first 2048 bytes Blowfish-CBC encrypted, the rest left as-is.
func buildEncrypted(t *testing.T, sngID int64, plain []byte) []byte {
	t.Helper()
	bf, err := blowfish.NewCipher(blowfishKey(sngID))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 0, len(plain))
	for off := 0; off < len(plain); off += cipherBlock {
		end := off + cipherBlock
		if end > len(plain) {
			end = len(plain)
		}
		block := append([]byte{}, plain[off:end]...)
		if len(block) >= chunkStripe {
			enc := encryptStripe(bf, block[:chunkStripe])
			copy(block[:chunkStripe], enc)
		}
		out = append(out, block...)
	}
	return out
}

func TestDecryptTrackRoundTrip(t *testing.T) {
	sngID := int64(3135556)
	// Sizes chosen to hit: multi-block, a partial final block >2048, and a tiny
	// final block <2048 (passed through untouched).
	for _, size := range []int{cipherBlock, cipherBlock*3 + 4000, cipherBlock*2 + 1000, 500} {
		plain := make([]byte, size)
		rand.New(rand.NewSource(int64(size))).Read(plain)

		enc := buildEncrypted(t, sngID, plain)
		var got bytes.Buffer
		n, err := DecryptTrack(&got, bytes.NewReader(enc), sngID)
		if err != nil {
			t.Fatalf("size %d: DecryptTrack: %v", size, err)
		}
		if n != int64(size) {
			t.Fatalf("size %d: wrote %d bytes", size, n)
		}
		if !bytes.Equal(got.Bytes(), plain) {
			t.Fatalf("size %d: round-trip mismatch", size)
		}
	}
}

func TestDecryptTrackResumeEquivalence(t *testing.T) {
	sngID := int64(42)
	size := cipherBlock*4 + 123
	plain := make([]byte, size)
	rand.New(rand.NewSource(7)).Read(plain)
	enc := buildEncrypted(t, sngID, plain)

	// Decrypt the first two blocks, then "resume" from a block boundary and
	// decrypt the remainder; the concatenation must equal a full decrypt.
	boundary := AlignResumeOffset(cipherBlock*2 + 10)
	if boundary != cipherBlock*2 {
		t.Fatalf("AlignResumeOffset = %d, want %d", boundary, cipherBlock*2)
	}
	var part1, part2 bytes.Buffer
	if _, err := DecryptTrack(&part1, bytes.NewReader(enc[:boundary]), sngID); err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptTrack(&part2, bytes.NewReader(enc[boundary:]), sngID); err != nil {
		t.Fatal(err)
	}
	joined := append(part1.Bytes(), part2.Bytes()...)
	if !bytes.Equal(joined, plain) {
		t.Fatal("resumed decrypt differs from full decrypt")
	}
}

func TestBlowfishKeyDeterministic(t *testing.T) {
	k1 := blowfishKey(3135556)
	k2 := blowfishKey(3135556)
	if !bytes.Equal(k1, k2) || len(k1) != 16 {
		t.Fatalf("key not deterministic/len16: %x %x", k1, k2)
	}
	if bytes.Equal(k1, blowfishKey(3135557)) {
		t.Fatal("keys should differ per track id")
	}
}

func TestLegacyStreamURL(t *testing.T) {
	url, err := legacyStreamURL(3135556, "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix([]byte(url), []byte("https://e-cdns-proxy-a.dzcdn.net/mobile/1/")) {
		t.Fatalf("unexpected url: %s", url)
	}
}
