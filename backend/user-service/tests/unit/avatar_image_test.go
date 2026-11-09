package unit_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

const (
	losslessWebP = "UklGRiQAAABXRUJQVlA4TBcAAAAvJ8AJAAdQ5CrUo/8BICH8P69G9D81AAA="
	lossyWebP    = "UklGRk4AAABXRUJQVlA4IEIAAACQAwCdASooACgAPrVaqE8nJSQiI4gA4BaJZwDM/oAAI0KUepYAAP5vH/8BdO4dnP/5Sv58/IPiYS+4ikSmAq8AAAA="
	animatedWebP = "UklGRrIAAABXRUJQVlA4WAoAAAACAAAAJwAAJwAAQU5JTQYAAAAAAAAAAABBTk1GJgAAAAAAAAAAACcAACcAADIAAAJWUDhMDgAAAC8nwAkABxAR/Q9ERP8DQU5NRigAAAAAAAAAAAAnAAAnAAAyAAAAVlA4TA8AAAAvJ8AJAAcQhYr+ByKi/wEAQU5NRigAAAAAAAAAAAAnAAAnAAAyAAAAVlA4TA8AAAAvJ8AJAAcQBY3+ByKi/wEA"
)

func decodeBase64(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func solidImage(width, height int, fill color.Color) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.Set(x, y, fill)
		}
	}
	return canvas
}

func pngBytes(t *testing.T, source image.Image) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func jpegBytes(t *testing.T, source image.Image) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, nil); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// pngHeaderOnly claims the dimensions but holds no pixel data at all.
func pngHeaderOnly(width, height uint32) []byte {
	var header bytes.Buffer
	header.WriteString("IHDR")
	_ = binary.Write(&header, binary.BigEndian, width)
	_ = binary.Write(&header, binary.BigEndian, height)
	header.Write([]byte{8, 6, 0, 0, 0})

	var file bytes.Buffer
	file.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&file, binary.BigEndian, uint32(13))
	file.Write(header.Bytes())
	_ = binary.Write(&file, binary.BigEndian, crc32.ChecksumIEEE(header.Bytes()))
	return file.Bytes()
}

func wantAvatarPNG(t *testing.T, normalized []byte) image.Image {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(normalized))
	if err != nil {
		t.Fatalf("output is not PNG: %v", err)
	}
	if size := decoded.Bounds().Size(); size != image.Pt(256, 256) {
		t.Fatalf("output size = %v", size)
	}
	return decoded
}

// TestNormalizeAvatarAccepts checks every accepted format.
//
// Expected output: JPEG, PNG, lossless and lossy WebP each become a 256x256 PNG.
func TestNormalizeAvatarAccepts(t *testing.T) {
	blue := color.NRGBA{B: 255, A: 255}
	for name, raw := range map[string][]byte{
		"png":           pngBytes(t, solidImage(64, 64, blue)),
		"jpeg":          jpegBytes(t, solidImage(64, 64, blue)),
		"webp lossless": decodeBase64(t, losslessWebP),
		"webp lossy":    decodeBase64(t, lossyWebP),
		"smallest":      pngBytes(t, solidImage(32, 32, blue)),
	} {
		t.Run(name, func(t *testing.T) {
			normalized, err := service.NormalizeAvatar(raw)
			if err != nil {
				t.Fatalf("NormalizeAvatar = %v", err)
			}
			wantAvatarPNG(t, normalized)
		})
	}
}

// TestNormalizeAvatarCropsCenter checks which part of a wide image is kept.
//
// Expected output: only the middle third of a 3:1 image remains.
func TestNormalizeAvatarCropsCenter(t *testing.T) {
	source := solidImage(300, 100, color.NRGBA{R: 255, A: 255})
	for y := range 100 {
		for x := 100; x < 200; x++ {
			source.Set(x, y, color.NRGBA{B: 255, A: 255})
		}
	}
	normalized, err := service.NormalizeAvatar(pngBytes(t, source))
	if err != nil {
		t.Fatal(err)
	}
	decoded := wantAvatarPNG(t, normalized)
	for _, point := range []image.Point{{0, 0}, {255, 0}, {128, 128}, {0, 255}, {255, 255}} {
		if r, _, b, _ := decoded.At(point.X, point.Y).RGBA(); r != 0 || b != 0xffff {
			t.Fatalf("pixel %v is outside the center: r=%d b=%d", point, r, b)
		}
	}
}

// TestNormalizeAvatarRejects checks every kind of unacceptable file.
//
// Expected output: each returns ErrInvalidImage. Header-only files show that
// dimensions are refused before any pixel is decoded.
func TestNormalizeAvatarRejects(t *testing.T) {
	var animated bytes.Buffer
	frame := image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{color.Black, color.White})
	if err := gif.Encode(&animated, frame, nil); err != nil {
		t.Fatal(err)
	}
	valid := pngBytes(t, solidImage(64, 64, color.NRGBA{A: 255}))

	for name, raw := range map[string][]byte{
		"empty":          {},
		"text":           []byte("definitely not an image"),
		"html":           []byte("<html><script>alert(1)</script></html>"),
		"svg":            []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"html then png":  append([]byte("<html><script>alert(1)</script>"), valid...),
		"gif":            animated.Bytes(),
		"over 2 MiB":     append(bytes.Clone(valid), make([]byte, service.MaxAvatarBytes)...),
		"truncated png":  valid[:len(valid)/2],
		"header only":    pngHeaderOnly(64, 64),
		"too narrow":     pngHeaderOnly(31, 64),
		"too short":      pngHeaderOnly(64, 31),
		"too wide":       pngHeaderOnly(4097, 64),
		"too tall":       pngHeaderOnly(64, 4097),
		"too many":       pngHeaderOnly(4096, 4096),
		"overflow claim": pngHeaderOnly(1<<31-1, 1<<31-1),
		"corrupt webp":   decodeBase64(t, losslessWebP)[:20],
		"animated webp":  decodeBase64(t, animatedWebP),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.NormalizeAvatar(raw); !errors.Is(err, service.ErrInvalidImage) {
				t.Fatalf("NormalizeAvatar = %v, want ErrInvalidImage", err)
			}
		})
	}
}

// TestNormalizeAvatarDropsHiddenData checks a valid image carrying other content.
//
// Expected output: the image is accepted and the stored bytes hold only pixels.
func TestNormalizeAvatarDropsHiddenData(t *testing.T) {
	const payload = "<script>alert(document.cookie)</script>"
	raw := append(pngBytes(t, solidImage(64, 64, color.NRGBA{G: 255, A: 255})), payload...)
	normalized, err := service.NormalizeAvatar(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(normalized), payload) {
		t.Fatal("the stored file keeps the hidden data")
	}
}
