package requests

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

// startTestHTTPServer creates the shared HTTP fixture used by client and
// response behavior tests.
func startTestHTTPServer() *httptest.Server {
	handler := http.NewServeMux()
	handler.HandleFunc("/test-get", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "GET response")
	})

	handler.HandleFunc("/test-post", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "POST response")
	})

	handler.HandleFunc("/test-put", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "PUT response")
	})

	handler.HandleFunc("/test-delete", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "DELETE response")
	})

	handler.HandleFunc("/test-patch", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "PATCH response")
	})

	handler.HandleFunc("/test-status-code", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintln(w, `Created`)
	})

	handler.HandleFunc("/test-headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "TestValue")
		_, _ = fmt.Fprintln(w, `Headers test`)
	})

	handler.HandleFunc("/test-cookies", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "test-cookie", Value: "cookie-value"})
		_, _ = fmt.Fprintln(w, `Cookies test`)
	})

	handler.HandleFunc("/test-body", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "This is the response body.")
	})

	handler.HandleFunc("/test-empty", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler.HandleFunc("/test-json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"message": "This is a JSON response", "status": true}`)
	})

	handler.HandleFunc("/test-xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintln(w, `<Response><Message>This is an XML response</Message><Status>true</Status></Response>`)
	})

	handler.HandleFunc("/test-text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, `This is a text response`)
	})

	handler.HandleFunc("/test-pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = fmt.Fprintln(w, `This is a PDF response`)
	})

	handler.HandleFunc("/test-redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/test-redirected", http.StatusFound)
	})

	handler.HandleFunc("/test-redirected", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "Redirected")
	})

	handler.HandleFunc("/test-failure", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	})

	return httptest.NewServer(handler)
}
