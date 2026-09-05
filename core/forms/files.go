package forms

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
)

func (f Field) cleanFile(raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	file, ok := raw.(*multipart.FileHeader)
	if !ok || file == nil {
		return nil, f.failure("invalid", "Submit a valid file.")
	}
	max := f.MaxBytes
	if max <= 0 {
		max = 10 << 20
	}
	if file.Size > max || file.Size <= 0 {
		return nil, f.failure("invalid_size", "The file is empty or too large.")
	}
	if strings.ContainsAny(file.Filename, "\x00\r\n") || filepath.Base(file.Filename) != file.Filename || strings.Contains(file.Filename, `\`) {
		return nil, f.failure("invalid_name", "The file name is invalid.")
	}
	if f.Kind == Image {
		stream, err := file.Open()
		if err != nil {
			return nil, f.failure("invalid_image", "Submit a valid image.")
		}
		defer stream.Close()
		info, _, err := image.DecodeConfig(io.LimitReader(stream, max+1))
		if err != nil || info.Width <= 0 || info.Height <= 0 || int64(info.Width)*int64(info.Height) > 40_000_000 {
			return nil, f.failure("invalid_image", "Submit a valid image.")
		}
	}
	return file, nil
}
