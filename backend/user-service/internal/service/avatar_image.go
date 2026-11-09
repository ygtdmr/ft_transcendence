package service

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Limits on an uploaded avatar.
const (
	// MaxAvatarBytes is the largest accepted upload, 2 MiB.
	MaxAvatarBytes   = 2 << 20
	minAvatarSide    = 32
	maxAvatarSide    = 4096
	maxAvatarPixels  = 16_000_000
	avatarOutputSide = 256
)

// ErrInvalidImage reports an upload that is not an acceptable image.
var ErrInvalidImage = errors.New("invalid image")

type imageFormat struct {
	config func(*bytes.Reader) (image.Config, error)
	decode func(*bytes.Reader) (image.Image, error)
}

// avatarFormats lists the only accepted types, keyed by what the bytes are.
var avatarFormats = map[string]imageFormat{
	"image/jpeg": {
		config: func(r *bytes.Reader) (image.Config, error) { return jpeg.DecodeConfig(r) },
		decode: func(r *bytes.Reader) (image.Image, error) { return jpeg.Decode(r) },
	},
	"image/png": {
		config: func(r *bytes.Reader) (image.Config, error) { return png.DecodeConfig(r) },
		decode: func(r *bytes.Reader) (image.Image, error) { return png.Decode(r) },
	},
	"image/webp": {
		config: func(r *bytes.Reader) (image.Config, error) { return webp.DecodeConfig(r) },
		decode: func(r *bytes.Reader) (image.Image, error) { return webp.Decode(r) },
	},
}

// NormalizeAvatar validates raw from its bytes alone and returns a new 256x256
// PNG cropped to the center square. Dimensions are checked before pixels are
// decoded, so a small file cannot claim a huge image. Re-encoding keeps only
// pixels, so metadata and any data hidden in the file are dropped.
//
// Errors:
//
//   - ErrInvalidImage: raw is empty, over 2 MiB, not JPEG, PNG or WebP, corrupt,
//     or its dimensions are outside 32 to 4096 per side or 16 megapixels.
func NormalizeAvatar(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxAvatarBytes {
		return nil, ErrInvalidImage
	}
	format, known := avatarFormats[http.DetectContentType(raw)]
	if !known {
		return nil, ErrInvalidImage
	}
	config, err := format.config(bytes.NewReader(raw))
	if err != nil || !acceptableAvatarSize(config.Width, config.Height) {
		return nil, ErrInvalidImage
	}
	source, err := format.decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrInvalidImage
	}

	return encodeAvatar(source)
}

func acceptableAvatarSize(width, height int) bool {
	return width >= minAvatarSide && height >= minAvatarSide &&
		width <= maxAvatarSide && height <= maxAvatarSide && width*height <= maxAvatarPixels
}

func encodeAvatar(source image.Image) ([]byte, error) {
	bounds := source.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	corner := image.Pt(bounds.Min.X+(bounds.Dx()-side)/2, bounds.Min.Y+(bounds.Dy()-side)/2)
	crop := image.Rectangle{Min: corner, Max: corner.Add(image.Pt(side, side))}

	target := image.NewNRGBA(image.Rect(0, 0, avatarOutputSide, avatarOutputSide))
	draw.CatmullRom.Scale(target, target.Bounds(), source, crop, draw.Src, nil)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, target); err != nil {
		return nil, err
	}

	return encoded.Bytes(), nil
}
