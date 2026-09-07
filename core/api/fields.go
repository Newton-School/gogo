package api

import (
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/models"
)

func FloatField(name string, options ...models.FieldOption) Field {
	return Scalar(models.FloatField(name, options...))
}
func UUIDField(name string, options ...models.FieldOption) Field {
	return Scalar(models.UUIDField(name, options...))
}
func URLField(name string, options ...models.FieldOption) Field {
	return Scalar(models.URLField(name, options...))
}
func EmailField(name string, options ...models.FieldOption) Field {
	return Scalar(models.EmailField(name, options...))
}
func SlugField(name string, options ...models.FieldOption) Field {
	return Scalar(models.SlugField(name, options...))
}
func IPAddressField(name string, options ...models.FieldOption) Field {
	return Scalar(models.GenericIPAddressField(name, options...))
}
func DateField(name string, options ...models.FieldOption) Field {
	return Scalar(models.DateField(name, options...))
}
func DateTimeField(name string, options ...models.FieldOption) Field {
	return Scalar(models.DateTimeField(name, options...))
}
func TimeField(name string, options ...models.FieldOption) Field {
	return Scalar(models.TimeField(name, options...))
}
func DurationField(name string, options ...models.FieldOption) Field {
	return Scalar(models.DurationField(name, options...))
}
func ChoiceField(field Field, choices ...models.Choice) Field {
	field.Model.Choices = append([]models.Choice(nil), choices...)
	return field
}

// Uploaded files are write-only by default. Exposing a stored file requires an
// explicitly authorized URL or representation callback, never a disk pathname.
func FileField(name string, maxBytes int64) Field {
	if maxBytes == 0 {
		maxBytes = 10 << 20
	}
	return Field{Name: name, Required: true, WriteOnly: true, Validate: func(ctx context.Context, value any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, ok := value.(*multipart.FileHeader)
		if !ok || file == nil || file.Size < 1 || maxBytes < 1 || file.Size > maxBytes {
			return nil, models.Invalid("invalid_file", "Submit a nonempty file within the size limit.")
		}
		if !utf8.ValidString(file.Filename) || file.Filename == "." || file.Filename == ".." || path.Base(file.Filename) != file.Filename || strings.ContainsAny(file.Filename, "\\\x00\r\n") {
			return nil, models.Invalid("invalid_name", "Invalid file name.")
		}
		return file, nil
	}}
}
func ImageField(name string, maxBytes, maxPixels int64) Field {
	f := FileField(name, maxBytes)
	validate := f.Validate
	if maxPixels == 0 {
		maxPixels = 40_000_000
	}
	if maxBytes == 0 {
		maxBytes = 10 << 20
	}
	f.Validate = func(ctx context.Context, value any) (any, error) {
		value, err := validate(ctx, value)
		if err != nil {
			return nil, err
		}
		file := value.(*multipart.FileHeader)
		stream, err := file.Open()
		if err != nil {
			return nil, models.Invalid("invalid_image", "Submit a valid image.")
		}
		defer stream.Close()
		info, _, err := image.DecodeConfig(io.LimitReader(stream, maxBytes+1))
		if err != nil || info.Width < 1 || info.Height < 1 || maxPixels < 1 || int64(info.Width) > maxPixels/int64(info.Height) {
			return nil, models.Invalid("invalid_image", "Image dimensions exceed the limit or the image is invalid.")
		}
		return value, nil
	}
	return f
}

type ModelOptions struct {
	Fields, Readonly, Writeonly []string
	Overrides                   map[string]Field
	Validate                    Validator
	// ResolveRelation must validate every ID against the current actor's query
	// scope. It returns the explicit value passed to the client's save policy.
	ResolveRelation   func(context.Context, models.Field, any) (any, error)
	RepresentRelation func(context.Context, models.Field, any) (any, error)
}

// FromModel uses an explicit allowlist and copies metadata. Save remains an
// explicit atomic policy; this function never implies nested model persistence.
func FromModel(schema models.Schema, options ModelOptions) (*Serializer, error) {
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	if len(options.Fields) == 0 {
		return nil, errors.New("api: ModelSerializer requires an explicit field allowlist")
	}
	schema = schema.Clone()
	fields := make([]Field, 0, len(options.Fields))
	for key := range options.Overrides {
		if !slices.Contains(options.Fields, key) {
			return nil, errors.New("api: override is not in the field allowlist")
		}
	}
	for _, key := range append(slices.Clone(options.Readonly), options.Writeonly...) {
		if !slices.Contains(options.Fields, key) {
			return nil, errors.New("api: direction field is not in the allowlist")
		}
	}
	for _, name := range options.Fields {
		metadata, ok := schema.Field(name)
		if !ok {
			return nil, errors.New("api: unknown model serializer field")
		}
		f := Scalar(metadata)
		// Model-derived nullable fields without uniqueness requirements may be
		// omitted. This does not give an explicitly declared scalar field an
		// implicit default, and PATCH still skips all omitted fields/defaults.
		uniqueInput := metadata.Unique
		for _, constraint := range schema.Constraints {
			if constraint.Kind == "unique" && slices.Contains(constraint.Fields, name) {
				uniqueInput = true
			}
		}
		if metadata.Null && !uniqueInput {
			f.Required = false
		}
		if metadata.Relation != nil {
			meta := metadata
			if options.ResolveRelation != nil {
				f.Validate = func(ctx context.Context, value any) (any, error) { return options.ResolveRelation(ctx, meta, value) }
			}
			if options.RepresentRelation != nil {
				f.Represent = func(ctx context.Context, value any) (any, error) { return options.RepresentRelation(ctx, meta, value) }
			}
		}
		if metadata.Kind == models.File {
			f = FileField(name, 0)
		}
		if metadata.Kind == models.Image {
			f = ImageField(name, 0, 0)
		}
		if override, ok := options.Overrides[name]; ok {
			f = override
			f.Name = name
		}
		if !metadata.IsEditable() || slices.Contains(options.Readonly, name) {
			f.ReadOnly = true
			f.Required = false
		}
		if slices.Contains(options.Writeonly, name) {
			f.WriteOnly = true
		}
		fields = append(fields, f)
	}
	return New(Definition{Fields: fields, Validate: options.Validate})
}
