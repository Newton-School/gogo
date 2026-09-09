package catalog

import "github.com/Newton-School/gogo/core/forms"

// EnquiryForm is request-local. Validation demonstrates the shared field layer;
// successful submissions do not send mail or persist an enquiry implicitly.
func EnquiryForm(options ...forms.Option) (*forms.Form, error) {
	return forms.New([]forms.Field{
		forms.NewField("name", forms.Char),
		forms.NewField("email", forms.Email),
		forms.NewField("quantity", forms.Integer),
	}, options...)
}
