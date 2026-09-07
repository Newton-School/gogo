package models

type FieldOption func(*Field)

func NewField(name string, kind Kind, options ...FieldOption) Field {
	f := Field{Name: name, Kind: kind, Editable: true}
	if f.IsAuto() {
		f.PrimaryKey = true
		f.Editable = false
	}
	if kind == Generated {
		f.Editable = false
	}
	for _, option := range options {
		option(&f)
	}
	return f
}
func Nullable(f *Field)                       { f.Null = true }
func Optional(f *Field)                       { f.Blank = true }
func ReadOnly(f *Field)                       { f.Editable = false }
func Primary(f *Field)                        { f.PrimaryKey = true }
func UniqueValue(f *Field)                    { f.Unique = true }
func WithColumn(name string) FieldOption      { return func(f *Field) { f.Column = name } }
func WithStructField(name string) FieldOption { return func(f *Field) { f.StructField = name } }
func WithMaxLength(n int) FieldOption         { return func(f *Field) { f.MaxLength = n } }
func WithMinLength(n int) FieldOption         { return func(f *Field) { f.MinLength = n } }
func WithAllowUnicode(allow bool) FieldOption { return func(f *Field) { f.AllowUnicode = allow } }
func WithDBIndex(indexed bool) FieldOption    { return func(f *Field) { f.DBIndex = indexed } }
func WithPrecision(digits, places int) FieldOption {
	return func(f *Field) { f.MaxDigits = digits; f.DecimalPlaces = places }
}
func WithDefault(value any) FieldOption { return func(f *Field) { f.Default = value } }
func WithDefaultFunc(id string, fn func() any) FieldOption {
	return func(f *Field) { f.DefaultID = id; f.DefaultFunc = fn }
}
func WithChoices(choices ...Choice) FieldOption {
	return func(f *Field) { f.Choices = append([]Choice(nil), choices...) }
}
func WithLabel(label string) FieldOption   { return func(f *Field) { f.Label = label } }
func WithHelpText(text string) FieldOption { return func(f *Field) { f.HelpText = text } }
func WithValidators(validators ...Validator) FieldOption {
	return func(f *Field) { f.Validators = append([]Validator(nil), validators...) }
}
func WithRelation(relation Relation) FieldOption         { return func(f *Field) { f.Relation = &relation } }
func WithBounds(min, max any) FieldOption                { return func(f *Field) { f.Min = min; f.Max = max } }
func SmallIntegerField(n string, o ...FieldOption) Field { return NewField(n, SmallInteger, o...) }
func IntegerField(n string, o ...FieldOption) Field      { return NewField(n, Integer, o...) }
func BigIntegerField(n string, o ...FieldOption) Field   { return NewField(n, BigInteger, o...) }
func PositiveSmallIntegerField(n string, o ...FieldOption) Field {
	return NewField(n, PositiveSmallInteger, o...)
}
func PositiveIntegerField(n string, o ...FieldOption) Field {
	return NewField(n, PositiveInteger, o...)
}
func PositiveBigIntegerField(n string, o ...FieldOption) Field {
	return NewField(n, PositiveBigInteger, o...)
}
func SmallAutoField(n string, o ...FieldOption) Field { return NewField(n, SmallAuto, o...) }
func AutoField(n string, o ...FieldOption) Field      { return NewField(n, Auto, o...) }
func BigAutoField(n string, o ...FieldOption) Field   { return NewField(n, BigAuto, o...) }
func UUIDField(n string, o ...FieldOption) Field      { return NewField(n, UUID, o...) }
func DecimalField(n string, d, p int, o ...FieldOption) Field {
	return NewField(n, Decimal, append([]FieldOption{WithPrecision(d, p)}, o...)...)
}
func FloatField(n string, o ...FieldOption) Field   { return NewField(n, Float, o...) }
func BooleanField(n string, o ...FieldOption) Field { return NewField(n, Boolean, o...) }
func CharField(n string, o ...FieldOption) Field    { return NewField(n, Char, o...) }
func TextField(n string, o ...FieldOption) Field    { return NewField(n, Text, o...) }
func SlugField(n string, o ...FieldOption) Field {
	return NewField(n, Slug, append([]FieldOption{WithMaxLength(50), WithDBIndex(true)}, o...)...)
}
func EmailField(n string, o ...FieldOption) Field { return NewField(n, Email, o...) }
func URLField(n string, o ...FieldOption) Field   { return NewField(n, URL, o...) }
func GenericIPAddressField(n string, o ...FieldOption) Field {
	return NewField(n, GenericIPAddress, o...)
}
func FilePathField(n string, o ...FieldOption) Field { return NewField(n, FilePath, o...) }
func DateField(n string, o ...FieldOption) Field     { return NewField(n, Date, o...) }
func DateTimeField(n string, o ...FieldOption) Field { return NewField(n, DateTime, o...) }
func TimeField(n string, o ...FieldOption) Field     { return NewField(n, Time, o...) }
func DurationField(n string, o ...FieldOption) Field { return NewField(n, Duration, o...) }
func BinaryField(n string, o ...FieldOption) Field   { return NewField(n, Binary, o...) }
func JSONField(n string, o ...FieldOption) Field     { return NewField(n, JSON, o...) }
func FileField(n string, o ...FieldOption) Field     { return NewField(n, File, o...) }
func ImageField(n string, o ...FieldOption) Field    { return NewField(n, Image, o...) }
func ForeignKeyField(n string, r Relation, o ...FieldOption) Field {
	return NewField(n, ForeignKey, append([]FieldOption{WithRelation(r)}, o...)...)
}
func OneToOneField(n string, r Relation, o ...FieldOption) Field {
	return NewField(n, OneToOne, append([]FieldOption{WithRelation(r), UniqueValue}, o...)...)
}
func ManyToManyField(n string, r Relation, o ...FieldOption) Field {
	return NewField(n, ManyToMany, append([]FieldOption{WithRelation(r)}, o...)...)
}
