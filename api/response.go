package api

import (
	"encoding/json"
	"net/http"
	"os"

	frameworkhttp "github.com/Newton-School/gogo/http"
)

// APIError is the normalized API error body.
type APIError struct {
	Code      string              `json:"code"`
	Message   string              `json:"message"`
	Fields    map[string][]string `json:"fields,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
}

// Response is an API response writer.
type Response struct {
	status      int
	body        any
	contentType string
	filePath    string
	noBody      bool
	header      http.Header
}

// JSON creates a JSON response.
func JSON(status int, body any) Response {
	return Response{status: status, body: body, contentType: "application/json"}
}

// Created creates a 201 JSON response.
func Created(body any) Response {
	return JSON(http.StatusCreated, body)
}

// Accepted creates a 202 JSON response.
func Accepted(body any) Response {
	return JSON(http.StatusAccepted, body)
}

// NoContent creates a 204 response.
func NoContent() Response {
	return Response{status: http.StatusNoContent, noBody: true}
}

// Error creates a normalized JSON error response.
func Error(status int, err APIError) Response {
	return JSON(status, map[string]APIError{"error": err})
}

// File creates a file response.
func File(path, contentType string) Response {
	return Response{status: http.StatusOK, filePath: path, contentType: contentType}
}

// Header returns mutable response headers.
func (r *Response) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

// Write writes the response to a standard response writer.
func (r Response) Write(w http.ResponseWriter) error {
	for key, values := range r.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if r.contentType != "" {
		w.Header().Set("Content-Type", r.contentType)
	}
	w.WriteHeader(r.status)
	if r.noBody {
		return nil
	}
	if r.filePath != "" {
		body, err := os.ReadFile(r.filePath)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	if r.contentType == "application/json" {
		return json.NewEncoder(w).Encode(r.body)
	}
	return nil
}

// HTTP converts an API response into a framework HTTP response.
func (r Response) HTTP() frameworkhttp.Response {
	var response frameworkhttp.Response
	switch {
	case r.filePath != "":
		response = frameworkhttp.File(r.filePath)
	case r.noBody:
		response = frameworkhttp.NoContent()
	case r.contentType == "application/json":
		response = frameworkhttp.JSON(r.status, r.body)
	default:
		response = frameworkhttp.Text(r.status, "")
	}
	if r.contentType != "" {
		response.Header().Set("Content-Type", r.contentType)
	}
	for key, values := range r.header {
		for _, value := range values {
			response.Header().Add(key, value)
		}
	}
	return response
}
