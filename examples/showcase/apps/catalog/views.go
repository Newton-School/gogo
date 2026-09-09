package catalog

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"

	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/security"
)

//go:embed templates/*.html static/*.css
var assets embed.FS

var page = template.Must(template.ParseFS(assets, "templates/*.html"))

type pageData struct {
	Version, Title, CSRF, Message string
	Form                          template.HTML
	Groups                        []fieldGroup
}
type fieldGroup struct {
	ID, Name, Description string
	Entries               []fieldlab.Entry
}

func render(w http.ResponseWriter, status int, data pageData) {
	data.Version = gogo.Version
	var out bytes.Buffer
	if err := page.ExecuteTemplate(&out, "page.html", data); err != nil {
		http.Error(w, "Page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}

func Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", 405)
		return
	}
	render(w, http.StatusOK, pageData{Title: "One project. A complete developer journey."})
}

func Styles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", 405)
		return
	}
	content, err := assets.ReadFile("static/showcase.css")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(content)
}

func Fields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", 405)
		return
	}
	groups := []fieldGroup{{ID: "models", Name: "Model fields", Description: "Typed values, schema descriptors and PostgreSQL boundaries."}, {ID: "forms", Name: "Form fields", Description: "Request-local binding, validation and cleaned values."}, {ID: "widgets", Name: "Widgets", Description: "Escaped HTML rendered by public widget APIs."}, {ID: "limitations", Name: "Explicit limitations", Description: "Known missing features are not hidden behind successful examples."}}
	indices := map[string]int{"model": 0, "form": 1, "widget": 2, "limitation": 3}
	for _, entry := range fieldlab.Catalog() {
		index := indices[entry.Category]
		groups[index].Entries = append(groups[index].Entries, entry)
	}
	render(w, 200, pageData{Title: "The field and widget laboratory", Groups: groups})
}

func Form(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	options := []forms.Option{forms.WithContext(r.Context())}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form submission", 400)
			return
		}
		options = append(options, forms.WithData(r.PostForm))
	}
	declaration, err := fieldlab.Form()
	if err != nil {
		http.Error(w, "Form unavailable", 500)
		return
	}
	var fields []forms.Field
	for _, field := range declaration.Fields() {
		if field.Kind != forms.File && field.Kind != forms.Image {
			fields = append(fields, field)
		}
	}
	form, err := forms.New(fields, options...)
	if err != nil {
		http.Error(w, "Form unavailable", 500)
		return
	}
	data := pageData{Title: "Try the form fields", CSRF: security.CSRFToken(r)}
	status := http.StatusOK
	if form.IsBound() {
		if form.IsValid() {
			data.Message = "Validation passed. Nothing was stored or sent."
		} else {
			status = http.StatusUnprocessableEntity
			data.Message = "Review the field errors below. No data was stored."
		}
	}
	data.Form, err = form.Render("div")
	if err != nil {
		http.Error(w, "Form unavailable", 500)
		return
	}
	render(w, status, data)
}
