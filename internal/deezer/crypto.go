package deezer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"

	"golang.org/x/crypto/blowfish"
)

// Deezer's fixed secrets (public knowledge, identical across all clients).
const (
	blowfishSecret = "g4el58wc0zvf9na1"
	legacyAESKey   = "jo6aey6haid2Teih"
	// legacySep is the "¤" separator as a single latin1 byte, matching how
	// deemix builds the legacy stream path (Buffer.from(s, "binary")).
	legacySep = 0xA4
)

const (
	// chunkStripe is the size of the encrypted portion at the head of each block.
	chunkStripe = 2048
	// cipherBlock is the full stripe block: 2048 encrypted + 4096 plaintext.
	cipherBlock = chunkStripe * 3 // 6144
)

// blowfishIV is the fixed IV used for every stripe (bytes 0..7).
var blowfishIV = []byte{0, 1, 2, 3, 4, 5, 6, 7}

// blowfishKey derives the per-track Blowfish key from the SNG_ID:
//
//	h = md5_hex(ascii(sngID))
//	key[i] = h[i] ^ h[i+16] ^ SECRET[i]   for i in 0..15
func blowfishKey(sngID int64) []byte {
	sum := md5.Sum([]byte(strconv.FormatInt(sngID, 10)))
	h := hex.EncodeToString(sum[:]) // 32 lowercase hex chars
	key := make([]byte, 16)
	for i := 0; i < 16; i++ {
		key[i] = h[i] ^ h[i+16] ^ blowfishSecret[i]
	}
	return key
}

// decryptStripe decrypts a single 2048-byte stripe in place-safe fashion.
func decryptStripe(bf cipher.Block, stripe []byte) []byte {
	out := make([]byte, len(stripe))
	cipher.NewCBCDecrypter(bf, blowfishIV).CryptBlocks(out, stripe)
	return out
}

// DecryptTrack streams the encrypted body from r, decrypts it (BF_CBC_STRIPE),
// and writes the plaintext to w, returning the number of plaintext bytes
// written. It processes the stream in 6144-byte blocks, decrypting only the
// first 2048 bytes of each block and passing the rest through unchanged.
//
// Because each block re-uses the same IV, blocks are independent: resuming a
// download at a multiple of 6144 (see cipherBlock) and continuing to write at
// the same output offset yields a byte-identical file. atStart must be true only
// when writing from offset 0.
//
// For FLAC/MP3 the decrypted stream always begins with a valid header
// ("fLaC"/"ID3"/0xFF frame sync), so no leading-pad stripping is needed and the
// output length equals the source length block-for-block.
func DecryptTrack(w io.Writer, r io.Reader, sngID int64) (int64, error) {
	bf, err := blowfish.NewCipher(blowfishKey(sngID))
	if err != nil {
		return 0, fmt.Errorf("blowfish init: %w", err)
	}
	buf := make([]byte, cipherBlock)
	var written int64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			block := buf[:n]
			if n >= chunkStripe {
				dec := decryptStripe(bf, block[:chunkStripe])
				copy(block[:chunkStripe], dec)
			}
			if _, werr := w.Write(block); werr != nil {
				return written, werr
			}
			written += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

// AlignResumeOffset rounds a byte offset down to the nearest stripe-block
// boundary so a resumed download stays cipher-aligned.
func AlignResumeOffset(n int64) int64 {
	return n - (n % cipherBlock)
}

// aesECBEncrypt encrypts data (a multiple of the AES block size) with AES-128 in
// ECB mode and no padding, matching deemix's _ecbCrypt.
func aesECBEncrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(data)%bs != 0 {
		return nil, fmt.Errorf("ecb: data not a multiple of block size")
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += bs {
		block.Encrypt(out[i:i+bs], data[i:i+bs])
	}
	return out, nil
}

// legacyStreamURL builds the pre-media.deezer.com CDN URL for a track. Used as a
// fallback when media.deezer.com/v1/get_url does not return a source. md5Origin
// is the track's MD5_ORIGIN; formatCode is the numeric Deezer format.
func legacyStreamURL(sngID int64, md5Origin string, mediaVersion, formatCode int) (string, error) {
	if md5Origin == "" {
		return "", fmt.Errorf("legacy url: empty md5 origin")
	}
	sep := []byte{legacySep}
	// urlPart = md5 ¤ format ¤ sngID ¤ mediaVersion
	urlPart := []byte(md5Origin)
	urlPart = append(urlPart, sep...)
	urlPart = append(urlPart, []byte(strconv.Itoa(formatCode))...)
	urlPart = append(urlPart, sep...)
	urlPart = append(urlPart, []byte(strconv.FormatInt(sngID, 10))...)
	urlPart = append(urlPart, sep...)
	urlPart = append(urlPart, []byte(strconv.Itoa(mediaVersion))...)

	inner := md5.Sum(urlPart)
	innerHex := []byte(hex.EncodeToString(inner[:]))

	// step2 = md5hex ¤ urlPart ¤  then '.'-pad to a multiple of 16 bytes.
	step2 := append([]byte{}, innerHex...)
	step2 = append(step2, sep...)
	step2 = append(step2, urlPart...)
	step2 = append(step2, sep...)
	if pad := 16 - (len(step2) % 16); pad != 16 {
		for i := 0; i < pad; i++ {
			step2 = append(step2, '.')
		}
	}

	enc, err := aesECBEncrypt([]byte(legacyAESKey), step2)
	if err != nil {
		return "", err
	}
	path := hex.EncodeToString(enc)
	host := md5Origin[0:1]
	return fmt.Sprintf("https://e-cdns-proxy-%s.dzcdn.net/mobile/1/%s", host, path), nil
}
