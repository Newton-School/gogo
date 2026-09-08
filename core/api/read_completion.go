package api

import "net/http"

// resourceReadWriter guards both successful and error responses. WriteError
// does not return transfer errors, so this boundary must abort before a failed
// write can be mistaken for a completed response. Never retry or append JSON.
type resourceReadWriter struct{ http.ResponseWriter }

func (w resourceReadWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	if err != nil || n != len(body) {
		panic(http.ErrAbortHandler)
	}
	return n, err
}

func (w resourceReadWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
