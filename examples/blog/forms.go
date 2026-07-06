package blog

import (
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/forms"
)

func NewPostForm(data map[string]any) *forms.Form {
	return forms.NewForm(forms.FormOptions{
		Data: data,
		Fields: map[string]*forms.Field{
			"title":        forms.CharField(forms.FieldOptions{Required: true, Label: "Title"}),
			"slug":         forms.SlugField(forms.FieldOptions{Required: true, Label: "Slug"}),
			"body":         forms.CharField(forms.FieldOptions{Required: true, Label: "Body"}),
			"status":       forms.ChoiceField(forms.FieldOptions{Required: true, Label: "Status"}, statusChoices()),
			"published_at": forms.DateTimeField(forms.FieldOptions{Required: false, Label: "Published at"}),
		},
		FieldOrder: []string{"title", "slug", "body", "status", "published_at"},
		Groups: []forms.FieldGroup{
			{Name: "Content", Fields: []string{"title", "slug", "body"}},
			{Name: "Publishing", Fields: []string{"status", "published_at"}},
		},
		Clean: func(form *forms.Form) error {
			if strings.EqualFold(fmt.Sprint(form.CleanedData["status"]), "published") && form.CleanedData["published_at"] == nil {
				return fmt.Errorf("published posts require a publication timestamp")
			}
			return nil
		},
	})
}

func NewCommentForm(data map[string]any) *forms.Form {
	return forms.NewForm(forms.FormOptions{
		Data: data,
		Fields: map[string]*forms.Field{
			"name":    forms.CharField(forms.FieldOptions{Required: true, Label: "Name"}),
			"email":   forms.EmailField(forms.FieldOptions{Required: true, Label: "Email"}),
			"body":    forms.CharField(forms.FieldOptions{Required: true, Label: "Comment"}),
			"consent": forms.BooleanField(forms.FieldOptions{Required: true, Label: "Consent"}),
		},
		FieldOrder: []string{"name", "email", "body", "consent"},
		Clean: func(form *forms.Form) error {
			consent, _ := form.CleanedData["consent"].(bool)
			if !consent {
				return fmt.Errorf("consent is required")
			}
			return nil
		},
	})
}

func statusChoices() []forms.Choice {
	return []forms.Choice{
		{Value: "draft", Label: "Draft"},
		{Value: "published", Label: "Published"},
		{Value: "archived", Label: "Archived"},
	}
}
